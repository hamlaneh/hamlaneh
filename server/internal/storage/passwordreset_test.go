package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const resetTTL = 30 * time.Minute

// assertPool opens a second PostgreSQL pool. testdb.Raw covers everything a
// portable test needs; this is only for the one PostgreSQL-only test that has
// to hold an uncommitted transaction open (channels_test.go), which a pooled
// database/sql handle cannot express.
func assertPool(ctx context.Context, t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open assertion pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestUserByEmailIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("mailbox"))

	t.Run("matches case-insensitively", func(t *testing.T) {
		got, err := store.UserByEmail(ctx, "MAILBOX@EXAMPLE.COM")
		if err != nil {
			t.Fatalf("UserByEmail: %v", err)
		}
		if got.ID != user.ID {
			t.Errorf("matched %s, want %s", got.ID, user.ID)
		}
	})

	t.Run("unknown address is ErrNotFound", func(t *testing.T) {
		_, err := store.UserByEmail(ctx, "nobody@example.com")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("a username is not an address", func(t *testing.T) {
		// UserByIdentifier deliberately matches both columns; the reset
		// path must not, or it would confirm which usernames exist.
		if _, err := store.UserByEmail(ctx, "mailbox"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for a username", err)
		}
	})

	t.Run("an account without an address never matches", func(t *testing.T) {
		noMail := newUser("nomail")
		noMail.Email = nil
		mustCreateUser(ctx, t, store, noMail)

		if _, err := store.UserByEmail(ctx, ""); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for an empty address", err)
		}
	})
}

func TestCreatePasswordResetTokenIntegration(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("requester"))

	if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("first"), resetTTL); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	t.Run("the token lands hashed with the requested lifetime", func(t *testing.T) {
		var expiresAt, createdAt time.Time
		err := raw.QueryRow(ctx,
			`SELECT expires_at, created_at FROM password_reset_tokens WHERE token_hash = ?`,
			hashOf("first"),
		).Scan(&expiresAt, &createdAt)
		if err != nil {
			t.Fatalf("read stored token: %v", err)
		}
		if got := expiresAt.Sub(createdAt); got < resetTTL-time.Minute || got > resetTTL+time.Minute {
			t.Errorf("lifetime %v, want about %v", got, resetTTL)
		}
	})

	t.Run("issuing a second token supersedes the first", func(t *testing.T) {
		if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("second"), resetTTL); err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}

		var live int
		if err := raw.QueryRow(ctx,
			`SELECT count(*) FROM password_reset_tokens WHERE user_id = ? AND used_at IS NULL`,
			user.ID,
		).Scan(&live); err != nil {
			t.Fatalf("count live tokens: %v", err)
		}
		if live != 1 {
			t.Errorf("%d live tokens, want exactly 1", live)
		}

		_, outcome, err := store.ConsumePasswordReset(ctx, hashOf("first"), "irrelevant-hash")
		if err != nil {
			t.Fatalf("ConsumePasswordReset: %v", err)
		}
		if outcome != storage.ResetOutcomeUsed {
			t.Errorf("superseded token = %s, want used", outcome)
		}
	})
}

func TestConsumePasswordResetIntegration(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()

	t.Run("unknown token", func(t *testing.T) {
		_, outcome, err := store.ConsumePasswordReset(ctx, hashOf("never-issued"), "hash")
		if err != nil {
			t.Fatalf("ConsumePasswordReset: %v", err)
		}
		if outcome != storage.ResetOutcomeUnknown {
			t.Errorf("outcome = %s, want unknown", outcome)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("expiree"))
		if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("stale"), -time.Minute); err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}

		_, outcome, err := store.ConsumePasswordReset(ctx, hashOf("stale"), "hash")
		if err != nil {
			t.Fatalf("ConsumePasswordReset: %v", err)
		}
		if outcome != storage.ResetOutcomeExpired {
			t.Errorf("outcome = %s, want expired", outcome)
		}
	})

	t.Run("a live token applies the whole reset", func(t *testing.T) {
		nu := newUser("resetter")
		nu.MustChangePassword = true
		user := mustCreateUser(ctx, t, store, nu)

		// Two live session families, a second outstanding reset token, and
		// a pending two-step challenge: everything the reset must sweep.
		first := mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-A", "ref-A"))
		second := mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-B", "ref-B"))
		if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("live"), resetTTL); err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}
		insertRawResetToken(ctx, t, raw, user.ID, hashOf("sibling"), resetTTL)
		insertChallenge(ctx, t, raw, user.ID, hashOf("challenge"))

		gotUser, outcome, err := store.ConsumePasswordReset(ctx, hashOf("live"), "new-argon2-hash")
		if err != nil {
			t.Fatalf("ConsumePasswordReset: %v", err)
		}
		if outcome != storage.ResetOutcomeApplied {
			t.Fatalf("outcome = %s, want applied", outcome)
		}
		if gotUser != user.ID {
			t.Errorf("returned user %s, want %s", gotUser, user.ID)
		}

		stored, err := store.UserByID(ctx, user.ID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if stored.PasswordHash != "new-argon2-hash" {
			t.Errorf("password hash %q, want the new one", stored.PasswordHash)
		}
		if stored.MustChangePassword {
			t.Error("must_change_password is still set")
		}

		for _, sess := range []storage.Session{first, second} {
			// "every generation is revoked", counted rather than folded with
			// bool_and(), which is PostgreSQL's alone. count(revoked_at)
			// counts the non-null ones, and the total guards the vacuous pass
			// an empty family would otherwise give.
			var generations, revoked int
			if err := raw.QueryRow(ctx,
				`SELECT count(*), count(revoked_at) FROM sessions WHERE family_id = ?`,
				sess.FamilyID,
			).Scan(&generations, &revoked); err != nil {
				t.Fatalf("read family state: %v", err)
			}
			if generations == 0 || revoked != generations {
				t.Errorf("family %s survived the reset: %d of %d generations revoked",
					sess.FamilyID, revoked, generations)
			}
		}

		var liveTokens int
		if err := raw.QueryRow(ctx,
			`SELECT count(*) FROM password_reset_tokens WHERE user_id = ? AND used_at IS NULL`,
			user.ID,
		).Scan(&liveTokens); err != nil {
			t.Fatalf("count live tokens: %v", err)
		}
		if liveTokens != 0 {
			t.Errorf("%d reset tokens still live, want 0", liveTokens)
		}

		var pendingChallenges int
		if err := raw.QueryRow(ctx,
			`SELECT count(*) FROM totp_challenges WHERE user_id = ? AND consumed_at IS NULL`,
			user.ID,
		).Scan(&pendingChallenges); err != nil {
			t.Fatalf("count pending challenges: %v", err)
		}
		if pendingChallenges != 0 {
			t.Errorf("%d two-step challenges still pending, want 0", pendingChallenges)
		}

		t.Run("replaying the same token", func(t *testing.T) {
			_, replayOutcome, replayErr := store.ConsumePasswordReset(ctx, hashOf("live"), "another-hash")
			if replayErr != nil {
				t.Fatalf("ConsumePasswordReset: %v", replayErr)
			}
			if replayOutcome != storage.ResetOutcomeUsed {
				t.Errorf("outcome = %s, want used", replayOutcome)
			}
			after, err := store.UserByID(ctx, user.ID)
			if err != nil {
				t.Fatalf("UserByID: %v", err)
			}
			if after.PasswordHash != "new-argon2-hash" {
				t.Error("a replayed token changed the password again")
			}
		})
	})

	t.Run("two-step verification survives the reset", func(t *testing.T) {
		user := mustCreateUser(ctx, t, store, newUser("twofactor"))
		insertActiveTOTP(ctx, t, raw, user.ID)
		if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("2fa-live"), resetTTL); err != nil {
			t.Fatalf("CreatePasswordResetToken: %v", err)
		}

		if _, outcome, err := store.ConsumePasswordReset(ctx, hashOf("2fa-live"), "hash"); err != nil {
			t.Fatalf("ConsumePasswordReset: %v", err)
		} else if outcome != storage.ResetOutcomeApplied {
			t.Fatalf("outcome = %s, want applied", outcome)
		}

		var stillActive bool
		if err := raw.QueryRow(ctx,
			`SELECT activated_at IS NOT NULL FROM user_totp WHERE user_id = ?`, user.ID,
		).Scan(&stillActive); err != nil {
			t.Fatalf("read TOTP state: %v", err)
		}
		if !stillActive {
			t.Error("the reset disabled two-step verification; control of a mailbox must not")
		}
	})
}

// TestConsumePasswordResetHasOneWinnerIntegration is the FOR UPDATE proof:
// however many callers race on one link, exactly one reset is applied.
func TestConsumePasswordResetHasOneWinnerIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("racer"))
	if err := store.CreatePasswordResetToken(ctx, user.ID, hashOf("contended"), resetTTL); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	const racers = 8
	outcomes := make([]storage.ResetOutcome, racers)
	errs := make([]error, racers)
	start := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			<-start
			_, outcomes[i], errs[i] = store.ConsumePasswordReset(ctx, hashOf("contended"), "hash-from-racer")
		}()
	}
	close(start)
	wg.Wait()

	applied := 0
	for i, outcome := range outcomes {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if outcome == storage.ResetOutcomeApplied {
			applied++
			continue
		}
		if outcome != storage.ResetOutcomeUsed {
			t.Errorf("racer %d saw %s, want applied or used", i, outcome)
		}
	}
	if applied != 1 {
		t.Errorf("%d racers applied the reset, want exactly 1", applied)
	}
}

func TestResetOutcomeString(t *testing.T) {
	t.Parallel()

	tests := map[storage.ResetOutcome]string{
		storage.ResetOutcomeUnknown: "unknown",
		storage.ResetOutcomeUsed:    "used",
		storage.ResetOutcomeExpired: "expired",
		storage.ResetOutcomeApplied: "applied",
		storage.ResetOutcome(99):    "invalid",
	}
	for outcome, want := range tests {
		if got := outcome.String(); got != want {
			t.Errorf("ResetOutcome(%d).String() = %q, want %q", int(outcome), got, want)
		}
	}
}

// insertRawResetToken adds a second outstanding token without going through
// CreatePasswordResetToken, which would supersede the first.
func insertRawResetToken(ctx context.Context, t *testing.T, raw *testdb.Raw, userID uuid.UUID, hash []byte, ttl time.Duration) {
	t.Helper()

	now := time.Now().UTC()
	raw.Exec(ctx, t,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uuid.New(), userID, hash, now.Add(ttl), now)
}

// insertChallenge adds a pending two-step challenge, the state a
// half-finished sign-in leaves behind.
func insertChallenge(ctx context.Context, t *testing.T, raw *testdb.Raw, userID uuid.UUID, hash []byte) {
	t.Helper()

	now := time.Now().UTC()
	raw.Exec(ctx, t,
		`INSERT INTO totp_challenges (id, user_id, token_hash, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uuid.New(), userID, hash, now.Add(5*time.Minute), now)
}

// insertActiveTOTP switches two-step verification on for a user.
func insertActiveTOTP(ctx context.Context, t *testing.T, raw *testdb.Raw, userID uuid.UUID) {
	t.Helper()

	now := time.Now().UTC()
	raw.Exec(ctx, t,
		`INSERT INTO user_totp (user_id, secret, verified_at, activated_at, setup_expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, make([]byte, 20), now, now, now.Add(10*time.Minute), now)
}
