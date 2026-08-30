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

// Two-step verification storage: the user_totp, user_recovery_codes and
// totp_challenges tables from migrations 0004 and 0005.
//
// The PostgreSQL driver locks every row it is about to change with
// SELECT ... FOR UPDATE, in the order totp_challenges -> user_totp ->
// user_recovery_codes, so two of these operations can never deadlock. There
// is no lock clause anywhere below and no lock order to keep: a write
// transaction here holds the database's write lock from BEGIN, so two of
// these operations cannot overlap at all. Each method says at its own site
// what the PostgreSQL clause was doing and why the outcome survives its
// absence.
//
// Secrets never leave this package as anything but bytes, and no code, secret
// or token appears in an error message here.

// totpColumns is the canonical column list, in the order scanTotp expects.
const totpColumns = `user_id, secret, verified_at, activated_at, setup_expires_at,
	verify_attempts, last_used_step, created_at`

// TotpByUser returns the user's two-step row, or storage.ErrNotFound when the
// account has neither a pending setup nor a live second factor.
func (s *Store) TotpByUser(ctx context.Context, userID uuid.UUID) (storage.Totp, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+totpColumns+` FROM user_totp WHERE user_id = ?`, userID)
	t, err := scanTotp(row)
	if err != nil {
		return storage.Totp{}, fmt.Errorf("totp by user: %w", err)
	}
	return t, nil
}

// RecoveryCodeCounts returns how many of the user's current recovery codes
// are unused and how many the set holds.
func (s *Store) RecoveryCodeCounts(ctx context.Context, userID uuid.UUID) (remaining, total int, err error) {
	// SQLite has supported FILTER on aggregates since 3.30, so the counting
	// expression is the PostgreSQL one unchanged.
	err = s.db.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE used_at IS NULL), count(*)
		 FROM user_recovery_codes WHERE user_id = ?`,
		userID,
	).Scan(&remaining, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return remaining, total, nil
}

// StartTotpSetup installs a fresh pending setup, replacing any previous one
// and the recovery codes an abandoned setup may have issued. An account whose
// second factor is already on is refused with storage.ErrTotpAlreadyEnabled:
// turning it off is a separate, password-confirmed action.
func (s *Store) StartTotpSetup(ctx context.Context, userID uuid.UUID, secret []byte, ttl time.Duration) error {
	now := s.clock()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.StartTotpSetup reads this row FOR UPDATE because the check
		// and the upsert below are one decision: without the lock a second
		// setup could commit between them and be overwritten by a caller that
		// had already read "not enabled". Here the two calls cannot interleave
		// — this transaction holds the database's write lock from BEGIN — so
		// the check and the write see the same row by construction. Only the
		// PostgreSQL-specific shape is lost (there, a setup on one account
		// never waits on a setup on another); on SQLite everything queues,
		// briefly.
		var enabled bool
		err := tx.QueryRowContext(ctx,
			`SELECT activated_at IS NOT NULL FROM user_totp WHERE user_id = ?`,
			userID,
		).Scan(&enabled)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// No row yet: the first setup for this account.
		case err != nil:
			return fmt.Errorf("lock existing setup: %w", err)
		case enabled:
			return storage.ErrTotpAlreadyEnabled
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_totp (user_id, secret, setup_expires_at, created_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (user_id) DO UPDATE
			     SET secret           = excluded.secret,
			         verified_at      = NULL,
			         activated_at     = NULL,
			         setup_expires_at = excluded.setup_expires_at,
			         verify_attempts  = 0,
			         last_used_step   = NULL,
			         created_at       = excluded.created_at`,
			userID, secret, asTime(now.Add(ttl)), asTime(now),
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

// VerifyTotpSetup is setup step 2: it checks the first authenticator code
// against the pending secret and, on success, records the verification and
// installs the recovery codes.
//
// Two-step verification stays OFF either way — activation is a separate
// call. A wrong code costs one attempt and leaves the secret alone; the
// attempt that reaches MaxAttempts deletes the pending setup, because an
// uncapped verifier is a brute-force oracle.
func (s *Store) VerifyTotpSetup(ctx context.Context, v storage.TotpSetupVerification) (storage.TotpVerifyOutcome, error) {
	outcome := storage.TotpVerifyNoSetup

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.VerifyTotpSetup holds this row FOR UPDATE across the
		// callback, so two attempts cannot each read verify_attempts = 3 and
		// each write 4 — the cap is a budget, and a lost update hands an
		// attacker extra guesses. Here the second attempt cannot start until
		// this transaction commits, so the counter is spent exactly once per
		// attempt without any lock clause.
		//
		// The comparison against now() becomes a bound timestamp: SQLite has
		// no now(), and the fixed-width UTC text layout makes <= a correct
		// chronological test (codec.go).
		var (
			secret   []byte
			enabled  bool
			expired  bool
			attempts int
			step     sql.NullInt64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT secret, activated_at IS NOT NULL, setup_expires_at <= ?,
			        verify_attempts, last_used_step
			 FROM user_totp WHERE user_id = ?`,
			s.nowText(), v.UserID,
		).Scan(&secret, &enabled, &expired, &attempts, &step)
		if errors.Is(err, sql.ErrNoRows) {
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

		acceptedStep, accepted := v.CheckCode(secret, int64Ptr(step))
		if !accepted {
			revoked, err := burnSetupAttempt(ctx, tx, v.UserID, v.MaxAttempts)
			if err != nil {
				return err
			}
			outcome = storage.TotpVerifyRejected
			if revoked {
				outcome = storage.TotpVerifyRevoked
			}
			return nil
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE user_totp
			    SET verified_at     = COALESCE(verified_at, ?),
			        last_used_step  = ?,
			        verify_attempts = 0
			  WHERE user_id = ?`,
			s.nowText(), acceptedStep, v.UserID,
		); err != nil {
			return fmt.Errorf("record verification: %w", err)
		}
		if err := s.replaceRecoveryCodes(ctx, tx, v.UserID, v.RecoveryCodeHashes()); err != nil {
			return err
		}
		outcome = storage.TotpVerifyAccepted
		return nil
	})
	if err != nil {
		return storage.TotpVerifyNoSetup, fmt.Errorf("verify totp setup: %w", err)
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
// unblocked, not just the one that ran the setup (ADR 004).
func (s *Store) ActivateTotp(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	var activatedAt time.Time
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.ActivateTotp takes pg_advisory_xact_lock here to serialize
		// this against concurrent session mints: the mint INSERT decides
		// totp_enrollment_required from whether the account has an activated
		// factor, so a login committing alongside an activation could mint a
		// flagged session the sweep below cannot see and strand that device
		// behind a gate the account already satisfies. There is nothing to
		// serialize here — a mint is a write transaction too, so it either
		// runs entirely before this one (and its session is swept below) or
		// entirely after (and it reads the activated factor). The advisory
		// lock is what a single writer does by existing.
		//
		// One clock reading serves both the value written and the expiry test,
		// where PostgreSQL's two now() calls read one transaction timestamp.
		now := s.nowText()
		err := tx.QueryRowContext(ctx,
			`UPDATE user_totp SET activated_at = ?
			  WHERE user_id = ?
			    AND activated_at IS NULL
			    AND verified_at IS NOT NULL
			    AND setup_expires_at > ?
			    AND EXISTS (SELECT 1 FROM user_recovery_codes
			                 WHERE user_id = ? AND used_at IS NULL)
			 RETURNING activated_at`,
			now, userID, now, userID,
		).Scan(timeScan{dst: &activatedAt})
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrTotpSetupNotVerified
		}
		if err != nil {
			return fmt.Errorf("set activated: %w", err)
		}

		// PostgreSQL writes `AND totp_enrollment_required`; a SQLite boolean
		// is INTEGER 0/1, so the predicate is spelled out.
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET totp_enrollment_required = 0
			  WHERE user_id = ? AND totp_enrollment_required = 1`,
			userID,
		); err != nil {
			return fmt.Errorf("clear enrollment flags: %w", err)
		}
		return nil
	})
	if errors.Is(err, storage.ErrTotpSetupNotVerified) {
		return time.Time{}, storage.ErrTotpSetupNotVerified
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
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// The organisation's policy outranks the account's own preference,
		// and it is read inside this transaction so an administrator turning
		// it on cannot be raced by a disable that read the old value.
		//
		// Without this the enforcement built in 1.6 binds on everybody except
		// the people it is aimed at: enrol, sign in without the flag, switch
		// the second factor back off, and keep refreshing. The gate only ever
		// looks at sessions minted since, so that account never sees it again.
		//
		// The PostgreSQL driver relies on READ COMMITTED plus the write lock
		// its own UPDATE of org_settings takes; here the read and the deletes
		// are one write transaction, so a policy change is strictly before or
		// strictly after this whole method.
		var required bool
		if err := tx.QueryRowContext(ctx, `SELECT require_totp FROM org_settings`).Scan(&required); err != nil {
			return fmt.Errorf("read org policy: %w", err)
		}
		if required {
			return storage.ErrTotpRequiredByOrg
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM totp_challenges WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("drop challenges: %w", err)
		}

		res, err := tx.ExecContext(ctx,
			`DELETE FROM user_totp WHERE user_id = ? AND activated_at IS NOT NULL`,
			userID,
		)
		if err != nil {
			return fmt.Errorf("drop secret: %w", err)
		}
		n, err := rowsAffected(res)
		if err != nil {
			return fmt.Errorf("drop secret: %w", err)
		}
		if n == 0 {
			return storage.ErrTotpNotEnabled
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
// hashes is a callback for the same reason storage.TotpSetupVerification's
// is: it is called only after the check passed, inside the transaction, so a
// refused regeneration costs no argon2id work at all. Ten hashes at 64 MiB
// each is nearly a second of CPU and 640 MiB of memory traffic, and an
// endpoint that spends it before deciding whether it will do anything is a
// lever an attacker with one session can pull repeatedly. Moving the check
// out to the caller instead would reintroduce the TOCTOU this transaction
// exists to close.
//
// In home mode that second of CPU is spent while this transaction holds the
// database's write lock, so every other writer waits behind it — and, because
// the driver runs on a single connection (sqlitestore.go), so does every
// reader, /readyz among them. That cost is accepted deliberately and it is
// bounded: one regeneration, on an account that has already proved it has a
// second factor, on a household-sized instance. The alternative — hashing
// before the check — spends the same CPU on accounts that qualify for
// nothing, which is the attack the ordering closes.
//
// ponytail: this is the longest lock hold in the driver by three orders of
// magnitude. If a home instance ever notices a second of stalled requests
// here, the fix is to split the transaction — commit the check, hash, then
// re-check and insert — not to hash first.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes func() []string) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.ReplaceRecoveryCodes reads this row FOR UPDATE so a
		// concurrent DisableTotp cannot switch the factor off between the
		// check and the insert, leaving a password-only account holding live
		// recovery codes. A disable is a write transaction too, so here it
		// cannot run between these two statements at all.
		var enabled bool
		err := tx.QueryRowContext(ctx,
			`SELECT activated_at IS NOT NULL FROM user_totp WHERE user_id = ?`,
			userID,
		).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrTotpNotEnabled
		}
		if err != nil {
			return fmt.Errorf("lock secret: %w", err)
		}
		if !enabled {
			return storage.ErrTotpNotEnabled
		}
		return s.replaceRecoveryCodes(ctx, tx, userID, hashes())
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
// The replacement is a single upsert against the UNIQUE (user_id) index
// (migration 0005) rather than a delete-then-insert, and the reason survives
// the port intact even though the concurrency argument changes. On
// PostgreSQL the index is what makes two concurrent mints collide at all: a
// delete matching no row locks nothing, so both could insert and one account
// would hold two five-guess budgets. Here two mints cannot overlap, but the
// invariant is still worth stating in the schema rather than in whoever
// writes the next query — and a single statement is also the only shape that
// needs no transaction of its own.
func (s *Store) CreateTotpChallenge(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	now := s.clock()
	// The id is generated here: SQLite has no gen_random_uuid(), and nothing
	// in this schema tree defaults one (migration 0001 says so). A conflicting
	// upsert keeps the existing row's id, exactly as the PostgreSQL DO UPDATE
	// does.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO totp_challenges (id, user_id, token_hash, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (user_id) DO UPDATE
		     SET token_hash  = excluded.token_hash,
		         expires_at  = excluded.expires_at,
		         attempts    = 0,
		         consumed_at = NULL,
		         created_at  = excluded.created_at`,
		uuid.New(), userID, tokenHash, asTime(now.Add(ttl)), asTime(now),
	)
	if err != nil {
		return fmt.Errorf("create totp challenge: %w", err)
	}
	return nil
}

// TotpChallengeUserByTokenHash returns the account a challenge token was
// minted for, or storage.ErrNotFound when the token matches no challenge row.
// It deliberately matches spent and expired rows too: the caller (the
// two-step sign-in's per-account rate limiter) needs the account's identity,
// and liveness is CompleteTotpChallenge's decision.
func (s *Store) TotpChallengeUserByTokenHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM totp_challenges WHERE token_hash = ?`, tokenHash,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, storage.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("totp challenge user by token hash: %w", err)
	}
	return userID, nil
}

// CompleteTotpChallenge is the second half of a two-step sign-in, in one
// transaction: the challenge, the replay guard, the recovery-code
// consumption and the session that results either all happen or none do.
//
// That atomicity is the point of the single transaction — a recovery code
// must not be spent by a sign-in that then fails to produce a session, and a
// session must not exist for a code that was never consumed.
func (s *Store) CompleteTotpChallenge(ctx context.Context, att storage.TotpChallengeAttempt) (storage.User, storage.Session, storage.TotpChallengeOutcome, error) {
	var (
		user    storage.User
		sess    storage.Session
		outcome = storage.TotpChallengeNone
	)

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.CompleteTotpChallenge takes this row FOR UPDATE so two
		// attempts on one challenge serialize: without it both could read
		// attempts = 4 and both proceed, and the guess budget the cap exists
		// to enforce would be whatever concurrency the attacker can muster.
		// Here the second attempt waits at BEGIN, so the row this transaction
		// reads is the row it will write.
		var challengeID, userID uuid.UUID
		err := tx.QueryRowContext(ctx,
			`SELECT id, user_id FROM totp_challenges
			  WHERE token_hash = ?
			    AND consumed_at IS NULL
			    AND expires_at > ?
			    AND attempts < ?`,
			att.TokenHash, s.nowText(), att.MaxAttempts,
		).Scan(&challengeID, &userID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock challenge: %w", err)
		}

		// The second FOR UPDATE the PostgreSQL driver takes, on user_totp. It
		// pins the activation fact for the rest of the transaction, which is
		// also why that driver does NOT take its session-mint advisory lock
		// here — a row lock after an advisory lock would order the two
		// inversely to ActivateTotp and invite a deadlock. Neither lock exists
		// here and neither ordering question arises: an activation and this
		// sign-in are both write transactions and cannot interleave.
		var secret []byte
		var step sql.NullInt64
		err = tx.QueryRowContext(ctx,
			`SELECT secret, last_used_step FROM user_totp
			  WHERE user_id = ? AND activated_at IS NOT NULL`,
			userID,
		).Scan(&secret, &step)
		if errors.Is(err, sql.ErrNoRows) {
			// The second factor was turned off while the challenge was open;
			// the challenge has nothing left to verify against.
			return s.consumeChallenge(ctx, tx, challengeID)
		}
		if err != nil {
			return fmt.Errorf("lock secret: %w", err)
		}

		accepted, err := s.acceptChallengeCode(ctx, tx, att, userID, secret, int64Ptr(step))
		if err != nil {
			return err
		}
		if !accepted {
			revoked, burnErr := s.burnChallengeAttempt(ctx, tx, challengeID, att.MaxAttempts)
			if burnErr != nil {
				return burnErr
			}
			outcome = storage.TotpChallengeRejected
			if revoked {
				outcome = storage.TotpChallengeRevoked
			}
			return nil
		}

		if consumeErr := s.consumeChallenge(ctx, tx, challengeID); consumeErr != nil {
			return consumeErr
		}
		if sess, err = s.insertSessionFamily(ctx, tx, userID, att.Session); err != nil {
			return err
		}
		if user, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, userID)); err != nil {
			return fmt.Errorf("load authenticated user: %w", err)
		}
		outcome = storage.TotpChallengeCompleted
		return nil
	})
	if err != nil {
		return storage.User{}, storage.Session{}, storage.TotpChallengeNone, fmt.Errorf("complete totp challenge: %w", err)
	}
	return user, sess, outcome, nil
}

// acceptChallengeCode tries the presented credential against the account's
// second factor and records what a success consumes: the time step for an
// authenticator code, the code row for a recovery code.
func (s *Store) acceptChallengeCode(
	ctx context.Context,
	q querier,
	att storage.TotpChallengeAttempt,
	userID uuid.UUID,
	secret []byte,
	lastUsedStep *int64,
) (bool, error) {
	if att.CheckCode != nil {
		step, ok := att.CheckCode(secret, lastUsedStep)
		if !ok {
			return false, nil
		}
		if _, err := q.ExecContext(ctx,
			`UPDATE user_totp SET last_used_step = ? WHERE user_id = ?`,
			step, userID,
		); err != nil {
			return false, fmt.Errorf("record used step: %w", err)
		}
		return true, nil
	}

	if att.MatchRecoveryCode != nil {
		return s.consumeRecoveryCode(ctx, q, userID, att.MatchRecoveryCode)
	}
	return false, nil
}

// consumeRecoveryCode walks the account's unused codes and spends the one
// that matches.
//
// storage.consumeRecoveryCode locks the candidate rows FOR UPDATE, so a code
// cannot be spent twice by two concurrent sign-ins. Here no other writer can
// be inside this transaction's window at all, so the rows are simply read.
// They are still read to completion before the UPDATE, for a mechanical
// reason rather than a locking one: a *sql.Rows holds the transaction's
// connection until it is closed, exactly as a pgx transaction runs one query
// at a time.
func (s *Store) consumeRecoveryCode(ctx context.Context, q querier, userID uuid.UUID, match func(storedHash string) bool) (bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, code_hash FROM user_recovery_codes
		  WHERE user_id = ? AND used_at IS NULL
		 ORDER BY created_at, id`,
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
			return false, errors.Join(fmt.Errorf("read recovery code: %w", err), rows.Close())
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, fmt.Errorf("read recovery codes: %w", err)
	}

	for _, c := range candidates {
		if !match(c.hash) {
			continue
		}
		res, err := q.ExecContext(ctx,
			`UPDATE user_recovery_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`,
			s.nowText(), c.id,
		)
		if err != nil {
			return false, fmt.Errorf("spend recovery code: %w", err)
		}
		n, err := rowsAffected(res)
		if err != nil {
			return false, fmt.Errorf("spend recovery code: %w", err)
		}
		// Nothing else can have written this row since it was read — this
		// transaction holds the write lock — so exactly one update is the only
		// possible result; anything else means the invariant broke.
		if n != 1 {
			return false, errors.New("spend recovery code: row changed under the write lock")
		}
		return true, nil
	}
	return false, nil
}

// burnSetupAttempt counts one wrong setup code and reports whether it was the
// attempt that reached the cap, in which case the pending setup is deleted.
func burnSetupAttempt(ctx context.Context, q querier, userID uuid.UUID, maxAttempts int) (bool, error) {
	var attempts int
	if err := q.QueryRowContext(ctx,
		`UPDATE user_totp SET verify_attempts = verify_attempts + 1
		  WHERE user_id = ? RETURNING verify_attempts`,
		userID,
	).Scan(&attempts); err != nil {
		return false, fmt.Errorf("count wrong setup code: %w", err)
	}
	if attempts < maxAttempts {
		return false, nil
	}
	if err := deletePendingSetup(ctx, q, userID); err != nil {
		return false, err
	}
	return true, nil
}

// burnChallengeAttempt counts one wrong sign-in code and reports whether it
// was the attempt that reached the cap, in which case the challenge is
// consumed and the caller starts again at the password step.
func (s *Store) burnChallengeAttempt(ctx context.Context, q querier, challengeID uuid.UUID, maxAttempts int) (bool, error) {
	var attempts int
	if err := q.QueryRowContext(ctx,
		`UPDATE totp_challenges SET attempts = attempts + 1 WHERE id = ? RETURNING attempts`,
		challengeID,
	).Scan(&attempts); err != nil {
		return false, fmt.Errorf("count wrong sign-in code: %w", err)
	}
	if attempts < maxAttempts {
		return false, nil
	}
	if err := s.consumeChallenge(ctx, q, challengeID); err != nil {
		return false, err
	}
	return true, nil
}

// consumeChallenge marks a challenge spent; nothing revives it.
func (s *Store) consumeChallenge(ctx context.Context, q querier, challengeID uuid.UUID) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE totp_challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		s.nowText(), challengeID,
	); err != nil {
		return fmt.Errorf("consume challenge: %w", err)
	}
	return nil
}

// deletePendingSetup drops a pending setup and anything it issued. It never
// touches an active second factor.
func deletePendingSetup(ctx context.Context, q querier, userID uuid.UUID) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM user_totp WHERE user_id = ? AND activated_at IS NULL`,
		userID,
	); err != nil {
		return fmt.Errorf("drop pending setup: %w", err)
	}
	return deleteRecoveryCodes(ctx, q, userID)
}

// replaceRecoveryCodes swaps the account's whole set for hashes.
//
// PostgreSQL inserts the whole set in one statement over unnest($2::text[]).
// SQLite has neither arrays nor unnest, so this is a loop of single-row
// inserts inside the caller's transaction — ten statements at the size the
// caller actually uses, all under one write lock, which is the same atomic
// fact for a cost nothing at this scale can measure. Every row is given the
// same created_at, so ORDER BY created_at, id in consumeRecoveryCode breaks
// the tie on id exactly as it does on PostgreSQL.
func (s *Store) replaceRecoveryCodes(ctx context.Context, q querier, userID uuid.UUID, hashes []string) error {
	if err := deleteRecoveryCodes(ctx, q, userID); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return errors.New("install recovery codes: empty set")
	}
	createdAt := s.nowText()
	for _, hash := range hashes {
		if _, err := q.ExecContext(ctx,
			`INSERT INTO user_recovery_codes (id, user_id, code_hash, created_at)
			 VALUES (?, ?, ?, ?)`,
			uuid.New(), userID, hash, createdAt,
		); err != nil {
			return fmt.Errorf("install recovery codes: %w", err)
		}
	}
	return nil
}

func deleteRecoveryCodes(ctx context.Context, q querier, userID uuid.UUID) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("drop recovery codes: %w", err)
	}
	return nil
}

// scanTotp scans one totpColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound.
func scanTotp(row rowScanner) (storage.Totp, error) {
	var (
		t    storage.Totp
		step sql.NullInt64
	)
	err := row.Scan(
		&t.UserID, &t.Secret,
		nullTimeScan{dst: &t.VerifiedAt}, nullTimeScan{dst: &t.ActivatedAt},
		timeScan{dst: &t.SetupExpiresAt},
		&t.VerifyAttempts, &step, timeScan{dst: &t.CreatedAt},
	)
	if err != nil {
		return storage.Totp{}, notFound(err)
	}
	t.LastUsedStep = int64Ptr(step)
	return t, nil
}
