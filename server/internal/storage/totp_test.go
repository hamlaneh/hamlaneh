package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const (
	totpTestMaxAttempts = 5
	totpTestSetupTTL    = time.Hour
)

// totpTestSecret is opaque to storage: the verification callback decides, not
// the database.
var totpTestSecret = []byte("12345678901234567890")

// acceptTotpStep returns a CheckCode callback that accepts, claiming step.
func acceptTotpStep(step int64) func([]byte, *int64) (int64, bool) {
	return func(_ []byte, _ *int64) (int64, bool) { return step, true }
}

// rejectTotpCode is a CheckCode callback that never accepts.
func rejectTotpCode(_ []byte, _ *int64) (int64, bool) { return 0, false }

// totpCodeHashes builds a placeholder recovery-code set. Hashing is the
// handler's job; storage only stores strings.
func totpCodeHashes(prefix string, n int) []string {
	hashes := make([]string, 0, n)
	for i := range n {
		hashes = append(hashes, prefix+string(rune('a'+i)))
	}
	return hashes
}

// hashesFunc is totpCodeHashes in the callback shape the store takes, so
// the hashing a real caller would do stays inside the transaction.
func hashesFunc(prefix string, n int) func() []string {
	return func() []string { return totpCodeHashes(prefix, n) }
}

func matchTotpCodeHash(want string) func(string) bool {
	return func(stored string) bool { return stored == want }
}

// startVerifiedTotpSetup walks a user to the verified-but-not-active state.
func startVerifiedTotpSetup(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID, hashes []string) {
	t.Helper()

	if err := store.StartTotpSetup(ctx, userID, totpTestSecret, totpTestSetupTTL); err != nil {
		t.Fatalf("StartTotpSetup: %v", err)
	}
	outcome, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
		UserID:             userID,
		MaxAttempts:        totpTestMaxAttempts,
		CheckCode:          acceptTotpStep(100),
		RecoveryCodeHashes: func() []string { return hashes },
	})
	if err != nil {
		t.Fatalf("VerifyTotpSetup: %v", err)
	}
	if outcome != storage.TotpVerifyAccepted {
		t.Fatalf("VerifyTotpSetup outcome %v, want accepted", outcome)
	}
}

// enableTotp walks a user all the way to two-step verification on.
func enableTotp(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID, hashes []string) {
	t.Helper()

	startVerifiedTotpSetup(ctx, t, store, userID, hashes)
	if _, err := store.ActivateTotp(ctx, userID); err != nil {
		t.Fatalf("ActivateTotp: %v", err)
	}
}

// expireTotpSetup pushes a pending setup's deadline into the past, the one state
// no public method can produce on demand.
func expireTotpSetup(ctx context.Context, t *testing.T, dsn string, userID uuid.UUID) {
	t.Helper()
	execTotpSQL(ctx, t, dsn, `UPDATE user_totp SET setup_expires_at = now() - interval '1 second' WHERE user_id = $1`, userID)
}

func execTotpSQL(ctx context.Context, t *testing.T, dsn, sql string, args ...any) {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for raw SQL: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(ctx); closeErr != nil {
			t.Errorf("close raw SQL connection: %v", closeErr)
		}
	}()

	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("raw SQL: %v", err)
	}
}

func TestTotpSetupLifecycleIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()

	t.Run("start creates a pending setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("setupstart"))
		if err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, totpTestSetupTTL); err != nil {
			t.Fatalf("StartTotpSetup: %v", err)
		}

		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if record.Enabled() {
			t.Error("a pending setup reports as enabled")
		}
		if string(record.Secret) != string(totpTestSecret) {
			t.Error("stored secret does not round-trip")
		}
		if record.VerifiedAt != nil || record.LastUsedStep != nil || record.VerifyAttempts != 0 {
			t.Error("a fresh setup is not in its initial state")
		}
		if !record.SetupExpiresAt.After(record.CreatedAt) {
			t.Error("pending setup is born expired")
		}
	})

	t.Run("start again replaces the pending setup and its codes", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("setupreplace"))
		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("first", 3))

		replacement := []byte("09876543210987654321")
		if err := store.StartTotpSetup(ctx, user.ID, replacement, totpTestSetupTTL); err != nil {
			t.Fatalf("StartTotpSetup again: %v", err)
		}

		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if string(record.Secret) != string(replacement) {
			t.Error("the replacement secret was not stored")
		}
		if record.VerifiedAt != nil {
			t.Error("the replacement setup inherited the previous verification")
		}
		remaining, total, err := store.RecoveryCodeCounts(ctx, user.ID)
		if err != nil {
			t.Fatalf("RecoveryCodeCounts: %v", err)
		}
		if remaining != 0 || total != 0 {
			t.Errorf("codes from the abandoned setup survived: %d of %d", remaining, total)
		}
	})

	t.Run("start refuses an account that already has it on", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("setupenabled"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("live", 3))

		err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, totpTestSetupTTL)
		if !errors.Is(err, storage.ErrTotpAlreadyEnabled) {
			t.Fatalf("StartTotpSetup on an enabled account: %v, want ErrTotpAlreadyEnabled", err)
		}
	})

	t.Run("verify accepts, records the step and installs the codes", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("verifyok"))
		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("ok", 10))

		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if record.VerifiedAt == nil {
			t.Error("verification was not recorded")
		}
		if record.LastUsedStep == nil || *record.LastUsedStep != 100 {
			t.Error("the accepted step was not recorded")
		}
		if record.Enabled() {
			t.Error("verification switched two-step verification on by itself")
		}
		remaining, total, err := store.RecoveryCodeCounts(ctx, user.ID)
		if err != nil {
			t.Fatalf("RecoveryCodeCounts: %v", err)
		}
		if remaining != 10 || total != 10 {
			t.Errorf("got %d of %d codes, want 10 of 10", remaining, total)
		}
	})

	t.Run("verify without a setup reports nothing to verify", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("verifynone"))
		outcome, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
			UserID:             user.ID,
			MaxAttempts:        totpTestMaxAttempts,
			CheckCode:          acceptTotpStep(1),
			RecoveryCodeHashes: func() []string { return totpCodeHashes("none", 10) },
		})
		if err != nil {
			t.Fatalf("VerifyTotpSetup: %v", err)
		}
		if outcome != storage.TotpVerifyNoSetup {
			t.Errorf("outcome %v, want no setup", outcome)
		}
	})

	t.Run("verify drops an expired setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("verifyexpired"))
		if err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, -time.Second); err != nil {
			t.Fatalf("StartTotpSetup: %v", err)
		}

		outcome, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
			UserID:             user.ID,
			MaxAttempts:        totpTestMaxAttempts,
			CheckCode:          acceptTotpStep(1),
			RecoveryCodeHashes: func() []string { return totpCodeHashes("expired", 10) },
		})
		if err != nil {
			t.Fatalf("VerifyTotpSetup: %v", err)
		}
		if outcome != storage.TotpVerifyNoSetup {
			t.Errorf("outcome %v, want no setup", outcome)
		}
		if _, err := store.TotpByUser(ctx, user.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Error("the expired setup was left behind")
		}
	})

	t.Run("the attempt cap revokes the setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("verifycap"))
		if err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, totpTestSetupTTL); err != nil {
			t.Fatalf("StartTotpSetup: %v", err)
		}

		for attempt := 1; attempt <= totpTestMaxAttempts; attempt++ {
			outcome, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
				UserID:             user.ID,
				MaxAttempts:        totpTestMaxAttempts,
				CheckCode:          rejectTotpCode,
				RecoveryCodeHashes: func() []string { return totpCodeHashes("cap", 10) },
			})
			if err != nil {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			want := storage.TotpVerifyRejected
			if attempt == totpTestMaxAttempts {
				want = storage.TotpVerifyRevoked
			}
			if outcome != want {
				t.Fatalf("attempt %d: outcome %v, want %v", attempt, outcome, want)
			}
		}

		if _, err := store.TotpByUser(ctx, user.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Error("the revoked setup survived the cap")
		}
	})

	t.Run("a correct code clears the attempt count", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("verifyreset"))
		if err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, totpTestSetupTTL); err != nil {
			t.Fatalf("StartTotpSetup: %v", err)
		}
		for range totpTestMaxAttempts - 1 {
			if _, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
				UserID:             user.ID,
				MaxAttempts:        totpTestMaxAttempts,
				CheckCode:          rejectTotpCode,
				RecoveryCodeHashes: func() []string { return totpCodeHashes("reset", 10) },
			}); err != nil {
				t.Fatalf("wrong attempt: %v", err)
			}
		}

		outcome, err := store.VerifyTotpSetup(ctx, storage.TotpSetupVerification{
			UserID:             user.ID,
			MaxAttempts:        totpTestMaxAttempts,
			CheckCode:          acceptTotpStep(7),
			RecoveryCodeHashes: func() []string { return totpCodeHashes("reset", 10) },
		})
		if err != nil {
			t.Fatalf("VerifyTotpSetup: %v", err)
		}
		if outcome != storage.TotpVerifyAccepted {
			t.Fatalf("outcome %v, want accepted", outcome)
		}
		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if record.VerifyAttempts != 0 {
			t.Errorf("attempts left at %d after a correct code", record.VerifyAttempts)
		}
	})

	t.Run("activate refuses an unverified setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("activateunverified"))
		if err := store.StartTotpSetup(ctx, user.ID, totpTestSecret, totpTestSetupTTL); err != nil {
			t.Fatalf("StartTotpSetup: %v", err)
		}

		if _, err := store.ActivateTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpSetupNotVerified) {
			t.Fatalf("ActivateTotp: %v, want ErrTotpSetupNotVerified", err)
		}
		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if record.Enabled() {
			t.Error("an unverified setup was activated")
		}
	})

	t.Run("activate refuses when nothing exists", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("activatenone"))
		if _, err := store.ActivateTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpSetupNotVerified) {
			t.Fatalf("ActivateTotp: %v, want ErrTotpSetupNotVerified", err)
		}
	})

	t.Run("activate refuses an expired setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("activateexpired"))
		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("exp", 10))
		expireTotpSetup(ctx, t, dsn, user.ID)

		if _, err := store.ActivateTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpSetupNotVerified) {
			t.Fatalf("ActivateTotp: %v, want ErrTotpSetupNotVerified", err)
		}
	})

	t.Run("activate refuses a verified setup with no recovery codes", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("activatenocodes"))
		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("gone", 10))
		execTotpSQL(ctx, t, dsn, `DELETE FROM user_recovery_codes WHERE user_id = $1`, user.ID)

		if _, err := store.ActivateTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpSetupNotVerified) {
			t.Fatalf("ActivateTotp: %v, want ErrTotpSetupNotVerified", err)
		}
	})

	t.Run("activate twice is refused the second time", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("activatetwice"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("twice", 10))

		if _, err := store.ActivateTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpSetupNotVerified) {
			t.Fatalf("second ActivateTotp: %v, want ErrTotpSetupNotVerified", err)
		}
	})
}

func TestTotpDisableAndRegenerateIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	t.Run("disable clears the secret, the codes and any challenge", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("disableok"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("dis", 10))
		if err := store.CreateTotpChallenge(ctx, user.ID, hashOf("live-challenge"), time.Minute); err != nil {
			t.Fatalf("CreateTotpChallenge: %v", err)
		}

		if err := store.DisableTotp(ctx, user.ID); err != nil {
			t.Fatalf("DisableTotp: %v", err)
		}
		if _, err := store.TotpByUser(ctx, user.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Error("the secret survived disabling")
		}
		remaining, total, err := store.RecoveryCodeCounts(ctx, user.ID)
		if err != nil {
			t.Fatalf("RecoveryCodeCounts: %v", err)
		}
		if remaining != 0 || total != 0 {
			t.Errorf("recovery codes survived disabling: %d of %d", remaining, total)
		}

		_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("live-challenge"),
			MaxAttempts: totpTestMaxAttempts,
			CheckCode:   acceptTotpStep(1),
			Session:     tokensFor("a", "r"),
		})
		if err != nil {
			t.Fatalf("CompleteTotpChallenge: %v", err)
		}
		if outcome != storage.TotpChallengeNone {
			t.Errorf("a challenge survived disabling: outcome %v", outcome)
		}
	})

	t.Run("disable refuses a pending setup", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("disablepending"))
		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("pend", 10))

		if err := store.DisableTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpNotEnabled) {
			t.Fatalf("DisableTotp: %v, want ErrTotpNotEnabled", err)
		}
		if _, err := store.TotpByUser(ctx, user.ID); err != nil {
			t.Error("a refused disable removed the pending setup anyway")
		}
	})

	t.Run("disable refuses an account without it", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("disablenone"))
		if err := store.DisableTotp(ctx, user.ID); !errors.Is(err, storage.ErrTotpNotEnabled) {
			t.Fatalf("DisableTotp: %v, want ErrTotpNotEnabled", err)
		}
	})

	t.Run("regenerate replaces the whole set", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("regenok"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("old", 10))

		if err := store.ReplaceRecoveryCodes(ctx, user.ID, hashesFunc("new", 10)); err != nil {
			t.Fatalf("ReplaceRecoveryCodes: %v", err)
		}

		// An old code no longer matches anything; a new one does.
		_, _, outcome := attemptTotpRecoveryLogin(ctx, t, store, "old-set-token", user.ID, "olda")
		if outcome != storage.TotpChallengeRejected {
			t.Errorf("an invalidated code was accepted: outcome %v", outcome)
		}
		_, _, outcome = attemptTotpRecoveryLogin(ctx, t, store, "new-set-token", user.ID, "newa")
		if outcome != storage.TotpChallengeCompleted {
			t.Errorf("a fresh code was refused: outcome %v", outcome)
		}
	})

	// A refused regeneration must cost no hashing: the caller's callback
	// runs ten argon2id hashes at 64 MiB each, and spending that before the
	// store has decided it will do anything hands an attacker holding one
	// session a lever to pull. The counter is what proves the work is
	// genuinely deferred rather than merely thrown away afterwards.
	t.Run("regenerate refuses an account without it on, hashing nothing", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("regenoff"))

		hashed := 0
		counted := func() []string {
			hashed++
			return totpCodeHashes("nope", 10)
		}

		err := store.ReplaceRecoveryCodes(ctx, user.ID, counted)
		if !errors.Is(err, storage.ErrTotpNotEnabled) {
			t.Fatalf("ReplaceRecoveryCodes: %v, want ErrTotpNotEnabled", err)
		}

		startVerifiedTotpSetup(ctx, t, store, user.ID, totpCodeHashes("pending", 10))
		err = store.ReplaceRecoveryCodes(ctx, user.ID, counted)
		if !errors.Is(err, storage.ErrTotpNotEnabled) {
			t.Fatalf("ReplaceRecoveryCodes on a pending setup: %v, want ErrTotpNotEnabled", err)
		}

		if hashed != 0 {
			t.Errorf("a refused regeneration hashed %d times, want 0 — the work must "+
				"happen inside the transaction, after the enabled check", hashed)
		}

		// And the callback does run once the account qualifies, so the
		// counter above is measuring laziness and not a callback that is
		// never called at all.
		if _, err := store.ActivateTotp(ctx, user.ID); err != nil {
			t.Fatalf("ActivateTotp: %v", err)
		}
		if err := store.ReplaceRecoveryCodes(ctx, user.ID, counted); err != nil {
			t.Fatalf("ReplaceRecoveryCodes on an enabled account: %v", err)
		}
		if hashed != 1 {
			t.Errorf("an accepted regeneration hashed %d times, want exactly 1", hashed)
		}
	})
}

func TestTotpChallengeIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	t.Run("an authenticator code completes the sign-in once", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengeok"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("chal", 10))
		mustCreateTotpChallenge(ctx, t, store, user.ID, "ok-token", time.Minute)

		signedIn, sess, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("ok-token"),
			MaxAttempts: totpTestMaxAttempts,
			CheckCode:   acceptTotpStep(200),
			Session:     tokensFor("access-ok", "refresh-ok"),
		})
		if err != nil {
			t.Fatalf("CompleteTotpChallenge: %v", err)
		}
		if outcome != storage.TotpChallengeCompleted {
			t.Fatalf("outcome %v, want completed", outcome)
		}
		if signedIn.ID != user.ID {
			t.Error("the wrong user was returned")
		}
		if sess.FamilyID == uuid.Nil || sess.UserID != user.ID {
			t.Error("no session family was created")
		}
		if _, _, authErr := store.SessionUserByAccessHash(ctx, hashOf("access-ok")); authErr != nil {
			t.Errorf("the minted session does not authenticate: %v", authErr)
		}

		// The accepted step is recorded, and the challenge is spent.
		record, err := store.TotpByUser(ctx, user.ID)
		if err != nil {
			t.Fatalf("TotpByUser: %v", err)
		}
		if record.LastUsedStep == nil || *record.LastUsedStep != 200 {
			t.Error("the accepted step was not recorded")
		}
		_, _, outcome, err = store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("ok-token"),
			MaxAttempts: totpTestMaxAttempts,
			CheckCode:   acceptTotpStep(201),
			Session:     tokensFor("access-again", "refresh-again"),
		})
		if err != nil {
			t.Fatalf("second CompleteTotpChallenge: %v", err)
		}
		if outcome != storage.TotpChallengeNone {
			t.Errorf("a consumed challenge was reusable: outcome %v", outcome)
		}
	})

	t.Run("an unknown or expired challenge is nothing", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengedead"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("dead", 10))
		mustCreateTotpChallenge(ctx, t, store, user.ID, "expired-token", -time.Second)

		for _, token := range []string{"never-issued", "expired-token"} {
			_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
				TokenHash:   hashOf(token),
				MaxAttempts: totpTestMaxAttempts,
				CheckCode:   acceptTotpStep(1),
				Session:     tokensFor("access-"+token, "refresh-"+token),
			})
			if err != nil {
				t.Fatalf("CompleteTotpChallenge(%s): %v", token, err)
			}
			if outcome != storage.TotpChallengeNone {
				t.Errorf("%s: outcome %v, want none", token, outcome)
			}
		}
	})

	t.Run("minting a challenge drops the previous one", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengeone"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("one", 10))
		mustCreateTotpChallenge(ctx, t, store, user.ID, "first-token", time.Minute)
		mustCreateTotpChallenge(ctx, t, store, user.ID, "second-token", time.Minute)

		_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("first-token"),
			MaxAttempts: totpTestMaxAttempts,
			CheckCode:   acceptTotpStep(1),
			Session:     tokensFor("access-first", "refresh-first"),
		})
		if err != nil {
			t.Fatalf("CompleteTotpChallenge: %v", err)
		}
		if outcome != storage.TotpChallengeNone {
			t.Errorf("the superseded challenge still worked: outcome %v", outcome)
		}
	})

	t.Run("the attempt cap revokes the challenge", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengecap"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("cap", 10))
		mustCreateTotpChallenge(ctx, t, store, user.ID, "cap-token", time.Minute)

		for attempt := 1; attempt <= totpTestMaxAttempts; attempt++ {
			_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
				TokenHash:   hashOf("cap-token"),
				MaxAttempts: totpTestMaxAttempts,
				CheckCode:   rejectTotpCode,
				Session:     tokensFor("access-cap", "refresh-cap"),
			})
			if err != nil {
				t.Fatalf("attempt %d: %v", attempt, err)
			}
			want := storage.TotpChallengeRejected
			if attempt == totpTestMaxAttempts {
				want = storage.TotpChallengeRevoked
			}
			if outcome != want {
				t.Fatalf("attempt %d: outcome %v, want %v", attempt, outcome, want)
			}
		}

		// Even the right code cannot save a revoked challenge.
		_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("cap-token"),
			MaxAttempts: totpTestMaxAttempts,
			CheckCode:   acceptTotpStep(9),
			Session:     tokensFor("access-cap2", "refresh-cap2"),
		})
		if err != nil {
			t.Fatalf("CompleteTotpChallenge after the cap: %v", err)
		}
		if outcome != storage.TotpChallengeNone {
			t.Errorf("outcome %v, want none", outcome)
		}
	})

	t.Run("a recovery code signs in once and never again", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengerecovery"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("rec", 10))

		_, _, outcome := attemptTotpRecoveryLogin(ctx, t, store, "rec-first", user.ID, "reca")
		if outcome != storage.TotpChallengeCompleted {
			t.Fatalf("first use of a recovery code: outcome %v, want completed", outcome)
		}
		remaining, total, err := store.RecoveryCodeCounts(ctx, user.ID)
		if err != nil {
			t.Fatalf("RecoveryCodeCounts: %v", err)
		}
		if remaining != 9 || total != 10 {
			t.Errorf("got %d of %d unused codes, want 9 of 10", remaining, total)
		}

		_, _, outcome = attemptTotpRecoveryLogin(ctx, t, store, "rec-second", user.ID, "reca")
		if outcome != storage.TotpChallengeRejected {
			t.Errorf("a spent recovery code was accepted again: outcome %v", outcome)
		}

		// A different, unused code still works.
		_, _, outcome = attemptTotpRecoveryLogin(ctx, t, store, "rec-third", user.ID, "recb")
		if outcome != storage.TotpChallengeCompleted {
			t.Errorf("an unused code was refused: outcome %v", outcome)
		}
	})

	t.Run("a code with no valid shape still costs an attempt", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("challengeshape"))
		enableTotp(ctx, t, store, user.ID, totpCodeHashes("shape", 10))
		mustCreateTotpChallenge(ctx, t, store, user.ID, "shape-token", time.Minute)

		_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
			TokenHash:   hashOf("shape-token"),
			MaxAttempts: totpTestMaxAttempts,
			Session:     tokensFor("access-shape", "refresh-shape"),
		})
		if err != nil {
			t.Fatalf("CompleteTotpChallenge: %v", err)
		}
		if outcome != storage.TotpChallengeRejected {
			t.Errorf("outcome %v, want rejected", outcome)
		}
	})
}

// TestTotpChallengeConcurrentMintIntegration pins the storage half of the
// two-parallel-budgets finding: two racing password logins must leave
// exactly ONE live challenge, however their transactions interleave. The
// old delete-then-insert had no lock for concurrent mints to meet on (a
// delete matching no row locks nothing), so both could insert and both
// tokens would answer — one account holding two five-guess budgets. Under
// the UNIQUE (user_id) upsert exactly one of the two tokens survives, every
// round, by construction.
func TestTotpChallengeConcurrentMintIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	user := mustCreateUser(ctx, t, store, newUser("challengerace"))
	enableTotp(ctx, t, store, user.ID, totpCodeHashes("race", 10))

	// One round is one pair of concurrent logins; the race window is the
	// whole storage call, so a modest number of rounds catches the old
	// behavior reliably while staying fast.
	const rounds = 25
	for round := range rounds {
		tokenA := fmt.Sprintf("race-a-%d", round)
		tokenB := fmt.Sprintf("race-b-%d", round)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, token := range []string{tokenA, tokenB} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = store.CreateTotpChallenge(ctx, user.ID, hashOf(token), time.Minute)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent CreateTotpChallenge %d: %v", round, i, err)
			}
		}

		// Exactly one of the two tokens may complete a sign-in. The test
		// closure accepts any code on purpose: what is under test is which
		// challenges are alive, not the code arithmetic.
		completed := 0
		for _, token := range []string{tokenA, tokenB} {
			_, _, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
				TokenHash:   hashOf(token),
				MaxAttempts: totpTestMaxAttempts,
				CheckCode:   acceptTotpStep(int64(1000 + round)),
				Session:     tokensFor("access-"+token, "refresh-"+token),
			})
			if err != nil {
				t.Fatalf("round %d: CompleteTotpChallenge(%s): %v", round, token, err)
			}
			if outcome == storage.TotpChallengeCompleted {
				completed++
			}
		}
		if completed != 1 {
			t.Fatalf("round %d: %d of 2 concurrently-minted challenges were live, want exactly 1", round, completed)
		}
	}
}

// TestTotpChallengeUserByTokenHashIntegration pins the lookup the two-step
// sign-in's per-account rate limiter keys on.
func TestTotpChallengeUserByTokenHashIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	user := mustCreateUser(ctx, t, store, newUser("challengewho"))
	enableTotp(ctx, t, store, user.ID, totpCodeHashes("who", 10))

	if _, err := store.TotpChallengeUserByTokenHash(ctx, hashOf("who-token")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unknown token: err = %v, want ErrNotFound", err)
	}

	mustCreateTotpChallenge(ctx, t, store, user.ID, "who-token", time.Minute)
	got, err := store.TotpChallengeUserByTokenHash(ctx, hashOf("who-token"))
	if err != nil {
		t.Fatalf("TotpChallengeUserByTokenHash: %v", err)
	}
	if got != user.ID {
		t.Errorf("got user %s, want %s", got, user.ID)
	}

	// A spent challenge still names its account: the limiter needs the
	// identity, and liveness is CompleteTotpChallenge's decision.
	if _, _, outcome, consumeErr := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
		TokenHash:   hashOf("who-token"),
		MaxAttempts: totpTestMaxAttempts,
		CheckCode:   acceptTotpStep(7),
		Session:     tokensFor("access-who", "refresh-who"),
	}); consumeErr != nil || outcome != storage.TotpChallengeCompleted {
		t.Fatalf("consume the challenge: outcome %v, err %v", outcome, consumeErr)
	}
	got, err = store.TotpChallengeUserByTokenHash(ctx, hashOf("who-token"))
	if err != nil {
		t.Fatalf("TotpChallengeUserByTokenHash after consumption: %v", err)
	}
	if got != user.ID {
		t.Errorf("consumed token names %s, want %s", got, user.ID)
	}
}

// mustCreateTotpChallenge mints a challenge for token with the given lifetime.
func mustCreateTotpChallenge(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID, token string, ttl time.Duration) {
	t.Helper()
	if err := store.CreateTotpChallenge(ctx, userID, hashOf(token), ttl); err != nil {
		t.Fatalf("CreateTotpChallenge(%s): %v", token, err)
	}
}

// attemptTotpRecoveryLogin mints a challenge and completes it with a recovery
// code, returning what CompleteTotpChallenge decided.
func attemptTotpRecoveryLogin(
	ctx context.Context,
	t *testing.T,
	store *storage.Store,
	token string,
	userID uuid.UUID,
	codeHash string,
) (storage.User, storage.Session, storage.TotpChallengeOutcome) {
	t.Helper()

	mustCreateTotpChallenge(ctx, t, store, userID, token, time.Minute)
	user, sess, outcome, err := store.CompleteTotpChallenge(ctx, storage.TotpChallengeAttempt{
		TokenHash:         hashOf(token),
		MaxAttempts:       totpTestMaxAttempts,
		MatchRecoveryCode: matchTotpCodeHash(codeHash),
		Session:           tokensFor("access-"+token, "refresh-"+token),
	})
	if err != nil {
		t.Fatalf("CompleteTotpChallenge(%s): %v", token, err)
	}
	return user, sess, outcome
}
