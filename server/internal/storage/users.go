package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors callers branch on with errors.Is.
var (
	// ErrNotFound reports that no row matched the lookup.
	ErrNotFound = errors.New("storage: not found")
	// ErrUsernameTaken reports a username uniqueness conflict.
	ErrUsernameTaken = errors.New("storage: username already taken")
	// ErrEmailTaken reports an email uniqueness conflict.
	ErrEmailTaken = errors.New("storage: email already taken")
	// ErrLastAdmin reports a change that would leave the instance with no
	// admin who can sign in. An instance nobody can administer is
	// unrecoverable without database access, so the change is refused
	// rather than warned about afterwards.
	ErrLastAdmin = errors.New("storage: would remove the last admin")
)

// User is a row of the users table. PasswordHash is the argon2id PHC string;
// it never leaves the server.
type User struct {
	ID           uuid.UUID
	Username     string
	Email        *string
	DisplayName  string
	PasswordHash string
	Locale       string
	IsAdmin      bool
	// IsActive is false for a deactivated account: it cannot sign in, and
	// deactivation revoked every session it held.
	IsActive           bool
	MustChangePassword bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewUser carries the fields for creating a user. Validation is the
// handler's job; the database constraints are the backstop.
type NewUser struct {
	Username           string
	Email              *string
	DisplayName        string
	PasswordHash       string
	Locale             string
	IsAdmin            bool
	MustChangePassword bool
}

// userColumns is the canonical column list every user query selects, in the
// order scanUser expects.
const userColumns = `id, username, email, display_name, password_hash, locale, is_admin, is_active, must_change_password, created_at, updated_at`

// CreateUser inserts a user and returns the stored row. Uniqueness
// conflicts map to ErrUsernameTaken / ErrEmailTaken.
func (s *Store) CreateUser(ctx context.Context, nu NewUser) (User, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, email, display_name, password_hash, locale, is_admin, must_change_password)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+userColumns,
		nu.Username, nu.Email, nu.DisplayName, nu.PasswordHash, nu.Locale, nu.IsAdmin, nu.MustChangePassword,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", mapUserConflict(err))
	}
	return u, nil
}

// UserByIdentifier looks a user up by username or email. Both columns are
// citext, so the match is case-insensitive. Returns ErrNotFound when no
// account matches.
func (s *Store) UserByIdentifier(ctx context.Context, identifier string) (User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1 OR email = $1`,
		identifier,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("user by identifier: %w", err)
	}
	return u, nil
}

// lockAccount takes the per-account serialization lock — the FIRST lock any
// account-scoped transaction takes (see the package's lock-order rule).
//
// Everything that has to be true of an account "as a whole" rests on this
// one lock: one live reset link, no session outliving a completed reset. A
// sweep cannot see a concurrent transaction's uncommitted INSERT, so the
// only way to be sure nothing was added behind it is to have excluded the
// other writer from the start.
//
// It selects the id rather than the whole row because the lock is the only
// thing anyone wants from it; a caller that also needs the account can read
// it under the lock it now holds.
//
// It reports ErrNotFound for an account that is gone; every table here
// cascades from users, so callers treat that as "nothing to act on".
func lockAccount(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	var locked uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock account: %w", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock account: %w", err)
	}
	return nil
}

// UserByID returns the user with the given id, or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("user by id: %w", err)
	}
	return u, nil
}

// UpdatePassword completes a password change in one transaction: it stores
// the new hash, clears must_change_password, and revokes every session
// family of the user except keepFamilyID (the session performing the
// change). Atomicity matters — a new password must never coexist with other
// live sessions of the old one.
func (s *Store) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, keepFamilyID uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE users SET password_hash = $1, must_change_password = false, updated_at = now() WHERE id = $2`,
			passwordHash, userID,
		)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("store new hash: %w", ErrNotFound)
		}

		_, err = tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now()
			 WHERE user_id = $1 AND family_id <> $2 AND revoked_at IS NULL`,
			userID, keepFamilyID,
		)
		if err != nil {
			return fmt.Errorf("revoke other session families: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// UpdatePasswordHash replaces only the stored hash, leaving
// must_change_password and sessions untouched. It exists for transparent
// rehash-on-login when the argon2 parameters are raised.
func (s *Store) UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`,
		passwordHash, userID,
	)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update password hash: %w", ErrNotFound)
	}
	return nil
}

// UserCursor is a keyset-pagination position: the (created_at, id) of the
// last row of the previous page.
type UserCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListUsersParams control one page of ListUsers. After nil means the first
// page. Limit must be positive; callers own clamping it to the API contract.
type ListUsersParams struct {
	After *UserCursor
	Limit int
}

// ListUsers returns one page of users in stable (created_at, id) order.
// Keyset pagination stays correct when rows are inserted between pages —
// every pre-existing row is returned exactly once.
func (s *Store) ListUsers(ctx context.Context, params ListUsersParams) ([]User, error) {
	var afterCreatedAt *time.Time
	var afterID *uuid.UUID
	if params.After != nil {
		afterCreatedAt = &params.After.CreatedAt
		afterID = &params.After.ID
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users
		 WHERE $1::timestamptz IS NULL OR (created_at, id) > ($1, $2)
		 ORDER BY created_at, id
		 LIMIT $3`,
		afterCreatedAt, afterID, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// CountUsers returns the total number of users. The admin bootstrap uses it
// to detect a fresh instance.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// AdminUserUpdate is the admin dashboard's patch of one account. A nil field
// is one the request did not mention and this call must not change.
type AdminUserUpdate struct {
	IsAdmin  *bool
	IsActive *bool
}

// adminSetLockKey is the advisory-lock key that serializes every change to
// the set of admins who can sign in. Its value is arbitrary and only has to
// be unique within this database.
const adminSetLockKey = 0x68616d6c // "haml"

// UpdateUserAdmin applies an admin's patch of one account and returns the
// stored row. It refuses with ErrLastAdmin any change that would leave the
// instance with no admin who can sign in, and it revokes every session
// family of an account it deactivates — in the same transaction, so a
// deactivated account can never be observed still holding one.
//
// # Why the last-admin rule is race-safe
//
// The rule is about a set, not a row: "at least one active admin exists".
// Two admins demoting each other at the same moment each read a set that
// still contains the other, and each would commit — leaving zero. Locking
// the two account rows does not help, because they are different rows.
//
// So the transaction takes one instance-wide advisory lock FIRST
// (pg_advisory_xact_lock, released at commit or rollback), which serializes
// every mutation of the admin set against every other. Under it the check is
// stated the simple way round: apply the change, then count the admins that
// remain, and return ErrLastAdmin — rolling the whole transaction back — if
// the count is zero. Counting after the write needs no case analysis over
// which field moved in which direction, and the count sees this
// transaction's own UPDATE.
//
// Lock order (package rule): advisory lock → users → sessions. The advisory
// lock is not a row lock and cannot deadlock against one, so taking it
// before lockAccount adds no cycle.
//
// Refusing to deactivate yourself is deliberately NOT here: it is a fact
// about the caller, not about the database, and the HTTP layer owns it.
func (s *Store) UpdateUserAdmin(ctx context.Context, userID uuid.UUID, upd AdminUserUpdate) (User, error) {
	var updated User

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(adminSetLockKey)); err != nil {
			return fmt.Errorf("lock admin set: %w", err)
		}
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}

		row := tx.QueryRow(ctx,
			`UPDATE users
			 SET is_admin   = COALESCE($1::boolean, is_admin),
			     is_active  = COALESCE($2::boolean, is_active),
			     updated_at = now()
			 WHERE id = $3
			 RETURNING `+userColumns,
			upd.IsAdmin, upd.IsActive, userID,
		)
		u, err := scanUser(row)
		if err != nil {
			return fmt.Errorf("update user: %w", err)
		}

		var admins int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM users WHERE is_admin AND is_active`,
		).Scan(&admins); err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if admins == 0 {
			return ErrLastAdmin
		}

		// Deactivation ends every session the account holds. A forced
		// password reset deliberately does not, which is the whole
		// difference between the two actions.
		if !u.IsActive {
			if _, err := tx.Exec(ctx,
				`UPDATE sessions SET revoked_at = now()
				 WHERE user_id = $1 AND revoked_at IS NULL`,
				userID,
			); err != nil {
				return fmt.Errorf("revoke session families: %w", err)
			}
		}

		updated = u
		return nil
	})
	if err != nil {
		return User{}, fmt.Errorf("update user admin: %w", err)
	}
	return updated, nil
}

// SetTemporaryPassword installs an admin-issued password and marks the
// account as owing a change. Sessions are deliberately untouched: this is
// the unlock path for somebody who forgot their password, not the
// offboarding path, and ending their session would make the two
// indistinguishable.
func (s *Store) SetTemporaryPassword(ctx context.Context, userID uuid.UUID, passwordHash string) (User, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE users
		 SET password_hash = $1, must_change_password = true, updated_at = now()
		 WHERE id = $2
		 RETURNING `+userColumns,
		passwordHash, userID,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("set temporary password: %w", err)
	}
	return u, nil
}

// scanUser scans one userColumns row. pgx.ErrNoRows becomes ErrNotFound.
func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.PasswordHash,
		&u.Locale, &u.IsAdmin, &u.IsActive, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// mapUserConflict translates unique-constraint violations on the users
// table into the sentinel errors handlers turn into 409s.
func mapUserConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.UniqueViolation {
		return err
	}
	switch pgErr.ConstraintName {
	case "users_username_key":
		return ErrUsernameTaken
	case "users_email_key":
		return ErrEmailTaken
	default:
		return err
	}
}
