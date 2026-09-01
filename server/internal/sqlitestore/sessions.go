package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The session model itself is storage/sessions.go's, unchanged: one row per
// refresh-token generation, a family per device, reuse detection revoking the
// family. Nothing about that is dialect-specific and none of it is restated
// here. What differs is mechanical and is explained at each site:
//
//   - The advisory lock that serializes minting against TOTP activation, and
//     the FOR UPDATE that serializes two rotations of one token, are both
//     absent — a single writer is what they were buying.
//   - now(), gen_random_uuid() and interval arithmetic do not exist, so the
//     clock reading, the identifiers and the access deadline are computed in
//     Go and bound. The one deadline that still has to be computed inside the
//     statement is the org lifetime clamp, because it reads a column.

// sessionMintIP binds SessionTokens.IP the way migration 0001 stores inet: as
// TEXT, nil for no recorded address. netip.Addr is not a driver.Valuer, and
// its String form is exactly the text PostgreSQL's inet renders.
func sessionMintIP(addr *netip.Addr) any {
	if addr == nil {
		return nil
	}
	return addr.String()
}

// sessionMintColumns is the insert list both mint statements below use. It
// names id and created_at, which the PostgreSQL table defaults
// (gen_random_uuid(), now()) and this one does not: the SQLite migration tree
// generates no identifiers and defaults no clock readings, so the driver
// supplies both.
const sessionMintColumns = `(id, user_id, family_id, refresh_token_hash, access_token_hash,
	      refresh_expires_at, access_expires_at, user_agent, ip,
	      totp_enrollment_required, created_at)`

// sessionRefreshDeadline is the org's session_lifetime_hours clamped to the
// caller's RefreshTTL, computed inside the statement so the clamp and the
// enrolment flag come from one snapshot of org_settings.
//
// PostgreSQL writes this as
//
//	now() + LEAST(make_interval(hours => o.session_lifetime_hours),
//	              make_interval(secs => $4))
//
// SQLite has no intervals, so both candidates become absolute timestamps and
// LEAST becomes the two-argument scalar min(): the encoding codec.go defines is
// fixed width and UTC, so the lexicographically smaller of two of these strings
// IS the earlier instant. The first placeholder is the caller's deadline,
// computed in Go; the second is the bound clock reading the org's ceiling is
// measured from.
//
// One honest imprecision: strftime's %f carries milliseconds, so the org
// ceiling lands on a millisecond boundary while the caller's deadline keeps
// full microseconds. The clamp can therefore fall up to 999µs earlier than
// PostgreSQL's would — always in the shortening direction, and far below the
// resolution of anything that reads it.
const sessionRefreshDeadline = `min(?, strftime('%Y-%m-%dT%H:%M:%f000Z', ?, '+' || o.session_lifetime_hours || ' hours'))`

// insertSessionFamily creates the first generation of a fresh session family:
// a login, whichever half of the sign-in produced it.
//
// It takes a querier rather than s.db because the two-step sign-in has to run
// this insert inside the transaction that consumes the challenge — a spent
// recovery code and the session it bought are one atomic fact — while a
// password-only login has no transaction to join.
//
// The INSERT is a single statement over org_settings on purpose: the enrolment
// flag and the lifetime clamp are decided from ONE snapshot, so the mint can
// never combine a new require_totp with a stale activation fact (or vice versa)
// the way two Go-side reads could. org_settings always holds exactly one row
// (migration 0009), so the SELECT always inserts.
//
//   - totp_enrollment_required: true only when the org requires two-step
//     verification AND the account has no activated second factor. On the
//     two-step sign-in path the caller's transaction is the database's single
//     writer, so the subquery deterministically reads the activation that
//     transaction has already written.
//   - refresh_expires_at: the org's session_lifetime_hours, clamped to the
//     caller's RefreshTTL — the server's own ceiling, per the contract
//     ("clamped rather than refused").
func (s *Store) insertSessionFamily(ctx context.Context, q querier, userID uuid.UUID, t storage.SessionTokens) (storage.Session, error) {
	now := s.clock()
	nowText := asTime(now)

	row := q.QueryRowContext(ctx,
		`INSERT INTO sessions `+sessionMintColumns+`
		 SELECT ?, ?, ?, ?, ?,
		        `+sessionRefreshDeadline+`,
		        ?, ?, ?,
		        o.require_totp = 1 AND NOT EXISTS (
		            SELECT 1 FROM user_totp ut
		            WHERE ut.user_id = ? AND ut.activated_at IS NOT NULL),
		        ?
		 FROM org_settings o
		 RETURNING `+sessionColumns,
		uuid.New(), userID, uuid.New(), t.RefreshTokenHash, t.AccessTokenHash,
		asTime(now.Add(t.RefreshTTL)), nowText,
		asTime(now.Add(t.AccessTTL)), t.UserAgent, sessionMintIP(t.IP),
		userID,
		nowText,
	)
	sess, err := scanSession(row)
	if err != nil {
		return storage.Session{}, fmt.Errorf("insert session family: %w", err)
	}
	return sess, nil
}

// CreateSession starts a new session family for a login and returns the first
// generation.
//
// storage.CreateSession wraps this insert in a transaction that first takes a
// per-user advisory lock (lockSessionMint), because a login whose INSERT reads
// require_totp and the account's activation state must not interleave with an
// ActivateTotp that changes the second while sweeping the flag off existing
// sessions — the login could otherwise mint a flagged session the sweep cannot
// see, stranding a device behind a gate the account already satisfies. Here
// the two cannot interleave at all: a write transaction holds the database's
// write lock for its whole life, so the mint either sees the activation and
// its sweep, or commits entirely before either exists. The statement is alone
// in this method, so it needs no transaction of its own.
func (s *Store) CreateSession(ctx context.Context, ns storage.NewSession) (storage.Session, error) {
	sess, err := s.insertSessionFamily(ctx, s.db, ns.UserID, ns.SessionTokens)
	if err != nil {
		return storage.Session{}, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// SessionUserByAccessHash authenticates an access token: it returns the live
// session matching the hash and its user in one query. A session is live when
// it is not revoked and its access token has not expired. Returns ErrNotFound
// otherwise.
//
// This is the ONE query that returns a user alongside other columns, so it is
// also the one that cannot call scanUser or scanSession. It shares both
// projections and both scan lists all the same — memberUserColumns with
// userScan.targets, and sessionColumns' order with sessionScanTargets, see
// projections.go. The PostgreSQL driver carried its own copy of the user half
// once: the copy missed migration 0014's COALESCE, which 500'd every
// authenticated request by an account with no password, and it had quietly
// lost the two SCIM columns as well. Do not re-inline either list.
func (s *Store) SessionUserByAccessHash(ctx context.Context, accessHash []byte) (storage.Session, storage.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.user_id, s.family_id, s.access_expires_at, s.refresh_expires_at, s.created_at,
		        s.totp_enrollment_required,
		        `+memberUserColumns+`
		 FROM sessions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.access_token_hash = ?
		   AND s.revoked_at IS NULL
		   AND s.access_expires_at > ?`,
		accessHash, s.nowText(),
	)

	var sess storage.Session
	var us userScan
	err := row.Scan(append(sessionScanTargets(&sess), us.targets()...)...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return storage.Session{}, storage.User{}, storage.ErrNotFound
	case err != nil:
		return storage.Session{}, storage.User{}, fmt.Errorf("session by access hash: %w", err)
	}
	return sess, us.user(), nil
}

// RotateSession implements refresh-token rotation with reuse detection, in one
// transaction:
//
//   - unknown, revoked, or expired token → RotateOutcomeInvalid
//   - already-used token → the whole family is revoked (reuse means the token
//     leaked and was replayed) → RotateOutcomeReuseDetected
//   - valid token → the presented row is marked used and its access token is
//     retired immediately (access_expires_at = the transaction's clock
//     reading), and the next generation is inserted in the same family →
//     RotateOutcomeRotated
//
// storage.RotateSession reads the presented row SELECT ... FOR UPDATE so that
// two concurrent rotations of one token serialize: the first wins, the second
// waits on the lock and then re-reads a used token, which is what makes the
// second trip reuse detection rather than mint a second live generation from
// the same token. Here there is nothing to serialize — the second rotation
// cannot begin until the first has committed, because a write transaction
// holds the database's write lock from BEGIN — so it reads used_at already set
// and reaches the same branch. The lock clause is absent; the outcome is not.
//
// The used check deliberately precedes the expiry check: replaying a used
// token is a theft signal regardless of expiry, and revoking the family is the
// safe response.
func (s *Store) RotateSession(ctx context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error) {
	var (
		nextSess storage.Session
		outcome  storage.RotateOutcome
	)

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// One clock reading for the whole transaction, which is what
		// PostgreSQL's now() is: the retirement of the old generation and the
		// birth of the new one must record the same instant.
		now := s.clock()
		nowText := asTime(now)

		var (
			id, userID, familyID   uuid.UUID
			revoked, used, expired bool
			enrollmentRequired     bool
		)
		err := tx.QueryRowContext(ctx,
			`SELECT id, user_id, family_id,
			        revoked_at IS NOT NULL,
			        used_at IS NOT NULL,
			        refresh_expires_at <= ?,
			        totp_enrollment_required
			 FROM sessions WHERE refresh_token_hash = ?`,
			nowText, refreshHash,
		).Scan(&id, &userID, &familyID, &revoked, &used, &expired, &enrollmentRequired)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = storage.RotateOutcomeInvalid
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up refresh token: %w", err)
		}

		switch {
		case revoked:
			outcome = storage.RotateOutcomeInvalid
			return nil
		case used:
			if _, execErr := tx.ExecContext(ctx,
				`UPDATE sessions SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`,
				nowText, familyID,
			); execErr != nil {
				return fmt.Errorf("revoke family on reuse: %w", execErr)
			}
			slog.Warn("refresh token reuse detected; session family revoked",
				"user_id", userID, "family_id", familyID)
			outcome = storage.RotateOutcomeReuseDetected
			return nil
		case expired:
			outcome = storage.RotateOutcomeInvalid
			return nil
		}

		if _, execErr := tx.ExecContext(ctx,
			`UPDATE sessions SET used_at = ?, access_expires_at = ? WHERE id = ?`,
			nowText, nowText, id,
		); execErr != nil {
			return fmt.Errorf("retire rotated session: %w", execErr)
		}

		// The next generation carries the retired one's enrolment flag: a
		// rotation is not a sign-in, so it must neither launder the flag away
		// nor pick up a policy flipped mid-session. The one adjustment is
		// downward-only — AND NOT activated — so a flag whose account has
		// since enrolled clears at the next refresh even if it was minted in
		// the microsecond window ActivateTotp's sweep cannot see. It can never
		// newly flag a session, which is what "never mid-session" forbids.
		//
		// refresh_expires_at re-reads the org lifetime (clamped to the
		// caller's ceiling): each rotation starts a fresh refresh window, and
		// a window minted after the admin shortened the lifetime must be the
		// shortened one — reusing the sign-in-time value would let an active
		// session keep year-long windows forever, which is the "believed but
		// unenforced" bug this slice exists to close. Already-issued windows
		// are untouched, so nothing is retroactively ended.
		row := tx.QueryRowContext(ctx,
			`INSERT INTO sessions `+sessionMintColumns+`
			 SELECT ?, ?, ?, ?, ?,
			        `+sessionRefreshDeadline+`,
			        ?, ?, ?,
			        ? = 1 AND NOT EXISTS (
			            SELECT 1 FROM user_totp ut
			            WHERE ut.user_id = ? AND ut.activated_at IS NOT NULL),
			        ?
			 FROM org_settings o
			 RETURNING `+sessionColumns,
			uuid.New(), userID, familyID, next.RefreshTokenHash, next.AccessTokenHash,
			asTime(now.Add(next.RefreshTTL)), nowText,
			asTime(now.Add(next.AccessTTL)), next.UserAgent, sessionMintIP(next.IP),
			boolValue(enrollmentRequired), userID,
			nowText,
		)
		nextSess, err = scanSession(row)
		if err != nil {
			return fmt.Errorf("insert next generation: %w", err)
		}
		outcome = storage.RotateOutcomeRotated
		return nil
	})
	if err != nil {
		return storage.Session{}, storage.RotateOutcomeInvalid, fmt.Errorf("rotate session: %w", err)
	}
	return nextSess, outcome, nil
}

// RevokeFamily revokes every not-yet-revoked row of a session family (logout,
// remote revocation).
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`,
		s.nowText(), familyID,
	)
	if err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}
