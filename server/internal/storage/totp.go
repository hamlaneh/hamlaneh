package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Two-step verification storage: the user_totp, user_recovery_codes and
// totp_challenges tables from migration 0004.
//
// Everything that races — an attempt counter, a single-use consumption, the
// activation switch — runs inside a transaction that locks the row it is
// about to change with SELECT ... FOR UPDATE, exactly as RotateSession does.
// Rows are always locked in the order totp_challenges → user_totp →
// user_recovery_codes, so two of these operations can never deadlock.
//
// Secrets never leave this package as anything but bytes, and no code, secret
// or token appears in an error message here.

// Sentinel errors for the states the handlers turn into 409s.
var (
	// ErrTotpAlreadyEnabled reports a setup attempt on an account whose
	// second factor is already on.
	ErrTotpAlreadyEnabled = errors.New("storage: two-step verification already enabled")
	// ErrTotpNotEnabled reports an action that needs a live second factor on
	// an account that has none.
	ErrTotpNotEnabled = errors.New("storage: two-step verification not enabled")

	// ErrTotpRequiredByOrg reports an attempt to switch off a second factor
	// the organisation requires.
	ErrTotpRequiredByOrg = errors.New("storage: two-step verification is required by the organisation")
	// ErrTotpSetupNotVerified reports an activation with nothing to
	// activate: no pending setup, one that never verified, or an expired one.
	ErrTotpSetupNotVerified = errors.New("storage: two-step setup not verified")
)

// Totp is a row of user_totp: a pending setup until ActivatedAt is set, the
// account's live second factor after.
type Totp struct {
	UserID         uuid.UUID
	Secret         []byte
	VerifiedAt     *time.Time
	ActivatedAt    *time.Time
	SetupExpiresAt time.Time
	VerifyAttempts int
	LastUsedStep   *int64
	CreatedAt      time.Time
}

// Enabled reports whether two-step verification is on for the account.
func (t Totp) Enabled() bool { return t.ActivatedAt != nil }

// totpColumns is the canonical column list, in the order scanTotp expects.
const totpColumns = `user_id, secret, verified_at, activated_at, setup_expires_at,
	verify_attempts, last_used_step, created_at`

// TotpByUser returns the user's two-step row, or ErrNotFound when the
// account has neither a pending setup nor a live second factor.
func (s *Store) TotpByUser(ctx context.Context, userID uuid.UUID) (Totp, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+totpColumns+` FROM user_totp WHERE user_id = $1`, userID)
	t, err := scanTotp(row)
	if err != nil {
		return Totp{}, fmt.Errorf("totp by user: %w", err)
	}
	return t, nil
}

// RecoveryCodeCounts returns how many of the user's current recovery codes
// are unused and how many the set holds.
func (s *Store) RecoveryCodeCounts(ctx context.Context, userID uuid.UUID) (remaining, total int, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE used_at IS NULL), count(*)
		 FROM user_recovery_codes WHERE user_id = $1`,
		userID,
	).Scan(&remaining, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return remaining, total, nil
}

// StartTotpSetup installs a fresh pending setup, replacing any previous one
// and the recovery codes an abandoned setup may have issued. An account whose
// second factor is already on is refused with ErrTotpAlreadyEnabled: turning
// it off is a separate, password-confirmed action.
func (s *Store) StartTotpSetup(ctx context.Context, userID uuid.UUID, secret []byte, ttl time.Duration) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var enabled bool
		err := tx.QueryRow(ctx,
			`SELECT activated_at IS NOT NULL FROM user_totp WHERE user_id = $1 FOR UPDATE`,
			userID,
		).Scan(&enabled)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No row yet: the first setup for this account.
		case err != nil:
			return fmt.Errorf("lock existing setup: %w", err)
		case enabled:
			return ErrTotpAlreadyEnabled
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO user_totp (user_id, secret, setup_expires_at)
			 VALUES ($1, $2, now() + make_interval(secs => $3))
			 ON CONFLICT (user_id) DO UPDATE
			     SET secret           = EXCLUDED.secret,
			         verified_at      = NULL,
			         activated_at     = NULL,
			         setup_expires_at = EXCLUDED.setup_expires_at,
			         verify_attempts  = 0,
			         last_used_step   = NULL,
			         created_at       = now()`,
			userID, secret, ttl.Seconds(),
		); err != nil {
			return fmt.Errorf("install pending setup: %w", err)
		}

		return deleteRecoveryCodes(ctx, tx, userID)
	})
	if err != nil {
		return fmt.Errorf("start totp setup: %w", err)
	}
	return nil
}

// TotpVerifyOutcome reports how VerifyTotpSetup classified an attempt.
type TotpVerifyOutcome int

const (
	// TotpVerifyNoSetup means there is nothing to verify: no pending setup,
	// it expired, or a previous cap revoked it.
	TotpVerifyNoSetup TotpVerifyOutcome = iota
	// TotpVerifyRejected means the code was wrong and the setup survives.
	TotpVerifyRejected
	// TotpVerifyRevoked means the code was wrong and it was the attempt that
	// hit the cap: the pending setup is gone.
	TotpVerifyRevoked
	// TotpVerifyAccepted means the setup is verified and its recovery codes
	// are stored.
	TotpVerifyAccepted
)

// String names the outcome for logs, so an unhandled value reaches slog as
// something a reader can act on rather than a bare integer.
func (o TotpVerifyOutcome) String() string {
	switch o {
	case TotpVerifyNoSetup:
		return "no-setup"
	case TotpVerifyRejected:
		return "rejected"
	case TotpVerifyRevoked:
		return "revoked"
	case TotpVerifyAccepted:
		return "accepted"
	default:
		return "invalid"
	}
}

// TotpSetupVerification is one attempt at setup step 2.
//
// Both callbacks keep the plaintext in the caller's closure, so no code and
// no recovery code ever reaches this package, a query, or a log line.
type TotpSetupVerification struct {
	UserID uuid.UUID
	// MaxAttempts is how many wrong codes revoke the pending setup.
	MaxAttempts int
	// CheckCode verifies the presented code against the pending secret and
	// the highest step already accepted, returning the step it accepted.
	CheckCode func(secret []byte, lastUsedStep *int64) (step int64, ok bool)
	// RecoveryCodeHashes is called only after CheckCode accepted; its
	// argon2id hashes replace the account's recovery codes inside the same
	// transaction, so a verified setup always has codes and an unverified one
	// never does.
	RecoveryCodeHashes func() []string
}

// VerifyTotpSetup is setup step 2: it checks the first authenticator code
// against the pending secret and, on success, records the verification and
// installs the recovery codes.
//
// Two-step verification stays OFF either way — activation is a separate
// call. A wrong code costs one attempt and leaves the secret alone; the
// attempt that reaches MaxAttempts deletes the pending setup, because an
// uncapped verifier is a brute-force oracle.
func (s *Store) VerifyTotpSetup(ctx context.Context, v TotpSetupVerification) (TotpVerifyOutcome, error) {
	outcome := TotpVerifyNoSetup

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			secret       []byte
			enabled      bool
			expired      bool
			attempts     int
			lastUsedStep *int64
		)
		err := tx.QueryRow(ctx,
			`SELECT secret, activated_at IS NOT NULL, setup_expires_at <= now(),
			        verify_attempts, last_used_step
			 FROM user_totp WHERE user_id = $1
			 FOR UPDATE`,
			v.UserID,
		).Scan(&secret, &enabled, &expired, &attempts, &lastUsedStep)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock pending setup: %w", err)
		}

		// An enabled account has no pending setup; an expired one is dead and
		// its secret has no further use.
		if enabled || attempts >= v.MaxAttempts {
			return nil
		}
		if expired {
			return deletePendingSetup(ctx, tx, v.UserID)
		}

		step, accepted := v.CheckCode(secret, lastUsedStep)
		if !accepted {
			revoked, err := burnSetupAttempt(ctx, tx, v.UserID, v.MaxAttempts)
			if err != nil {
				return err
			}
			outcome = TotpVerifyRejected
			if revoked {
				outcome = TotpVerifyRevoked
			}
			return nil
		}

		if _, err := tx.Exec(ctx,
			`UPDATE user_totp
			    SET verified_at     = COALESCE(verified_at, now()),
			        last_used_step  = $2,
			        verify_attempts = 0
			  WHERE user_id = $1`,
			v.UserID, step,
		); err != nil {
			return fmt.Errorf("record verification: %w", err)
		}
		if err := replaceRecoveryCodes(ctx, tx, v.UserID, v.RecoveryCodeHashes()); err != nil {
			return err
		}
		outcome = TotpVerifyAccepted
		return nil
	})
	if err != nil {
		return TotpVerifyNoSetup, fmt.Errorf("verify totp setup: %w", err)
	}
	return outcome, nil
}

// ActivateTotp is setup step 3: it turns two-step verification on and
// returns the moment it went on.
//
// The conditions are the guarantee that no code path enables a second factor
// the account cannot use: the setup must exist, be unexpired, have verified
// an authenticator code, and hold at least one unused recovery code — the
// fallback the user was shown at step 2.
//
// Activation also clears totp_enrollment_required on EVERY session of the
// user, in the same transaction: the account now satisfies the org policy,
// and a person who signed in on two devices under it must find both
// unblocked, not just the one that ran the setup (ADR 004). The advisory
// lock serializes this against concurrent session mints, so no login can
// slip a freshly flagged session past the sweep.
func (s *Store) ActivateTotp(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	var activatedAt time.Time
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if lockErr := lockSessionMint(ctx, tx, userID); lockErr != nil {
			return lockErr
		}

		err := tx.QueryRow(ctx,
			`UPDATE user_totp SET activated_at = now()
			  WHERE user_id = $1
			    AND activated_at IS NULL
			    AND verified_at IS NOT NULL
			    AND setup_expires_at > now()
			    AND EXISTS (SELECT 1 FROM user_recovery_codes
			                 WHERE user_id = $1 AND used_at IS NULL)
			 RETURNING activated_at`,
			userID,
		).Scan(&activatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTotpSetupNotVerified
		}
		if err != nil {
			return fmt.Errorf("set activated: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE sessions SET totp_enrollment_required = false
			  WHERE user_id = $1 AND totp_enrollment_required`,
			userID,
		); err != nil {
			return fmt.Errorf("clear enrollment flags: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrTotpSetupNotVerified) {
		return time.Time{}, ErrTotpSetupNotVerified
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("activate totp: %w", err)
	}
	return activatedAt, nil
}

// DisableTotp turns two-step verification off: the secret goes, every
// recovery code goes, and any half-finished sign-in challenge goes with them.
//
// Sessions are deliberately left alone (see the contract): revoking families
// would punish the legitimate user's devices while a hijacker's own session
// survived. The password prompt in front of this call is the defence.
func (s *Store) DisableTotp(ctx context.Context, userID uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The organisation's policy outranks the account's own preference,
		// and it is read inside this transaction so an administrator turning
		// it on cannot be raced by a disable that read the old value.
		//
		// Without this the enforcement built in 1.6 binds on everybody except
		// the people it is aimed at: enrol, sign in without the flag, switch
		// the second factor back off, and keep refreshing. The gate only ever
		// looks at sessions minted since, so that account never sees it again.
		var required bool
		if err := tx.QueryRow(ctx, `SELECT require_totp FROM org_settings`).Scan(&required); err != nil {
			return fmt.Errorf("read org policy: %w", err)
		}
		if required {
			return ErrTotpRequiredByOrg
		}

		if _, err := tx.Exec(ctx, `DELETE FROM totp_challenges WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("drop challenges: %w", err)
		}

		tag, err := tx.Exec(ctx,
			`DELETE FROM user_totp WHERE user_id = $1 AND activated_at IS NOT NULL`,
			userID,
		)
		if err != nil {
			return fmt.Errorf("drop secret: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrTotpNotEnabled
		}
		return deleteRecoveryCodes(ctx, tx, userID)
	})
	if err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	return nil
}

// ReplaceRecoveryCodes swaps the whole set for a fresh one. Every previous
// code — used or not — is gone when it returns. It refuses an account
// without a live second factor: recovery codes are sign-in credentials, and
// minting them for a password-only account would be minting a second way in.
//
// hashes is a callback for the same reason TotpSetupVerification's is: it
// is called only after the check passed, inside the transaction, so a
// refused regeneration costs no argon2id work at all. Ten hashes at 64 MiB
// each is nearly a second of CPU and 640 MiB of memory traffic, and an
// endpoint that spends it before deciding whether it will do anything is a
// lever an attacker with one session can pull repeatedly. Moving the check
// out to the caller instead would reintroduce the TOCTOU this transaction
// exists to close.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes func() []string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var enabled bool
		err := tx.QueryRow(ctx,
			`SELECT activated_at IS NOT NULL FROM user_totp WHERE user_id = $1 FOR UPDATE`,
			userID,
		).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTotpNotEnabled
		}
		if err != nil {
			return fmt.Errorf("lock secret: %w", err)
		}
		if !enabled {
			return ErrTotpNotEnabled
		}
		return replaceRecoveryCodes(ctx, tx, userID, hashes())
	})
	if err != nil {
		return fmt.Errorf("replace recovery codes: %w", err)
	}
	return nil
}

// CreateTotpChallenge mints the half-authenticated state between the password
// step and the code step. One challenge lives per user: minting a new one
// replaces the previous, so a stack of parallel guessing windows cannot
// build up.
//
// The replacement is a single upsert against the UNIQUE (user_id) constraint
// (migration 0005) rather than the delete-then-insert it used to be, for the
// same reason StartTotpSetup upserts user_totp: a delete of a row that is
// not there locks nothing, so two concurrent password logins could each
// insert and leave TWO live challenges — two parallel attempt budgets for
// one account. Under the unique index the second insert blocks on the first
// and lands as the replacing update, and the one-challenge invariant is
// enforced by the schema rather than by whoever writes the next query.
func (s *Store) CreateTotpChallenge(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO totp_challenges (user_id, token_hash, expires_at)
		 VALUES ($1, $2, now() + make_interval(secs => $3))
		 ON CONFLICT (user_id) DO UPDATE
		     SET token_hash  = EXCLUDED.token_hash,
		         expires_at  = EXCLUDED.expires_at,
		         attempts    = 0,
		         consumed_at = NULL,
		         created_at  = now()`,
		userID, tokenHash, ttl.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("create totp challenge: %w", err)
	}
	return nil
}

// TotpChallengeUserByTokenHash returns the account a challenge token was
// minted for, or ErrNotFound when the token matches no challenge row. It
// deliberately matches spent and expired rows too: the caller (the two-step
// sign-in's per-account rate limiter) needs the account's identity, and
// liveness is CompleteTotpChallenge's decision, made under its row lock.
func (s *Store) TotpChallengeUserByTokenHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM totp_challenges WHERE token_hash = $1`, tokenHash,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("totp challenge user by token hash: %w", err)
	}
	return userID, nil
}

// TotpChallengeOutcome reports how CompleteTotpChallenge classified an
// attempt.
type TotpChallengeOutcome int

const (
	// TotpChallengeNone means no live challenge answered to the token:
	// missing, expired, already consumed, capped, or belonging to an account
	// whose second factor was turned off meanwhile.
	TotpChallengeNone TotpChallengeOutcome = iota
	// TotpChallengeRejected means the code was wrong and the challenge
	// survives for another try.
	TotpChallengeRejected
	// TotpChallengeRevoked means the code was wrong and it was the attempt
	// that hit the cap: the challenge is consumed and sign-in starts over.
	TotpChallengeRevoked
	// TotpChallengeCompleted means the code was right: the challenge is
	// consumed and a session family exists.
	TotpChallengeCompleted
)

// String names the outcome for logs, so an unhandled value reaches slog as
// something a reader can act on rather than a bare integer.
func (o TotpChallengeOutcome) String() string {
	switch o {
	case TotpChallengeNone:
		return "none"
	case TotpChallengeRejected:
		return "rejected"
	case TotpChallengeRevoked:
		return "revoked"
	case TotpChallengeCompleted:
		return "completed"
	default:
		return "invalid"
	}
}

// TotpChallengeAttempt is one attempt at completing a two-step sign-in.
//
// At most one of the two callbacks is set — a six-digit code is checked
// against the secret, anything else against the unused recovery codes.
// Neither being set means the presented code has no valid shape; it is still
// an attempt, so probing costs the same as guessing. Both keep the plaintext
// in the caller's closure.
type TotpChallengeAttempt struct {
	// TokenHash is the SHA-256 of the challenge cookie's value.
	TokenHash []byte
	// MaxAttempts is how many wrong codes revoke the challenge.
	MaxAttempts int
	// CheckCode verifies a six-digit code against the account's secret and
	// the highest step already accepted, returning the step it accepted.
	CheckCode func(secret []byte, lastUsedStep *int64) (step int64, ok bool)
	// MatchRecoveryCode reports whether the presented code matches one
	// stored argon2id hash.
	MatchRecoveryCode func(storedHash string) bool
	// Session carries the token hashes and metadata for the session family
	// created when the attempt succeeds.
	Session SessionTokens
}

// CompleteTotpChallenge is the second half of a two-step sign-in, in one
// transaction: the challenge, the replay guard, the recovery-code
// consumption and the session that results either all happen or none do.
//
// That atomicity is the point of the single transaction — a recovery code
// must not be spent by a sign-in that then fails to produce a session, and a
// session must not exist for a code that was never consumed.
func (s *Store) CompleteTotpChallenge(ctx context.Context, att TotpChallengeAttempt) (User, Session, TotpChallengeOutcome, error) {
	var (
		user    User
		sess    Session
		outcome = TotpChallengeNone
	)

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var challengeID, userID uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT id, user_id FROM totp_challenges
			  WHERE token_hash = $1
			    AND consumed_at IS NULL
			    AND expires_at > now()
			    AND attempts < $2
			 FOR UPDATE`,
			att.TokenHash, att.MaxAttempts,
		).Scan(&challengeID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock challenge: %w", err)
		}

		var secret []byte
		var lastUsedStep *int64
		err = tx.QueryRow(ctx,
			`SELECT secret, last_used_step FROM user_totp
			  WHERE user_id = $1 AND activated_at IS NOT NULL
			 FOR UPDATE`,
			userID,
		).Scan(&secret, &lastUsedStep)
		if errors.Is(err, pgx.ErrNoRows) {
			// The second factor was turned off while the challenge was open;
			// the challenge has nothing left to verify against.
			return consumeChallenge(ctx, tx, challengeID)
		}
		if err != nil {
			return fmt.Errorf("lock secret: %w", err)
		}

		accepted, err := acceptChallengeCode(ctx, tx, att, userID, secret, lastUsedStep)
		if err != nil {
			return err
		}
		if !accepted {
			revoked, burnErr := burnChallengeAttempt(ctx, tx, challengeID, att.MaxAttempts)
			if burnErr != nil {
				return burnErr
			}
			outcome = TotpChallengeRejected
			if revoked {
				outcome = TotpChallengeRevoked
			}
			return nil
		}

		if consumeErr := consumeChallenge(ctx, tx, challengeID); consumeErr != nil {
			return consumeErr
		}
		if sess, err = insertSessionFamily(ctx, tx, userID, att.Session); err != nil {
			return err
		}
		if user, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, userID)); err != nil {
			return fmt.Errorf("load authenticated user: %w", err)
		}
		outcome = TotpChallengeCompleted
		return nil
	})
	if err != nil {
		return User{}, Session{}, TotpChallengeNone, fmt.Errorf("complete totp challenge: %w", err)
	}
	return user, sess, outcome, nil
}

// acceptChallengeCode tries the presented credential against the account's
// second factor and records what a success consumes: the time step for an
// authenticator code, the code row for a recovery code.
func acceptChallengeCode(
	ctx context.Context,
	tx pgx.Tx,
	att TotpChallengeAttempt,
	userID uuid.UUID,
	secret []byte,
	lastUsedStep *int64,
) (bool, error) {
	if att.CheckCode != nil {
		step, ok := att.CheckCode(secret, lastUsedStep)
		if !ok {
			return false, nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE user_totp SET last_used_step = $2 WHERE user_id = $1`,
			userID, step,
		); err != nil {
			return false, fmt.Errorf("record used step: %w", err)
		}
		return true, nil
	}

	if att.MatchRecoveryCode != nil {
		return consumeRecoveryCode(ctx, tx, userID, att.MatchRecoveryCode)
	}
	return false, nil
}

// consumeRecoveryCode walks the account's unused codes and spends the one
// that matches. The rows are read to completion before the update, because a
// pgx transaction runs one query at a time on its connection.
func consumeRecoveryCode(ctx context.Context, tx pgx.Tx, userID uuid.UUID, match func(storedHash string) bool) (bool, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, code_hash FROM user_recovery_codes
		  WHERE user_id = $1 AND used_at IS NULL
		 ORDER BY created_at, id
		 FOR UPDATE`,
		userID,
	)
	if err != nil {
		return false, fmt.Errorf("lock recovery codes: %w", err)
	}

	type candidate struct {
		id   uuid.UUID
		hash string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			rows.Close()
			return false, fmt.Errorf("read recovery code: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read recovery codes: %w", err)
	}

	for _, c := range candidates {
		if !match(c.hash) {
			continue
		}
		tag, err := tx.Exec(ctx,
			`UPDATE user_recovery_codes SET used_at = now() WHERE id = $1 AND used_at IS NULL`,
			c.id,
		)
		if err != nil {
			return false, fmt.Errorf("spend recovery code: %w", err)
		}
		// The row was locked above, so exactly one update is the only
		// possible result; anything else means the invariant broke.
		if tag.RowsAffected() != 1 {
			return false, errors.New("spend recovery code: row changed under the lock")
		}
		return true, nil
	}
	return false, nil
}

// burnSetupAttempt counts one wrong setup code and reports whether it was the
// attempt that reached the cap, in which case the pending setup is deleted.
func burnSetupAttempt(ctx context.Context, tx pgx.Tx, userID uuid.UUID, maxAttempts int) (bool, error) {
	var attempts int
	if err := tx.QueryRow(ctx,
		`UPDATE user_totp SET verify_attempts = verify_attempts + 1
		  WHERE user_id = $1 RETURNING verify_attempts`,
		userID,
	).Scan(&attempts); err != nil {
		return false, fmt.Errorf("count wrong setup code: %w", err)
	}
	if attempts < maxAttempts {
		return false, nil
	}
	if err := deletePendingSetup(ctx, tx, userID); err != nil {
		return false, err
	}
	return true, nil
}

// burnChallengeAttempt counts one wrong sign-in code and reports whether it
// was the attempt that reached the cap, in which case the challenge is
// consumed and the caller starts again at the password step.
func burnChallengeAttempt(ctx context.Context, tx pgx.Tx, challengeID uuid.UUID, maxAttempts int) (bool, error) {
	var attempts int
	if err := tx.QueryRow(ctx,
		`UPDATE totp_challenges SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts`,
		challengeID,
	).Scan(&attempts); err != nil {
		return false, fmt.Errorf("count wrong sign-in code: %w", err)
	}
	if attempts < maxAttempts {
		return false, nil
	}
	if err := consumeChallenge(ctx, tx, challengeID); err != nil {
		return false, err
	}
	return true, nil
}

// consumeChallenge marks a challenge spent; nothing revives it.
func consumeChallenge(ctx context.Context, tx pgx.Tx, challengeID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`UPDATE totp_challenges SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`,
		challengeID,
	); err != nil {
		return fmt.Errorf("consume challenge: %w", err)
	}
	return nil
}

// deletePendingSetup drops a pending setup and anything it issued. It never
// touches an active second factor.
func deletePendingSetup(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_totp WHERE user_id = $1 AND activated_at IS NULL`,
		userID,
	); err != nil {
		return fmt.Errorf("drop pending setup: %w", err)
	}
	return deleteRecoveryCodes(ctx, tx, userID)
}

// replaceRecoveryCodes swaps the account's whole set for hashes.
func replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, userID uuid.UUID, hashes []string) error {
	if err := deleteRecoveryCodes(ctx, tx, userID); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return errors.New("install recovery codes: empty set")
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_recovery_codes (user_id, code_hash)
		 SELECT $1, unnest($2::text[])`,
		userID, hashes,
	); err != nil {
		return fmt.Errorf("install recovery codes: %w", err)
	}
	return nil
}

func deleteRecoveryCodes(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("drop recovery codes: %w", err)
	}
	return nil
}

// scanTotp scans one totpColumns row. pgx.ErrNoRows becomes ErrNotFound.
func scanTotp(row pgx.Row) (Totp, error) {
	var t Totp
	err := row.Scan(
		&t.UserID, &t.Secret, &t.VerifiedAt, &t.ActivatedAt, &t.SetupExpiresAt,
		&t.VerifyAttempts, &t.LastUsedStep, &t.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Totp{}, ErrNotFound
	}
	if err != nil {
		return Totp{}, err
	}
	return t, nil
}
