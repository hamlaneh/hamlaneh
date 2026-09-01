package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// inviteColumns is the projection every invite query selects, in the order
// scanInvite expects.
const inviteColumns = `i.id, i.note, i.created_at, i.expires_at, u.id, u.username, u.display_name`

// openInvite is the predicate for "can still be redeemed": not spent, not
// revoked, not expired. Preview, redemption and the list all judge liveness
// by this one expression, so they can never disagree about it.
//
// PostgreSQL compares against now(); SQLite has no such function, so the
// expression ends in a bound parameter every caller fills with s.nowText().
// The comparison is a plain text one because the stored layout is fixed
// width and UTC, which makes lexicographic order chronological (codec.go).
const openInvite = `i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > ?`

// CreateInvite stores one invitation and returns it. tokenHash is the
// SHA-256 digest of the generated token; the raw value never reaches the
// database, so a stolen dump yields no usable invitation.
//
// PostgreSQL writes this as one data-modifying CTE — INSERT ... RETURNING
// feeding a join against users. SQLite has no data-modifying CTEs, so the
// insert and the read-back are two statements in one write transaction:
// nothing can come between them, because this transaction holds the
// database's write lock from BEGIN.
//
// The expiry is computed in Go rather than as now() + make_interval(), since
// SQLite has no interval type. It is the same clock either way: in home mode
// the application and the database are one process (sqlitestore.go).
func (s *Store) CreateInvite(ctx context.Context, createdBy uuid.UUID, tokenHash []byte, note string, ttl time.Duration) (storage.Invite, error) {
	var (
		id  = uuid.New()
		now = s.clock()
		inv storage.Invite
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO invites (id, token_hash, created_by, note, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, tokenHash, createdBy, note, asTime(now), asTime(now.Add(ttl)),
		); err != nil {
			return err
		}
		var scanErr error
		inv, scanErr = scanInvite(tx.QueryRowContext(ctx,
			`SELECT `+inviteColumns+`
			 FROM invites i JOIN users u ON u.id = i.created_by
			 WHERE i.id = ?`,
			id,
		))
		return scanErr
	})
	if err != nil {
		return storage.Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// ListOpenInvites returns one page of invitations that can still be
// redeemed, soonest expiry first. Spent, revoked and expired invitations are
// history the audit log keeps; the table's question is what is still live.
//
// The keyset is spelled out rather than compared as a tuple: PostgreSQL
// writes (i.expires_at, i.id) > ($1, $2) and SQLite has no row values, so
// the same predicate is the expanded disjunction. It walks the same index
// range, because both columns are TEXT in an order-preserving encoding.
func (s *Store) ListOpenInvites(ctx context.Context, params storage.ListInvitesParams) ([]storage.Invite, error) {
	var (
		afterExpires any
		afterID      *uuid.UUID
	)
	if params.After != nil {
		afterExpires = asTime(params.After.ExpiresAt)
		afterID = &params.After.ID
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+inviteColumns+`
		 FROM invites i JOIN users u ON u.id = i.created_by
		 WHERE `+openInvite+`
		   AND (? IS NULL OR i.expires_at > ? OR (i.expires_at = ? AND i.id > ?))
		 ORDER BY i.expires_at, i.id
		 LIMIT ?`,
		s.nowText(),
		afterExpires, afterExpires, afterExpires, afterID,
		params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	// See ListChannelsForUser on why the close error is discarded here.
	defer func() { _ = rows.Close() }()

	invites := []storage.Invite{}
	for rows.Next() {
		inv, scanErr := scanInvite(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list invites: %w", scanErr)
		}
		invites = append(invites, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	return invites, nil
}

// RevokeInvite closes an open invitation. It is idempotent: an invitation
// that is already spent, revoked or gone is left exactly as it is and
// reported as success, because the outcome the caller wanted is the outcome
// that holds.
func (s *Store) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE invites SET revoked_at = ?
		 WHERE id = ? AND accepted_at IS NULL AND revoked_at IS NULL`,
		s.nowText(), id,
	); err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	return nil
}

// OpenInviteByTokenHash resolves a presented token to a live invitation.
//
// Unknown, expired, revoked and already-used tokens are ONE answer here —
// ErrNotFound — because they are one answer in the contract: a guessed token
// must not be distinguishable from a spent one. The distinction is not
// available to be leaked further up, which is the point of collapsing it at
// the query rather than at the handler.
func (s *Store) OpenInviteByTokenHash(ctx context.Context, tokenHash []byte) (storage.Invite, error) {
	inv, err := scanInvite(s.db.QueryRowContext(ctx,
		`SELECT `+inviteColumns+`
		 FROM invites i JOIN users u ON u.id = i.created_by
		 WHERE i.token_hash = ? AND `+openInvite,
		tokenHash, s.nowText(),
	))
	if err != nil {
		return storage.Invite{}, fmt.Errorf("invite by token: %w", err)
	}
	return inv, nil
}

// RedeemInvite creates an account from an invitation and consumes the
// invitation, in one transaction. A link therefore cannot make two people:
// either both halves happen or neither does.
//
// Every unusable token — unknown, expired, revoked, already spent — answers
// ErrNotFound, exactly as OpenInviteByTokenHash does. Username and email
// conflicts answer ErrUsernameTaken / ErrEmailTaken, and burn nothing: the
// transaction rolls back, so the invitation is still live for the next try.
//
// # Why two people racing one link produce one account
//
// storage.RedeemInvite selects the invitation FOR UPDATE before it creates
// anything: the second racer blocks on that row lock, PostgreSQL re-evaluates
// its WHERE clause against the row as it now stands once the first commits,
// sees accepted_at set, and matches nothing. Without the lock both would
// read the invitation as open and both would insert.
//
// Here there is no row to lock and nothing to re-evaluate, because the two
// redemptions cannot overlap at all: this transaction takes the database's
// write lock at BEGIN (_txlock=immediate), so the second racer waits there
// and its SELECT is the FIRST thing it runs afterwards — against the
// committed state in which the invitation is already spent. Same one
// account, same honest ErrNotFound for everybody else.
//
// The PostgreSQL file's lock order (invites, then users) has no counterpart
// to state: there is one lock, and it is the whole database.
func (s *Store) RedeemInvite(ctx context.Context, tokenHash []byte, nu storage.NewUser) (storage.User, error) {
	var created storage.User

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var inviteID uuid.UUID
		err := tx.QueryRowContext(ctx,
			`SELECT i.id FROM invites i
			 WHERE i.token_hash = ? AND `+openInvite,
			tokenHash, s.nowText(),
		).Scan(&inviteID)
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("look up invite: %w", err)
		}

		now := s.clock()
		u, err := scanUser(tx.QueryRowContext(ctx,
			`INSERT INTO users (id, username, email, display_name, password_hash, locale, is_admin, must_change_password, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 RETURNING `+userColumns,
			uuid.New(), nu.Username, nullString(nu.Email), nu.DisplayName, nu.PasswordHash, nu.Locale,
			boolValue(nu.IsAdmin), boolValue(nu.MustChangePassword), asTime(now), asTime(now),
		))
		if err != nil {
			return mapUserConflict(err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE invites SET accepted_at = ?, accepted_by = ? WHERE id = ?`,
			asTime(now), u.ID, inviteID,
		); err != nil {
			return fmt.Errorf("consume invite: %w", err)
		}

		created = u
		return nil
	})
	if err != nil {
		return storage.User{}, fmt.Errorf("redeem invite: %w", err)
	}
	return created, nil
}

// scanInvite scans one inviteColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound.
func scanInvite(row rowScanner) (storage.Invite, error) {
	var inv storage.Invite
	err := row.Scan(
		&inv.ID, &inv.Note, timeScan{dst: &inv.CreatedAt}, timeScan{dst: &inv.ExpiresAt},
		&inv.CreatedBy.ID, &inv.CreatedBy.Username, &inv.CreatedBy.DisplayName,
	)
	if err != nil {
		return storage.Invite{}, notFound(err)
	}
	return inv, nil
}
