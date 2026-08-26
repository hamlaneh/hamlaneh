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

	// TotpEnrollmentRequired says this session was minted while the org
	// required two-step verification and the account had none activated
	// (ADR 004). It is decided by the mint INSERT itself — never computed
	// live from org settings afterwards, which is what keeps "at the next
	// sign-in, never mid-session" true — carried through rotations, and
	// cleared on every session of the user by ActivateTotp.
	TotpEnrollmentRequired bool
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
const sessionColumns = `id, user_id, family_id, access_expires_at, refresh_expires_at, created_at, totp_enrollment_required`

// totpEnrollLockClass namespaces the per-user advisory lock that serializes
// session minting against TOTP activation. The mint's INSERT reads two facts
// in one snapshot — org_settings.require_totp and whether the account has an
// activated second factor — and ActivateTotp changes the second while also
// clearing the flag on every existing session. Without the lock a login
// committing concurrently with an activation could mint a flagged session
// the activation's sweep cannot see, stranding a device behind the gate for
// a policy the account already satisfies.
//
// Two-argument advisory locks live in a different keyspace than the
// one-argument locks the audit chain and the admin set take, so this class
// cannot collide with them. Lock order (package rule): advisory lock first,
// rows after.
const totpEnrollLockClass = 0x746f7470 // "totp"

// lockSessionMint takes the per-user mint/activation advisory lock,
// released at commit or rollback. CreateSession and ActivateTotp both take
// it first; CompleteTotpChallenge deliberately does not — its transaction
// already holds the account's user_totp row FOR UPDATE, which pins the
// activation fact harder than the advisory lock could, and taking the lock
// after a row lock would order the two inversely to ActivateTotp and invite
// a deadlock.
func lockSessionMint(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2::text))`,
		int32(totpEnrollLockClass), userID.String()); err != nil {
		return fmt.Errorf("lock session mint: %w", err)
	}
	return nil
}

// rowQuerier is the single method a one-row statement needs, satisfied by
// both *pgxpool.Pool and pgx.Tx. It exists so a query can be written once
// and run either on its own connection or inside a caller's transaction —
// see insertSessionFamily, whose two callers differ in exactly that.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// insertSessionFamily creates the first generation of a fresh session
// family: a login, whichever half of the sign-in produced it.
//
// It takes a rowQuerier rather than the pool because the two-step sign-in
// has to run this insert inside the transaction that consumes the challenge
// — a spent recovery code and the session it bought are one atomic fact —
// while a password-only login has no transaction to join.
//
// The INSERT is a single statement over org_settings on purpose: the
// enrolment flag and the lifetime clamp are decided from ONE snapshot, so
// the mint can never combine a new require_totp with a stale activation
// fact (or vice versa) the way two Go-side reads could. org_settings always
// holds exactly one row (migration 0009), so the SELECT always inserts.
//
//   - totp_enrollment_required: true only when the org requires two-step
//     verification AND the account has no activated second factor. On the
//     two-step sign-in path the caller's transaction holds the activated
//     user_totp row FOR UPDATE, so the subquery deterministically reads
//     false there.
//   - refresh_expires_at: the org's session_lifetime_hours, clamped to the
//     caller's RefreshTTL — the server's own ceiling, per the contract
//     ("clamped rather than refused").
func insertSessionFamily(ctx context.Context, q rowQuerier, userID uuid.UUID, t SessionTokens) (Session, error) {
	row := q.QueryRow(ctx,
		`INSERT INTO sessions
		     (user_id, family_id, refresh_token_hash, access_token_hash,
		      refresh_expires_at, access_expires_at, user_agent, ip,
		      totp_enrollment_required)
		 SELECT $1, gen_random_uuid(), $2, $3,
		        now() + LEAST(make_interval(hours => o.session_lifetime_hours),
		                      make_interval(secs => $4)),
		        now() + make_interval(secs => $5), $6, $7,
		        o.require_totp AND NOT EXISTS (
		            SELECT 1 FROM user_totp ut
		            WHERE ut.user_id = $1 AND ut.activated_at IS NOT NULL)
		 FROM org_settings o
		 RETURNING `+sessionColumns,
		userID, t.RefreshTokenHash, t.AccessTokenHash,
		t.RefreshTTL.Seconds(), t.AccessTTL.Seconds(), t.UserAgent, t.IP,
	)
	sess, err := scanSession(row)
	if err != nil {
		return Session{}, fmt.Errorf("insert session family: %w", err)
	}
	return sess, nil
}

// CreateSession starts a new session family for a login and returns the
// first generation. The transaction exists for the advisory lock: a login
// must not interleave with an activation (see lockSessionMint).
func (s *Store) CreateSession(ctx context.Context, ns NewSession) (Session, error) {
	var sess Session
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if lockErr := lockSessionMint(ctx, tx, ns.UserID); lockErr != nil {
			return lockErr
		}
		var insertErr error
		sess, insertErr = insertSessionFamily(ctx, tx, ns.UserID, ns.SessionTokens)
		return insertErr
	})
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
		        s.totp_enrollment_required,
		        u.id, u.username, u.email, u.display_name, u.password_hash,
		        u.locale, u.is_admin, u.is_active, u.must_change_password, u.created_at, u.updated_at
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
		&sess.TotpEnrollmentRequired,
		&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.PasswordHash,
		&u.Locale, &u.IsAdmin, &u.IsActive, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt,
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
			enrollmentRequired     bool
		)
		err := tx.QueryRow(ctx,
			`SELECT id, user_id, family_id,
			        revoked_at IS NOT NULL,
			        used_at IS NOT NULL,
			        refresh_expires_at <= now(),
			        totp_enrollment_required
			 FROM sessions WHERE refresh_token_hash = $1
			 FOR UPDATE`,
			refreshHash,
		).Scan(&id, &userID, &familyID, &revoked, &used, &expired, &enrollmentRequired)
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

		// The next generation carries the retired one's enrolment flag: a
		// rotation is not a sign-in, so it must neither launder the flag away
		// nor pick up a policy flipped mid-session. The one adjustment is
		// downward-only — AND NOT activated — so a flag whose account has
		// since enrolled clears at the next refresh even if it was minted in
		// the microsecond window ActivateTotp's sweep cannot see. It can
		// never newly flag a session, which is what "never mid-session"
		// forbids.
		//
		// refresh_expires_at re-reads the org lifetime (clamped to the
		// caller's ceiling): each rotation starts a fresh refresh window, and
		// a window minted after the admin shortened the lifetime must be the
		// shortened one — reusing the sign-in-time value would let an active
		// session keep year-long windows forever, which is the "believed but
		// unenforced" bug this slice exists to close. Already-issued windows
		// are untouched, so nothing is retroactively ended.
		row := tx.QueryRow(ctx,
			`INSERT INTO sessions
			     (user_id, family_id, refresh_token_hash, access_token_hash,
			      refresh_expires_at, access_expires_at, user_agent, ip,
			      totp_enrollment_required)
			 SELECT $1, $2, $3, $4,
			        now() + LEAST(make_interval(hours => o.session_lifetime_hours),
			                      make_interval(secs => $5)),
			        now() + make_interval(secs => $6), $7, $8,
			        $9 AND NOT EXISTS (
			            SELECT 1 FROM user_totp ut
			            WHERE ut.user_id = $1 AND ut.activated_at IS NOT NULL)
			 FROM org_settings o
			 RETURNING `+sessionColumns,
			userID, familyID, next.RefreshTokenHash, next.AccessTokenHash,
			next.RefreshTTL.Seconds(), next.AccessTTL.Seconds(), next.UserAgent, next.IP,
			enrollmentRequired,
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
		&sess.TotpEnrollmentRequired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}
