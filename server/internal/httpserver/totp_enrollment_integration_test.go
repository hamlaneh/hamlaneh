package httpserver_test

// The org's require_totp policy, enforced per session at mint (ADR 004).
// These tests walk the whole promise through the real stack: the flag is
// decided when a session is minted, so flipping the policy strands nobody
// already signed in; a flagged session reaches only the enrolment surface;
// activating TOTP unblocks every session of the account at once; and
// sign-ins land in the audit log with the method that finished them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// requireTotp turns the org's two-step policy on, through the same storage
// call the admin endpoint uses.
func requireTotp(ctx context.Context, t *testing.T, store testdb.Store) {
	t.Helper()
	on := true
	if _, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{RequireTotp: &on}); err != nil {
		t.Fatalf("set require_totp: %v", err)
	}
}

// loginWithBody is login plus the response body: the tests here assert what
// the sign-in SAYS about the session it minted, not just that it minted one.
func loginWithBody(t *testing.T, handler http.Handler, identifier string) (sessionCookies, api.User) {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody(identifier, totpFixturePassword)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s: got status %d (body %s)", identifier, rec.Code, rec.Body.String())
	}
	var user api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("login body is not the User shape: %v", err)
	}
	cookies := responseCookies(rec)
	return sessionCookies{
		access:  cookieByName(t, cookies, session.AccessCookie).Value,
		refresh: cookieByName(t, cookies, session.RefreshCookie).Value,
		csrf:    cookieByName(t, cookies, session.CSRFCookie).Value,
	}, user
}

// channelsStatus asks a representative gated endpoint and returns the
// recorder: 200 is a session the gate admits, 403 one it refuses.
func channelsStatus(t *testing.T, handler http.Handler, sc sessionCookies) *httptest.ResponseRecorder {
	t.Helper()
	return doHandler(t, handler, request(http.MethodGet, "/api/v1/channels", "", withSession(sc)))
}

// currentUser reads GET /users/me and decodes the body.
func currentUser(t *testing.T, handler http.Handler, sc sessionCookies) api.User {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/users/me", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("users/me: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	var user api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("users/me body is not the User shape: %v", err)
	}
	return user
}

// refreshed rotates the session and returns the new cookie set.
func refreshed(t *testing.T, handler http.Handler, sc sessionCookies) sessionCookies {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "", withSession(sc)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	next := sc
	next.access = cookieByName(t, responseCookies(rec), session.AccessCookie).Value
	next.refresh = cookieByName(t, responseCookies(rec), session.RefreshCookie).Value
	return next
}

// TestTotpEnrollmentGateIntegration signs in under an enforced policy and
// walks the flagged session across the boundary: the allowed surface, the
// refused surface, the WebSocket refusal, and the way out — enrolment
// unblocking the very session that ran it.
func TestTotpEnrollmentGateIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "gated2fa", PasswordHash: password.Hash(totpFixturePassword), Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	requireTotp(ctx, t, store)

	sc, user := loginWithBody(t, handler, "gated2fa")
	if !user.TotpEnrollmentRequired {
		t.Fatal("login under require_totp did not flag the session")
	}

	// The refused surface, one representative per class: messaging, the
	// self-service surface outside enrolment, and the WebSocket upgrade —
	// which must be refused by the gate, so no socket ever opens.
	wantError(t, channelsStatus(t, handler, sc), http.StatusForbidden, "totp_enrollment_required")
	wantError(t, doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/disable",
		`{"password":"`+totpFixturePassword+`"}`, withSession(sc))),
		http.StatusForbidden, "totp_enrollment_required")
	wantError(t, doHandler(t, handler, request(http.MethodGet, "/api/v1/ws", "", withSession(sc))),
		http.StatusForbidden, "totp_enrollment_required")

	// The allowed surface: reading and patching users/me.
	if got := currentUser(t, handler, sc); !got.TotpEnrollmentRequired {
		t.Error("users/me does not report the flag")
	}
	if rec := doHandler(t, handler, request(http.MethodPatch, "/api/v1/users/me",
		`{"locale":"fa"}`, withSession(sc))); rec.Code != http.StatusOK {
		t.Errorf("patch users/me under the gate: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The way out: the enrolment endpoints are reachable, and activation
	// unblocks the session that ran them — no re-login.
	acct := twoStepAccount{cookies: sc}
	enableTwoStep(t, handler, &acct)

	if rec := channelsStatus(t, handler, sc); rec.Code != http.StatusOK {
		t.Errorf("channels after enrolment: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := currentUser(t, handler, sc); got.TotpEnrollmentRequired {
		t.Error("users/me still reports the flag after enrolment")
	}
}

// TestTotpPolicyFlipSparesOpenSessionsIntegration is the contract's core
// promise: the flag binds at the next sign-in, never mid-session. A session
// open when the admin turns the policy on keeps working — through refresh
// rotations too — while the next sign-in is flagged.
func TestTotpPolicyFlipSparesOpenSessionsIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "spared2fa", PasswordHash: password.Hash(totpFixturePassword), Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	// Sign in while the policy is off, then flip it on.
	open, user := loginWithBody(t, handler, "spared2fa")
	if user.TotpEnrollmentRequired {
		t.Fatal("login with the policy off flagged the session")
	}
	requireTotp(ctx, t, store)

	// The open session survives the flip — that is what per-session-at-mint
	// buys, and it is what keeps the admin who flipped the switch signed in.
	if rec := channelsStatus(t, handler, open); rec.Code != http.StatusOK {
		t.Errorf("open session after policy flip: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// A rotation is not a sign-in: the rotated generation must not pick the
	// policy up either.
	rotated := refreshed(t, handler, open)
	if rec := channelsStatus(t, handler, rotated); rec.Code != http.StatusOK {
		t.Errorf("rotated session after policy flip: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The next sign-in is where the policy binds.
	next, nextUser := loginWithBody(t, handler, "spared2fa")
	if !nextUser.TotpEnrollmentRequired {
		t.Error("the next sign-in after the flip is not flagged")
	}
	wantError(t, channelsStatus(t, handler, next), http.StatusForbidden, "totp_enrollment_required")
}

// TestFlaggedSessionRefreshKeepsFlagIntegration pins the other direction of
// "a rotation is not a sign-in": refreshing must not launder the flag away.
func TestFlaggedSessionRefreshKeepsFlagIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "laundry2fa", PasswordHash: password.Hash(totpFixturePassword), Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	requireTotp(ctx, t, store)

	sc, _ := loginWithBody(t, handler, "laundry2fa")
	rotated := refreshed(t, handler, sc)

	wantError(t, channelsStatus(t, handler, rotated), http.StatusForbidden, "totp_enrollment_required")
	if got := currentUser(t, handler, rotated); !got.TotpEnrollmentRequired {
		t.Error("rotated session lost the flag")
	}
}

// TestActivateTotpUnblocksOtherSessionsIntegration: a person signed in on
// two devices under the policy finishes enrolment on one, and the other is
// unblocked without touching it — the clear covers every session of the
// user, not just the caller's (ADR 004).
func TestActivateTotpUnblocksOtherSessionsIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "devices2fa", PasswordHash: password.Hash(totpFixturePassword), Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	requireTotp(ctx, t, store)

	deviceA, _ := loginWithBody(t, handler, "devices2fa")
	deviceB, _ := loginWithBody(t, handler, "devices2fa")
	wantError(t, channelsStatus(t, handler, deviceB), http.StatusForbidden, "totp_enrollment_required")

	acct := twoStepAccount{cookies: deviceA}
	enableTwoStep(t, handler, &acct)

	// Device B did nothing — no refresh, no re-login — and is unblocked.
	if rec := channelsStatus(t, handler, deviceB); rec.Code != http.StatusOK {
		t.Errorf("other device after enrolment: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := currentUser(t, handler, deviceB); got.TotpEnrollmentRequired {
		t.Error("other device still reports the flag after enrolment")
	}
}

// TestSignInAuditIntegration pins user.signed_in at every session-minting
// site, with the method that finished the sign-in — and pins that a refresh
// rotation mints NO sign-in entry, because it presents no credential.
func TestSignInAuditIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	rec := recordingAudit{}
	handler := httpserver.Handler(store, httpserver.WithAudit(&rec))

	user, err := store.CreateUser(ctx, storage.NewUser{
		Username: "audited2fa", PasswordHash: password.Hash(totpFixturePassword), Locale: "en",
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	signIns := func() []httpserver.AuditEvent {
		out := []httpserver.AuditEvent{}
		for _, ev := range rec.snapshot() {
			if ev.Action == "user.signed_in" {
				out = append(out, ev)
			}
		}
		return out
	}

	// A password sign-in records one entry, attributed to the user.
	sc, _ := loginWithBody(t, handler, "audited2fa")
	events := signIns()
	if len(events) != 1 {
		t.Fatalf("after one login: %d user.signed_in events, want 1", len(events))
	}
	if events[0].ActorID != user.ID || events[0].TargetID != user.ID {
		t.Errorf("signed_in actor=%s target=%s, want both %s", events[0].ActorID, events[0].TargetID, user.ID)
	}
	if got := events[0].Detail["method"]; got != "password" {
		t.Errorf("signed_in method %v, want password", got)
	}

	// A refresh rotation is not a sign-in.
	sc = refreshed(t, handler, sc)
	if got := len(signIns()); got != 1 {
		t.Errorf("after a refresh: %d user.signed_in events, want still 1", got)
	}

	// A completed two-step sign-in records the method that finished it, and
	// an enrolled account signing in under the policy is not flagged.
	acct := twoStepAccount{user: user, cookies: sc}
	enableTwoStep(t, handler, &acct)
	requireTotp(ctx, t, store)

	challenge := passwordLogin(t, handler, "audited2fa", "203.0.113.80:4000")
	if challenge.Code != http.StatusAccepted {
		t.Fatalf("password half: got %d, want 202 (body %s)", challenge.Code, challenge.Body.String())
	}
	if got := len(signIns()); got != 1 {
		t.Errorf("a 202 challenge recorded a sign-in; the sign-in is not complete yet")
	}
	token := cookieByName(t, responseCookies(challenge), challengeCookieName).Value
	done := completeLogin(t, handler, token, nextCode(t, acct.secret))
	if done.Code != http.StatusOK {
		t.Fatalf("code half: got %d, want 200 (body %s)", done.Code, done.Body.String())
	}
	var totpUser api.User
	if err := json.Unmarshal(done.Body.Bytes(), &totpUser); err != nil {
		t.Fatalf("totp login body: %v", err)
	}
	if totpUser.TotpEnrollmentRequired {
		t.Error("an enrolled account's two-step sign-in was flagged")
	}
	events = signIns()
	if len(events) != 2 {
		t.Fatalf("after the two-step sign-in: %d user.signed_in events, want 2", len(events))
	}
	if got := events[1].Detail["method"]; got != "totp" {
		t.Errorf("two-step signed_in method %v, want totp", got)
	}

	// A recovery-code sign-in names its own method.
	challenge = passwordLogin(t, handler, "audited2fa", "203.0.113.81:4000")
	token = cookieByName(t, responseCookies(challenge), challengeCookieName).Value
	done = completeLogin(t, handler, token, acct.codes[0])
	if done.Code != http.StatusOK {
		t.Fatalf("recovery half: got %d, want 200 (body %s)", done.Code, done.Body.String())
	}
	events = signIns()
	if len(events) != 3 {
		t.Fatalf("after the recovery sign-in: %d user.signed_in events, want 3", len(events))
	}
	if got := events[2].Detail["method"]; got != "recovery_code" {
		t.Errorf("recovery signed_in method %v, want recovery_code", got)
	}
}
