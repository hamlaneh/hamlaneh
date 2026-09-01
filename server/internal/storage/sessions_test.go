package storage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// hashOf builds a deterministic 32-byte token hash for tests.
func hashOf(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// tokensFor builds SessionTokens with normal TTLs.
func tokensFor(access, refresh string) storage.SessionTokens {
	return storage.SessionTokens{
		AccessTokenHash:  hashOf(access),
		RefreshTokenHash: hashOf(refresh),
		AccessTTL:        15 * time.Minute,
		RefreshTTL:       30 * 24 * time.Hour,
		UserAgent:        "test-agent",
	}
}

func mustCreateSession(ctx context.Context, t *testing.T, store testdb.Store, userID uuid.UUID, tokens storage.SessionTokens) storage.Session {
	t.Helper()
	sess, err := store.CreateSession(ctx, storage.NewSession{UserID: userID, SessionTokens: tokens})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess
}

func TestSessionsIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("sessionuser"))

	t.Run("create populates family and expiries", func(t *testing.T) {
		sess := mustCreateSession(ctx, t, store, user.ID, tokensFor("a1", "r1"))
		if sess.FamilyID == uuid.Nil {
			t.Error("no family assigned")
		}
		if sess.UserID != user.ID {
			t.Errorf("user id %s, want %s", sess.UserID, user.ID)
		}
		if !sess.AccessExpiresAt.Before(sess.RefreshExpiresAt) {
			t.Error("access expiry is not before refresh expiry")
		}
		if !sess.AccessExpiresAt.After(sess.CreatedAt) {
			t.Error("access token created already expired")
		}
	})

	t.Run("access hash authenticates and joins the user", func(t *testing.T) {
		mustCreateSession(ctx, t, store, user.ID, tokensFor("a2", "r2"))

		sess, u, err := store.SessionUserByAccessHash(ctx, hashOf("a2"))
		if err != nil {
			t.Fatalf("SessionUserByAccessHash: %v", err)
		}
		if u.ID != user.ID || u.Username != "sessionuser" {
			t.Errorf("joined user %q (%s), want sessionuser", u.Username, u.ID)
		}
		if sess.UserID != user.ID {
			t.Errorf("session user %s, want %s", sess.UserID, user.ID)
		}
	})

	// Regression: this query used to carry its own copy of the user
	// projection and its own scan list, and the copy drifted twice. It
	// missed migration 0014's COALESCE — a 500 on EVERY authenticated
	// request by an account with no password credential, which is the whole
	// API for every account a directory had provisioned — and it had lost
	// the two SCIM columns, so the user on every authenticated principal
	// came back looking un-managed. Both halves are asserted here because
	// the second was invisible: nothing read those fields off a principal,
	// and the drift would have waited for the first caller that did.
	t.Run("the joined user is a whole user", func(t *testing.T) {
		externalID := "ext-passwordless"
		email := "passwordless@corp.example"
		passwordless, err := store.CreateScimUser(ctx, storage.NewScimUser{
			Username: "passwordless", ScimUserName: email, ExternalID: &externalID,
			Email: &email, Locale: "en", IsActive: true,
		})
		if err != nil {
			t.Fatalf("CreateScimUser: %v", err)
		}
		mustCreateSession(ctx, t, store, passwordless.ID, tokensFor("a2b", "r2b"))

		_, u, err := store.SessionUserByAccessHash(ctx, hashOf("a2b"))
		if err != nil {
			t.Fatalf("SessionUserByAccessHash for a password-less account: %v", err)
		}
		if u.ID != passwordless.ID || u.PasswordHash != "" {
			t.Errorf("joined user (%s, hash %q), want (%s, empty)", u.ID, u.PasswordHash, passwordless.ID)
		}
		if u.ScimExternalID == nil || *u.ScimExternalID != externalID {
			t.Errorf("joined scim_external_id = %v, want %q — a directory-managed account must not read as un-managed",
				u.ScimExternalID, externalID)
		}
		if u.ScimUserName == nil || *u.ScimUserName != email {
			t.Errorf("joined scim_user_name = %v, want %q", u.ScimUserName, email)
		}
	})

	t.Run("unknown access hash is ErrNotFound", func(t *testing.T) {
		_, _, err := store.SessionUserByAccessHash(ctx, hashOf("never-issued"))
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("expired access token is rejected", func(t *testing.T) {
		tokens := tokensFor("a3", "r3")
		tokens.AccessTTL = -time.Second // expired at birth, judged by the DB clock
		mustCreateSession(ctx, t, store, user.ID, tokens)

		_, _, err := store.SessionUserByAccessHash(ctx, hashOf("a3"))
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for expired access token", err)
		}
	})

	t.Run("revoked family is rejected", func(t *testing.T) {
		sess := mustCreateSession(ctx, t, store, user.ID, tokensFor("a4", "r4"))
		if err := store.RevokeFamily(ctx, sess.FamilyID); err != nil {
			t.Fatalf("RevokeFamily: %v", err)
		}
		_, _, err := store.SessionUserByAccessHash(ctx, hashOf("a4"))
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound for revoked session", err)
		}
	})
}

func TestRotateSessionIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("rotator"))

	t.Run("rotation retires the old tokens and issues the next generation", func(t *testing.T) {
		first := mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-1", "ref-1"))

		next, outcome, err := store.RotateSession(ctx, hashOf("ref-1"), tokensFor("acc-2", "ref-2"))
		if err != nil {
			t.Fatalf("RotateSession: %v", err)
		}
		if outcome != storage.RotateOutcomeRotated {
			t.Fatalf("outcome = %v, want rotated", outcome)
		}
		if next.FamilyID != first.FamilyID {
			t.Error("rotation changed the family")
		}
		if next.UserID != user.ID {
			t.Error("rotation changed the user")
		}

		// The new access token works; the old one died with the rotation.
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("acc-2")); err != nil {
			t.Errorf("new access token rejected: %v", err)
		}
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("acc-1")); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("old access token still valid after rotation: %v", err)
		}
	})

	t.Run("reusing a rotated refresh token revokes the whole family", func(t *testing.T) {
		mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-A", "ref-A"))

		if _, outcome, err := store.RotateSession(ctx, hashOf("ref-A"), tokensFor("acc-B", "ref-B")); err != nil || outcome != storage.RotateOutcomeRotated {
			t.Fatalf("first rotation: outcome %v, err %v", outcome, err)
		}

		// Replay of the used token: reuse detection.
		_, outcome, err := store.RotateSession(ctx, hashOf("ref-A"), tokensFor("acc-X", "ref-X"))
		if err != nil {
			t.Fatalf("replay rotation: %v", err)
		}
		if outcome != storage.RotateOutcomeReuseDetected {
			t.Fatalf("outcome = %v, want reuse detected", outcome)
		}

		// The whole family is dead: the current generation's access token
		// fails, and its refresh token no longer rotates.
		if _, _, accessErr := store.SessionUserByAccessHash(ctx, hashOf("acc-B")); !errors.Is(accessErr, storage.ErrNotFound) {
			t.Errorf("family member access token survived reuse detection: %v", accessErr)
		}
		_, outcome, err = store.RotateSession(ctx, hashOf("ref-B"), tokensFor("acc-Y", "ref-Y"))
		if err != nil {
			t.Fatalf("post-revocation rotation: %v", err)
		}
		if outcome != storage.RotateOutcomeInvalid {
			t.Errorf("revoked family refresh rotated: outcome %v, want invalid", outcome)
		}
	})

	t.Run("unknown refresh token is invalid", func(t *testing.T) {
		_, outcome, err := store.RotateSession(ctx, hashOf("ref-never-issued"), tokensFor("acc-Z", "ref-Z"))
		if err != nil {
			t.Fatalf("RotateSession: %v", err)
		}
		if outcome != storage.RotateOutcomeInvalid {
			t.Errorf("outcome = %v, want invalid", outcome)
		}
	})

	t.Run("expired refresh token is invalid and does not rotate", func(t *testing.T) {
		tokens := tokensFor("acc-old", "ref-old")
		tokens.RefreshTTL = -time.Second
		mustCreateSession(ctx, t, store, user.ID, tokens)

		_, outcome, err := store.RotateSession(ctx, hashOf("ref-old"), tokensFor("acc-new", "ref-new"))
		if err != nil {
			t.Fatalf("RotateSession: %v", err)
		}
		if outcome != storage.RotateOutcomeInvalid {
			t.Errorf("outcome = %v, want invalid for expired refresh token", outcome)
		}
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("acc-new")); !errors.Is(err, storage.ErrNotFound) {
			t.Error("an expired refresh token minted a new session")
		}
	})
}

// TestUpdatePasswordRevokesOtherFamiliesIntegration pins the change-password
// storage contract: the new hash lands, the flag clears, every other family
// dies, and the performing session's family survives.
func TestUpdatePasswordRevokesOtherFamiliesIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	nu := newUser("changer")
	nu.MustChangePassword = true
	user := mustCreateUser(ctx, t, store, nu)

	current := mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-keep", "ref-keep"))
	mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-other1", "ref-other1"))
	mustCreateSession(ctx, t, store, user.ID, tokensFor("acc-other2", "ref-other2"))

	// An unrelated user's session must survive.
	bystander := mustCreateUser(ctx, t, store, newUser("bystander"))
	mustCreateSession(ctx, t, store, bystander.ID, tokensFor("acc-bystander", "ref-bystander"))

	if err := store.UpdatePassword(ctx, user.ID, "new-hash", current.FamilyID); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	got, err := store.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Errorf("hash = %q, want new-hash", got.PasswordHash)
	}
	if got.MustChangePassword {
		t.Error("must_change_password not cleared")
	}

	if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("acc-keep")); err != nil {
		t.Errorf("current session died: %v", err)
	}
	for _, token := range []string{"acc-other1", "acc-other2"} {
		if _, _, err := store.SessionUserByAccessHash(ctx, hashOf(token)); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("other session %s survived the password change: %v", token, err)
		}
	}
	if _, _, err := store.SessionUserByAccessHash(ctx, hashOf("acc-bystander")); err != nil {
		t.Errorf("bystander's session died: %v", err)
	}
}

// TestSessionLifetimeFromOrgSettingsIntegration pins that the org's
// session_lifetime_hours is read at every mint — the family's first
// generation and each rotation — clamped to the caller's RefreshTTL, which
// is the server's ceiling (ADR 004; the contract says a value above the
// ceiling is clamped rather than refused).
func TestSessionLifetimeFromOrgSettingsIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("lifetimeuser"))

	setLifetime := func(hours int) {
		t.Helper()
		if _, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{SessionLifetimeHours: &hours}); err != nil {
			t.Fatalf("set session_lifetime_hours=%d: %v", hours, err)
		}
	}
	// refreshWindow is the minted refresh window measured by the database's
	// own clock: both timestamps come from now() in the insert statement.
	refreshWindow := func(sess storage.Session) time.Duration {
		return sess.RefreshExpiresAt.Sub(sess.CreatedAt)
	}
	wantWindow := func(sess storage.Session, want time.Duration) {
		t.Helper()
		if got := refreshWindow(sess); got < want-time.Minute || got > want+time.Minute {
			t.Errorf("refresh window %s, want about %s", got, want)
		}
	}

	// Shorter than the ceiling: the org value wins, at mint and at rotation.
	setLifetime(2)
	sess := mustCreateSession(ctx, t, store, user.ID, tokensFor("lt-a1", "lt-r1"))
	wantWindow(sess, 2*time.Hour)

	next, outcome, err := store.RotateSession(ctx, hashOf("lt-r1"), tokensFor("lt-a2", "lt-r2"))
	if err != nil || outcome != storage.RotateOutcomeRotated {
		t.Fatalf("rotate: outcome %v, err %v", outcome, err)
	}
	wantWindow(next, 2*time.Hour)

	// Above the ceiling: the CHECK constraint's maximum is 8760 hours, a
	// year; the caller's 30-day RefreshTTL clamps it.
	setLifetime(8760)
	sess = mustCreateSession(ctx, t, store, user.ID, tokensFor("lt-a3", "lt-r3"))
	wantWindow(sess, 30*24*time.Hour)
}

// TestSessionTotpFlagAtMintIntegration pins where the enrolment flag is
// decided — the mint itself — and that a rotation carries it verbatim
// rather than recomputing the policy.
func TestSessionTotpFlagAtMintIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := mustCreateUser(ctx, t, store, newUser("flaguser"))

	setPolicy := func(on bool) {
		t.Helper()
		if _, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{RequireTotp: &on}); err != nil {
			t.Fatalf("set require_totp=%v: %v", on, err)
		}
	}

	// Policy off: unflagged.
	sess := mustCreateSession(ctx, t, store, user.ID, tokensFor("fl-a1", "fl-r1"))
	if sess.TotpEnrollmentRequired {
		t.Error("mint with the policy off flagged the session")
	}

	// Policy on, no activated second factor: flagged — and the flag comes
	// back on the authentication read, which is what the middleware acts on.
	setPolicy(true)
	sess = mustCreateSession(ctx, t, store, user.ID, tokensFor("fl-a2", "fl-r2"))
	if !sess.TotpEnrollmentRequired {
		t.Fatal("mint with the policy on did not flag the session")
	}
	authSess, _, err := store.SessionUserByAccessHash(ctx, hashOf("fl-a2"))
	if err != nil {
		t.Fatalf("authenticate flagged session: %v", err)
	}
	if !authSess.TotpEnrollmentRequired {
		t.Error("authentication read lost the flag")
	}

	// A rotation carries the flag; flipping the policy off first changes
	// nothing, because a rotation is not a sign-in and does not re-read it.
	setPolicy(false)
	next, outcome, err := store.RotateSession(ctx, hashOf("fl-r2"), tokensFor("fl-a3", "fl-r3"))
	if err != nil || outcome != storage.RotateOutcomeRotated {
		t.Fatalf("rotate: outcome %v, err %v", outcome, err)
	}
	if !next.TotpEnrollmentRequired {
		t.Error("rotation laundered the enrolment flag away")
	}
}
