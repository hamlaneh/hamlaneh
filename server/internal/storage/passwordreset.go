package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResetOutcome reports how ConsumePasswordReset classified a presented
// reset token. The zero value is the unknown-token case, so a caller that
// forgets to inspect the outcome fails closed.
type ResetOutcome int

const (
	// ResetOutcomeUnknown means no row matched the presented token.
	ResetOutcomeUnknown ResetOutcome = iota
	// ResetOutcomeUsed means the token was already consumed — by its own
	// reset, by a later request superseding it, or by a completed reset
	// that swept the account's outstanding tokens.
	ResetOutcomeUsed
	// ResetOutcomeExpired means the token was never used but is past its
	// expiry.
	ResetOutcomeExpired
	// ResetOutcomeApplied means the token was valid and the reset was
	// applied in full.
	ResetOutcomeApplied
)

// String names the outcome for logs. The API deliberately answers the same
// way for the three failures; only the log tells them apart.
func (o ResetOutcome) String() string {
	switch o {
	case ResetOutcomeUnknown:
		return "unknown"
	case ResetOutcomeUsed:
		return "used"
	case ResetOutcomeExpired:
		return "expired"
	case ResetOutcomeApplied:
		return "applied"
	default:
		return "invalid"
	}
}

// UserByEmail looks a user up by email address only. The column is citext,
// so the match is case-insensitive, and users without an address never
// match. Returns ErrNotFound when no account has that address.
//
// It is deliberately narrower than UserByIdentifier: a password reset is
// requested with an address, and matching usernames as well would let the
// endpoint confirm which usernames exist.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1`,
		email,
	)
	u, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("user by email: %w", err)
	}
	return u, nil
}

// CreatePasswordResetToken issues one reset token, in a transaction that
// first consumes the account's outstanding ones. At most one link per
// account is ever live, so a request from the real owner immediately kills
// a link an attacker triggered a moment earlier.
//
// The account row is locked first (package lock order). That lock is what
// makes the one-live-link invariant true rather than merely intended: the
// UPDATE below can only supersede tokens it can SEE, and under READ
// COMMITTED it cannot see a concurrent transaction's uncommitted INSERT, so
// without the lock two simultaneous requests each supersede what they can
// see and each insert — leaving two live links, and eight for eight racers.
//
// tokenHash is the SHA-256 digest of the emailed token; the raw value never
// reaches the database.
func (s *Store) CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAccount(ctx, tx, userID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE password_reset_tokens SET used_at = now()
			 WHERE user_id = $1 AND used_at IS NULL`,
			userID,
		); err != nil {
			return fmt.Errorf("supersede outstanding tokens: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
			 VALUES ($1, $2, now() + make_interval(secs => $3))`,
			userID, tokenHash, ttl.Seconds(),
		); err != nil {
			return fmt.Errorf("insert token: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	return nil
}

// ConsumePasswordReset completes a reset in one transaction: it consumes
// the presented token and, only if that token was live, stores the new
// password hash, clears must_change_password, revokes every session family
// of the account, consumes the account's other outstanding reset tokens,
// and consumes its pending two-step challenges.
//
//   - no row for the hash          → ResetOutcomeUnknown
//   - the row was already used     → ResetOutcomeUsed
//   - the row is past its expiry   → ResetOutcomeExpired
//   - otherwise                    → ResetOutcomeApplied, with the user id
//
// The three failures are distinct only in this return value; the HTTP layer
// answers identically for all of them so a replayed link learns nothing.
//
// The account is locked first and the presented row second, both with
// SELECT ... FOR UPDATE (package lock order). Locking the account is what
// keeps the session sweep honest: a session minted by a two-step sign-in
// running beside this reset would otherwise be invisible to the sweep — an
// uncommitted INSERT is invisible under READ COMMITTED — and would outlive
// the reset that was supposed to end it. Locking the token row is what makes
// two concurrent uses of ONE link serialize: the first applies the reset,
// the second re-reads the row it waited for and sees a consumed token.
//
// The account has to be locked before the token row even though the token
// hash is what names it, so the owner is resolved with an unlocked read
// first. Taking the two locks the other way round would deadlock against
// CreatePasswordResetToken, which holds the account and wants the tokens.
//
// Unlike UpdatePassword, no session family survives. A reset means the old
// password may be in someone else's hands, so every device signs in again.
// Two-step verification is deliberately left enabled: the token proves
// control of the mailbox, never of the authenticator.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string) (uuid.UUID, ResetOutcome, error) {
	var (
		userID  uuid.UUID
		outcome ResetOutcome
	)

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Names the account, decides nothing: user_id never changes on a
		// reset token, and liveness is judged by the FOR UPDATE re-read
		// below, under the account lock.
		var owner uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM password_reset_tokens WHERE token_hash = $1`,
			tokenHash,
		).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = ResetOutcomeUnknown
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up reset token: %w", err)
		}

		if lockErr := lockAccount(ctx, tx, owner); lockErr != nil {
			if errors.Is(lockErr, ErrNotFound) {
				// The account went away between the two reads; the cascade
				// took its tokens with it, so there is nothing to consume.
				outcome = ResetOutcomeUnknown
				return nil
			}
			return lockErr
		}

		var used, expired bool
		err = tx.QueryRow(ctx,
			`SELECT used_at IS NOT NULL, expires_at <= now()
			 FROM password_reset_tokens WHERE token_hash = $1
			 FOR UPDATE`,
			tokenHash,
		).Scan(&used, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = ResetOutcomeUnknown
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up reset token: %w", err)
		}

		switch {
		case used:
			outcome = ResetOutcomeUsed
			return nil
		case expired:
			outcome = ResetOutcomeExpired
			return nil
		}

		// One statement consumes the presented token and every other
		// outstanding token of the account: a completed reset must not
		// leave a second live link behind.
		if _, consumeErr := tx.Exec(ctx,
			`UPDATE password_reset_tokens SET used_at = now()
			 WHERE user_id = $1 AND used_at IS NULL`,
			owner,
		); consumeErr != nil {
			return fmt.Errorf("consume reset tokens: %w", consumeErr)
		}

		tag, err := tx.Exec(ctx,
			`UPDATE users SET password_hash = $1, must_change_password = false, updated_at = now()
			 WHERE id = $2`,
			passwordHash, owner,
		)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// lockAccount just found the row and holds it; treat a vanished
			// account as a defect rather than a silent success.
			return fmt.Errorf("store new hash: %w", ErrNotFound)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE totp_challenges SET consumed_at = now()
			 WHERE user_id = $1 AND consumed_at IS NULL`,
			owner,
		); err != nil {
			return fmt.Errorf("consume two-step challenges: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE sessions SET revoked_at = now()
			 WHERE user_id = $1 AND revoked_at IS NULL`,
			owner,
		); err != nil {
			return fmt.Errorf("revoke session families: %w", err)
		}

		userID = owner
		outcome = ResetOutcomeApplied
		return nil
	})
	if err != nil {
		return uuid.Nil, ResetOutcomeUnknown, fmt.Errorf("consume password reset: %w", err)
	}
	return userID, outcome, nil
}
