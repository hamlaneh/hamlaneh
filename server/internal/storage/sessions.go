package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Session is a row of the sessions table: one refresh-token generation
// inside a family. Token hashes stay out of the struct — they are only ever
// used as lookup keys.
type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	FamilyID         uuid.UUID
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	CreatedAt        time.Time
}

// SessionTokens carries the token hashes and metadata for one new session
// generation. TTLs are applied against the database clock (now()), so
// application clock skew cannot stretch a token's life.
type SessionTokens struct {
	AccessTokenHash  []byte
	RefreshTokenHash []byte
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	UserAgent        string
	IP               *netip.Addr
}

// NewSession creates the first generation of a fresh family (a login).
type NewSession struct {
	UserID uuid.UUID
	SessionTokens
}

// RotateOutcome reports how RotateSession classified a presented refresh
// token.
type RotateOutcome int

const (
	// RotateOutcomeInvalid means the token is unknown, revoked, or expired:
	// respond 401, nothing further to do.
	RotateOutcomeInvalid RotateOutcome = iota
	// RotateOutcomeRotated means the token was valid and the next
	// generation was created.
	RotateOutcomeRotated
	// RotateOutcomeReuseDetected means an already-used token was presented
	// — theft or replay — and the whole family has been revoked.
	RotateOutcomeReuseDetected
)

// sessionColumns is the canonical column list session queries return, in
// the order scanSession expects.
const sessionColumns = `id, user_id, family_id, access_expires_at, refresh_expires_at, created_at`

// CreateSession starts a new session family for a login and returns the
// first generation.
func (s *Store) CreateSession(ctx context.Context, ns NewSession) (Session, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO sessions
		     (user_id, family_id, refresh_token_hash, access_token_hash,
		      refresh_expires_at, access_expires_at, user_agent, ip)
		 VALUES ($1, gen_random_uuid(), $2, $3,
		         now() + make_interval(secs => $4), now() + make_interval(secs => $5), $6, $7)
		 RETURNING `+sessionColumns,
		ns.UserID, ns.RefreshTokenHash, ns.AccessTokenHash,
		ns.RefreshTTL.Seconds(), ns.AccessTTL.Seconds(), ns.UserAgent, ns.IP,
	)
	sess, err := scanSession(row)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// SessionUserByAccessHash authenticates an access token: it returns the
// live session matching the hash and its user in one query. A session is
// live when it is not revoked and its access token has not expired (checked
// against the database clock). Returns ErrNotFound otherwise.
func (s *Store) SessionUserByAccessHash(ctx context.Context, accessHash []byte) (Session, User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT s.id, s.user_id, s.family_id, s.access_expires_at, s.refresh_expires_at, s.created_at,
		        u.id, u.username, u.email, u.display_name, u.password_hash,
		        u.locale, u.is_admin, u.must_change_password, u.created_at, u.updated_at
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.access_token_hash = $1
		   AND s.revoked_at IS NULL
		   AND s.access_expires_at > now()`,
		accessHash,
	)

	var sess Session
	var u User
	err := row.Scan(
		&sess.ID, &sess.UserID, &sess.FamilyID, &sess.AccessExpiresAt, &sess.RefreshExpiresAt, &sess.CreatedAt,
		&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.PasswordHash,
		&u.Locale, &u.IsAdmin, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("session by access hash: %w", err)
	}
	return sess, u, nil
}

// RotateSession implements refresh-token rotation with reuse detection, in
// one transaction:
//
//   - unknown, revoked, or expired token → RotateOutcomeInvalid
//   - already-used token → the whole family is revoked (reuse means the
//     token leaked and was replayed) → RotateOutcomeReuseDetected
//   - valid token → the presented row is marked used and its access token
//     is retired immediately (access_expires_at = now()), and the next
//     generation is inserted in the same family → RotateOutcomeRotated
//
// The presented row is locked with SELECT ... FOR UPDATE, so two concurrent
// rotations of the same token serialize: the first wins, the second sees a
// used token and trips reuse detection.
//
// The used check deliberately precedes the expiry check: replaying a used
// token is a theft signal regardless of expiry, and revoking the family is
// the safe response.
func (s *Store) RotateSession(ctx context.Context, refreshHash []byte, next SessionTokens) (Session, RotateOutcome, error) {
	var (
		nextSess Session
		outcome  RotateOutcome
	)

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			id, userID, familyID   uuid.UUID
			revoked, used, expired bool
		)
		err := tx.QueryRow(ctx,
			`SELECT id, user_id, family_id,
			        revoked_at IS NOT NULL,
			        used_at IS NOT NULL,
			        refresh_expires_at <= now()
			 FROM sessions WHERE refresh_token_hash = $1
			 FOR UPDATE`,
			refreshHash,
		).Scan(&id, &userID, &familyID, &revoked, &used, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = RotateOutcomeInvalid
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up refresh token: %w", err)
		}

		switch {
		case revoked:
			outcome = RotateOutcomeInvalid
			return nil
		case used:
			if _, execErr := tx.Exec(ctx,
				`UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`,
				familyID,
			); execErr != nil {
				return fmt.Errorf("revoke family on reuse: %w", execErr)
			}
			slog.Warn("refresh token reuse detected; session family revoked",
				"user_id", userID, "family_id", familyID)
			outcome = RotateOutcomeReuseDetected
			return nil
		case expired:
			outcome = RotateOutcomeInvalid
			return nil
		}

		if _, execErr := tx.Exec(ctx,
			`UPDATE sessions SET used_at = now(), access_expires_at = now() WHERE id = $1`,
			id,
		); execErr != nil {
			return fmt.Errorf("retire rotated session: %w", execErr)
		}

		row := tx.QueryRow(ctx,
			`INSERT INTO sessions
			     (user_id, family_id, refresh_token_hash, access_token_hash,
			      refresh_expires_at, access_expires_at, user_agent, ip)
			 VALUES ($1, $2, $3, $4,
			         now() + make_interval(secs => $5), now() + make_interval(secs => $6), $7, $8)
			 RETURNING `+sessionColumns,
			userID, familyID, next.RefreshTokenHash, next.AccessTokenHash,
			next.RefreshTTL.Seconds(), next.AccessTTL.Seconds(), next.UserAgent, next.IP,
		)
		nextSess, err = scanSession(row)
		if err != nil {
			return fmt.Errorf("insert next generation: %w", err)
		}
		outcome = RotateOutcomeRotated
		return nil
	})
	if err != nil {
		return Session{}, RotateOutcomeInvalid, fmt.Errorf("rotate session: %w", err)
	}
	return nextSess, outcome, nil
}

// RevokeFamily revokes every not-yet-revoked row of a session family
// (logout, remote revocation).
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID,
	)
	if err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}

// scanSession scans one sessionColumns row. pgx.ErrNoRows becomes
// ErrNotFound.
func scanSession(row pgx.Row) (Session, error) {
	var sess Session
	err := row.Scan(
		&sess.ID, &sess.UserID, &sess.FamilyID,
		&sess.AccessExpiresAt, &sess.RefreshExpiresAt, &sess.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}
