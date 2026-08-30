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

// The two promises this file keeps are storage/passwordreset.go's and are not
// restated per method: at most one live reset link per account, and no session
// surviving a completed reset. Both are statements about an account AS A WHOLE
// — a sweep is only correct if nothing can be added behind it — which is why
// the PostgreSQL driver takes the account row FOR UPDATE first (lockAccount)
// in both methods below. It has to: under READ COMMITTED a sweep cannot see a
// concurrent transaction's uncommitted INSERT, so without excluding the other
// writer from the start, two simultaneous requests each supersede what they
// can see and each insert, leaving two live links.
//
// Here the database's write lock is that exclusion, taken at BEGIN by every
// transaction this package opens. Nothing can insert behind a sweep because
// nothing else is writing at all. So lockAccount has no counterpart, and each
// method says at its own site what it is standing in for.

// CreatePasswordResetToken issues one reset token, in a transaction that first
// consumes the account's outstanding ones. At most one link per account is
// ever live, so a request from the real owner immediately kills a link an
// attacker triggered a moment earlier.
//
// The invariant rests on the two statements being alone: the UPDATE can only
// supersede tokens it can SEE. storage.CreatePasswordResetToken buys that with
// lockAccount; this transaction holds the database's write lock for its whole
// life, so a second request cannot even begin until this one has committed the
// token it will then supersede.
//
// tokenHash is the SHA-256 digest of the emailed token; the raw value never
// reaches the database.
func (s *Store) CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	now := s.clock()
	nowText := asTime(now)

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE password_reset_tokens SET used_at = ?
			 WHERE user_id = ? AND used_at IS NULL`,
			nowText, userID,
		); err != nil {
			return fmt.Errorf("supersede outstanding tokens: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			uuid.New(), userID, tokenHash, asTime(now.Add(ttl)), nowText,
		); err != nil {
			if isForeignKeyViolation(err) {
				// storage.CreatePasswordResetToken reports a vanished account
				// as ErrNotFound, because lockAccount looks the row up before
				// writing anything. There is no such read here, so the missing
				// account surfaces as the foreign key refusing the insert;
				// callers still branch on the one sentinel.
				return fmt.Errorf("insert token: %w", storage.ErrNotFound)
			}
			return fmt.Errorf("insert token: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	return nil
}

// ConsumePasswordReset completes a reset in one transaction: it consumes the
// presented token and, only if that token was live, stores the new password
// hash, clears must_change_password, revokes every session family of the
// account, consumes the account's other outstanding reset tokens, and consumes
// its pending two-step challenges.
//
//   - no row for the hash          → ResetOutcomeUnknown
//   - the row was already used     → ResetOutcomeUsed
//   - the row is past its expiry   → ResetOutcomeExpired
//   - otherwise                    → ResetOutcomeApplied, with the user id
//
// The three failures are distinct only in this return value; the HTTP layer
// answers identically for all of them so a replayed link learns nothing.
//
// storage.ConsumePasswordReset reads the token row TWICE: once unlocked to
// learn which account it belongs to, then again FOR UPDATE under that
// account's lock. The detour is not about correctness of the read — user_id
// never changes on a reset token — it is purely deadlock avoidance. The
// account has to be locked before the token row (package lock order, because
// CreatePasswordResetToken holds the account and wants the tokens), yet the
// token hash is the only thing naming the account, so the owner must be
// resolved before the lock it dictates can be taken.
//
// Neither lock exists here and neither can deadlock, so the dance collapses to
// one read inside the transaction. It decides liveness as well as ownership:
// the write lock this transaction already holds means no concurrent writer can
// consume the token between reading it and acting on it, which is exactly what
// the FOR UPDATE re-read was there to guarantee. Two racing uses of one link
// still produce one winner — the second transaction starts after the first has
// committed and reads a consumed token.
//
// The vanished-account case the PostgreSQL driver handles between its two
// reads cannot arise: password_reset_tokens cascades from users, so a token
// row proves the account exists, and nothing can delete it mid-transaction.
//
// Unlike UpdatePassword, no session family survives. A reset means the old
// password may be in someone else's hands, so every device signs in again.
// Two-step verification is deliberately left enabled: the token proves control
// of the mailbox, never of the authenticator.
func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string) (uuid.UUID, storage.ResetOutcome, error) {
	var (
		userID  uuid.UUID
		outcome storage.ResetOutcome
	)

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// One clock reading for the whole transaction, which is what
		// PostgreSQL's now() is: the expiry test and every consumption below
		// must judge and record the same instant.
		nowText := s.nowText()

		var (
			owner         uuid.UUID
			used, expired bool
		)
		err := tx.QueryRowContext(ctx,
			`SELECT user_id, used_at IS NOT NULL, expires_at <= ?
			 FROM password_reset_tokens WHERE token_hash = ?`,
			nowText, tokenHash,
		).Scan(&owner, &used, &expired)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = storage.ResetOutcomeUnknown
			return nil
		}
		if err != nil {
			return fmt.Errorf("look up reset token: %w", err)
		}

		switch {
		case used:
			outcome = storage.ResetOutcomeUsed
			return nil
		case expired:
			outcome = storage.ResetOutcomeExpired
			return nil
		}

		// One statement consumes the presented token and every other
		// outstanding token of the account: a completed reset must not leave a
		// second live link behind.
		if _, consumeErr := tx.ExecContext(ctx,
			`UPDATE password_reset_tokens SET used_at = ?
			 WHERE user_id = ? AND used_at IS NULL`,
			nowText, owner,
		); consumeErr != nil {
			return fmt.Errorf("consume reset tokens: %w", consumeErr)
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, must_change_password = 0, updated_at = ?
			 WHERE id = ?`,
			passwordHash, nowText, owner,
		)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		affected, err := rowsAffected(res)
		if err != nil {
			return fmt.Errorf("store new hash: %w", err)
		}
		if affected == 0 {
			// The token row that named this account is a foreign key into it,
			// and no other writer exists; treat a vanished account as a defect
			// rather than a silent success.
			return fmt.Errorf("store new hash: %w", storage.ErrNotFound)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE totp_challenges SET consumed_at = ?
			 WHERE user_id = ? AND consumed_at IS NULL`,
			nowText, owner,
		); err != nil {
			return fmt.Errorf("consume two-step challenges: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ?
			 WHERE user_id = ? AND revoked_at IS NULL`,
			nowText, owner,
		); err != nil {
			return fmt.Errorf("revoke session families: %w", err)
		}

		userID = owner
		outcome = storage.ResetOutcomeApplied
		return nil
	})
	if err != nil {
		return uuid.Nil, storage.ResetOutcomeUnknown, fmt.Errorf("consume password reset: %w", err)
	}
	return userID, outcome, nil
}
