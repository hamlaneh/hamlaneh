package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The users table's two projections and their scan live in projections.go,
// for the reason storage/users.go states at length: a third copy is not a
// shortcut, it is the bug. Select userColumns (or memberUserColumns when the
// query joins) and scan with scanUser.

// CreateUser inserts a user and returns the stored row. Uniqueness conflicts
// map to storage.ErrUsernameTaken / storage.ErrEmailTaken.
//
// The id and both timestamps are bound rather than defaulted: the PostgreSQL
// schema defaults them (gen_random_uuid(), now()), and this tree deliberately
// declares neither, so the driver supplies every generated value and the
// caller sees exactly what was stored.
func (s *Store) CreateUser(ctx context.Context, nu storage.NewUser) (storage.User, error) {
	now := s.nowText()
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO users (id, username, email, display_name, password_hash, locale, is_admin, must_change_password, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING `+userColumns,
		uuid.New(), nu.Username, nullString(nu.Email), nu.DisplayName, nu.PasswordHash, nu.Locale,
		boolValue(nu.IsAdmin), boolValue(nu.MustChangePassword), now, now,
	)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("create user: %w", mapUserConflict(err))
	}
	return u, nil
}

// UserByIdentifier looks a user up by username or email. Both columns carry
// the CITEXT collation, so the match is case-insensitive exactly as citext
// makes it on PostgreSQL. Returns storage.ErrNotFound when no account matches.
//
// The identifier is bound twice because SQLite placeholders are positional:
// there is no $1 to reuse.
func (s *Store) UserByIdentifier(ctx context.Context, identifier string) (storage.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? OR email = ?`,
		identifier, identifier,
	)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("user by identifier: %w", err)
	}
	return u, nil
}

// UserByID returns the user with the given id, or storage.ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (storage.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("user by id: %w", err)
	}
	return u, nil
}

// UpdatePassword completes a password change in one transaction: it stores
// the new hash, clears must_change_password, and revokes every session
// family of the user except keepFamilyID (the session performing the
// change). Atomicity matters — a new password must never coexist with other
// live sessions of the old one.
//
// One nowText serves both statements, which is what PostgreSQL's two calls to
// now() do there: now() is the transaction timestamp, identical at every call
// site inside one transaction.
func (s *Store) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, keepFamilyID uuid.UUID) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.nowText()

		res, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, must_change_password = 0, updated_at = ? WHERE id = ?`,
			passwordHash, now, userID,
		)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		changed, err := rowsAffected(res)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		if changed == 0 {
			return fmt.Errorf("store new hash: %w", storage.ErrNotFound)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ?
			 WHERE user_id = ? AND family_id <> ? AND revoked_at IS NULL`,
			now, userID, keepFamilyID,
		); err != nil {
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, s.nowText(), userID,
	)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	changed, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("update password hash: %w", storage.ErrNotFound)
	}
	return nil
}

// ListUsers returns one page of users in stable (created_at, id) order.
// Keyset pagination stays correct when rows are inserted between pages —
// every pre-existing row is returned exactly once.
//
// Two statements rather than one. PostgreSQL states the cursor as
// `$1::timestamptz IS NULL OR (created_at, id) > ($1, $2)`, which SQLite
// cannot express: there are no row values here, and an IS NULL OR disjunction
// over a bound parameter leaves the planner unable to use an ordered range
// scan for the first page. The tuple comparison expands to the equivalent
// `created_at > ? OR (created_at = ? AND id > ?)`, and the first page simply
// omits the clause.
func (s *Store) ListUsers(ctx context.Context, params storage.ListUsersParams) ([]storage.User, error) {
	query := `SELECT ` + userColumns + `
		 FROM users
		 ORDER BY created_at, id
		 LIMIT ?`
	args := []any{params.Limit}

	if params.After != nil {
		query = `SELECT ` + userColumns + `
		 FROM users
		 WHERE created_at > ? OR (created_at = ? AND id > ?)
		 ORDER BY created_at, id
		 LIMIT ?`
		after := asTime(params.After.CreatedAt)
		args = []any{after, after, params.After.ID, params.Limit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := []storage.User{}
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
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// UpdateUserProfile applies a user's patch of their own account and returns
// the stored row. storage.ErrNotFound reports an account that is already gone.
//
// No lock and no transaction: this is one row, and nothing here is a fact
// about a set the way the admin flags are — a display name and a locale
// cannot leave the instance in a state no one can recover from.
//
// The nil-means-unchanged patch needs no cast the way PostgreSQL's
// COALESCE($1::text, …) does: a nil *string binds as SQL NULL and COALESCE
// keeps the stored value.
func (s *Store) UpdateUserProfile(ctx context.Context, userID uuid.UUID, upd storage.UserProfileUpdate) (storage.User, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE users
		 SET display_name = COALESCE(?, display_name),
		     locale       = COALESCE(?, locale),
		     updated_at   = ?
		 WHERE id = ?
		 RETURNING `+userColumns,
		nullString(upd.DisplayName), nullString(upd.Locale), s.nowText(), userID,
	)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("update user profile: %w", err)
	}
	return u, nil
}

// UpdateUserAdmin applies an admin's patch of one account and returns the
// stored row. It refuses with storage.ErrLastAdmin any change that would
// leave the instance with no admin who can sign in, and it revokes every
// session family of an account it deactivates — in the same transaction, so a
// deactivated account can never be observed still holding one.
//
// # Why the last-admin rule is race-safe here
//
// The rule is about a set, not a row: "at least one active admin exists".
// Two admins demoting each other at the same moment each read a world that
// still contains the other, and on PostgreSQL each would commit — leaving
// zero. Locking the two account rows does not help, because they are
// different rows, so storage.UpdateUserAdmin takes one instance-wide
// pg_advisory_xact_lock FIRST and serializes every mutation of the admin set
// against every other.
//
// That advisory lock is exactly what SQLite does by existing. This
// transaction holds the database's write lock from BEGIN (_txlock=immediate),
// so the second demotion cannot start until the first has committed or rolled
// back, and it counts what the first left behind. The check is therefore
// stated the same way round as on PostgreSQL: apply the change, then count the
// admins that remain — the count sees this transaction's own UPDATE — and
// return storage.ErrLastAdmin, rolling the whole transaction back, when the
// count is zero. Counting after the write needs no case analysis over which
// field moved in which direction.
//
// What is lost is only PostgreSQL's concurrency shape, where an unrelated
// write proceeds while an admin change waits on the advisory lock; here every
// writer queues, briefly, at household scale.
//
// Refusing to deactivate yourself is deliberately NOT here: it is a fact
// about the caller, not about the database, and the HTTP layer owns it.
func (s *Store) UpdateUserAdmin(ctx context.Context, userID uuid.UUID, upd storage.AdminUserUpdate) (storage.User, error) {
	var updated storage.User

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.lockAccount takes the row FOR UPDATE here, as the first
		// lock of any account-scoped transaction, to exclude a concurrent
		// writer on the same account for the rest of the transaction. There
		// is no concurrent writer to exclude under a single writer, so no
		// lock is emitted — but the existence check it also performed is
		// kept, because an account that is already gone must still answer
		// storage.ErrNotFound rather than be reported as a failed update.
		var present uuid.UUID
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM users WHERE id = ?`, userID,
		).Scan(&present); err != nil {
			return fmt.Errorf("lock account: %w", notFound(err))
		}

		row := tx.QueryRowContext(ctx,
			`UPDATE users
			 SET is_admin   = COALESCE(?, is_admin),
			     is_active  = COALESCE(?, is_active),
			     updated_at = ?
			 WHERE id = ?
			 RETURNING `+userColumns,
			upd.IsAdmin, upd.IsActive, s.nowText(), userID,
		)
		u, err := scanUser(row)
		if err != nil {
			return fmt.Errorf("update user: %w", err)
		}

		// is_admin and is_active are INTEGER 0/1, so PostgreSQL's
		// `WHERE is_admin AND is_active` is spelled as the comparison it is.
		var admins int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM users WHERE is_admin = 1 AND is_active = 1`,
		).Scan(&admins); err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if admins == 0 {
			return storage.ErrLastAdmin
		}

		// Deactivation ends every session the account holds. A forced
		// password reset deliberately does not, which is the whole
		// difference between the two actions.
		if !u.IsActive {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sessions SET revoked_at = ?
				 WHERE user_id = ? AND revoked_at IS NULL`,
				s.nowText(), userID,
			); err != nil {
				return fmt.Errorf("revoke session families: %w", err)
			}
		}

		updated = u
		return nil
	})
	if err != nil {
		return storage.User{}, fmt.Errorf("update user admin: %w", err)
	}
	return updated, nil
}

// SetTemporaryPassword installs an admin-issued password and marks the
// account as owing a change. Sessions are deliberately untouched: this is
// the unlock path for somebody who forgot their password, not the
// offboarding path, and ending their session would make the two
// indistinguishable.
func (s *Store) SetTemporaryPassword(ctx context.Context, userID uuid.UUID, passwordHash string) (storage.User, error) {
	row := s.db.QueryRowContext(ctx,
		`UPDATE users
		 SET password_hash = ?, must_change_password = 1, updated_at = ?
		 WHERE id = ?
		 RETURNING `+userColumns,
		passwordHash, s.nowText(), userID,
	)
	u, err := scanUser(row)
	if err != nil {
		return storage.User{}, fmt.Errorf("set temporary password: %w", err)
	}
	return u, nil
}
