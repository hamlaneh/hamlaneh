package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Invite is one row of the invites table as the dashboard lists it. The
// token is deliberately absent: only its hash is stored, exactly as
// password_reset_tokens does it, so nothing here can redisplay a link.
type Invite struct {
	ID        uuid.UUID
	CreatedBy InviteCreator
	Note      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// InviteCreator is who issued a link: exactly the three fields the
// contract's UserSummary carries, and nothing an invite row has no business
// exposing.
type InviteCreator struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// InviteCursor is a keyset-pagination position: the (expires_at, id) of the
// last row of the previous page. It matches invites_open_idx, so a page is
// an index range scan.
type InviteCursor struct {
	ExpiresAt time.Time
	ID        uuid.UUID
}

// ListInvitesParams control one page of ListOpenInvites. After nil means the
// first page; Limit must be positive and callers own clamping it.
type ListInvitesParams struct {
	After *InviteCursor
	Limit int
}

// inviteColumns is the projection every invite query selects, in the order
// scanInvite expects.
const inviteColumns = `i.id, i.note, i.created_at, i.expires_at, u.id, u.username, u.display_name`

// openInvite is the predicate for "can still be redeemed": not spent, not
// revoked, not expired. Preview, redemption and the list all judge liveness
// by this one expression, so they can never disagree about it.
const openInvite = `i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()`

// CreateInvite stores one invitation and returns it. tokenHash is the
// SHA-256 digest of the generated token; the raw value never reaches the
// database, so a stolen dump yields no usable invitation.
func (s *Store) CreateInvite(ctx context.Context, createdBy uuid.UUID, tokenHash []byte, note string, ttl time.Duration) (Invite, error) {
	row := s.pool.QueryRow(ctx,
		`WITH new_invite AS (
		     INSERT INTO invites (token_hash, created_by, note, expires_at)
		     VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		     RETURNING id, note, created_at, expires_at, created_by
		 )
		 SELECT `+inviteColumns+`
		 FROM new_invite i JOIN users u ON u.id = i.created_by`,
		tokenHash, createdBy, note, ttl.Seconds(),
	)
	inv, err := scanInvite(row)
	if err != nil {
		return Invite{}, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// ListOpenInvites returns one page of invitations that can still be
// redeemed, soonest expiry first. Spent, revoked and expired invitations are
// history the audit log keeps; the table's question is what is still live.
func (s *Store) ListOpenInvites(ctx context.Context, params ListInvitesParams) ([]Invite, error) {
	var (
		afterExpires *time.Time
		afterID      *uuid.UUID
	)
	if params.After != nil {
		afterExpires = &params.After.ExpiresAt
		afterID = &params.After.ID
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+inviteColumns+`
		 FROM invites i JOIN users u ON u.id = i.created_by
		 WHERE `+openInvite+`
		   AND ($1::timestamptz IS NULL OR (i.expires_at, i.id) > ($1, $2))
		 ORDER BY i.expires_at, i.id
		 LIMIT $3`,
		afterExpires, afterID, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	invites := []Invite{}
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
	if _, err := s.pool.Exec(ctx,
		`UPDATE invites SET revoked_at = now()
		 WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`,
		id,
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
func (s *Store) OpenInviteByTokenHash(ctx context.Context, tokenHash []byte) (Invite, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+inviteColumns+`
		 FROM invites i JOIN users u ON u.id = i.created_by
		 WHERE i.token_hash = $1 AND `+openInvite,
		tokenHash,
	)
	inv, err := scanInvite(row)
	if err != nil {
		return Invite{}, fmt.Errorf("invite by token: %w", err)
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
// The SELECT takes a row lock on the invitation before anything is created.
// The second racer blocks on that lock; when the first commits, PostgreSQL
// re-evaluates the second's WHERE clause against the row as it now stands
// (READ COMMITTED re-checks a locked row after the blocking transaction
// ends), sees accepted_at set, and matches nothing — so it gets the same
// honest 404 a spent link always gets.
//
// Lock order: invites → users. Nothing in this package goes the other way —
// an account-scoped transaction never touches invites — so no cycle exists.
// The users row this inserts is brand new and can conflict with nobody.
func (s *Store) RedeemInvite(ctx context.Context, tokenHash []byte, nu NewUser) (User, error) {
	var created User

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var inviteID uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT i.id FROM invites i
			 WHERE i.token_hash = $1 AND `+openInvite+`
			 FOR UPDATE`,
			tokenHash,
		).Scan(&inviteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("look up invite: %w", err)
		}

		row := tx.QueryRow(ctx,
			`INSERT INTO users (username, email, display_name, password_hash, locale, is_admin, must_change_password)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING `+userColumns,
			nu.Username, nu.Email, nu.DisplayName, nu.PasswordHash, nu.Locale, nu.IsAdmin, nu.MustChangePassword,
		)
		u, err := scanUser(row)
		if err != nil {
			return mapUserConflict(err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE invites SET accepted_at = now(), accepted_by = $1 WHERE id = $2`,
			u.ID, inviteID,
		); err != nil {
			return fmt.Errorf("consume invite: %w", err)
		}

		created = u
		return nil
	})
	if err != nil {
		return User{}, fmt.Errorf("redeem invite: %w", err)
	}
	return created, nil
}

// scanInvite scans one inviteColumns row. pgx.ErrNoRows becomes ErrNotFound.
func scanInvite(row pgx.Row) (Invite, error) {
	var inv Invite
	err := row.Scan(
		&inv.ID, &inv.Note, &inv.CreatedAt, &inv.ExpiresAt,
		&inv.CreatedBy.ID, &inv.CreatedBy.Username, &inv.CreatedBy.DisplayName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	return inv, nil
}
