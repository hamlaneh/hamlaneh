package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

const (
	resetBaseURL  = "https://chat.example.com"
	resetMailWait = 5 * time.Second
	// resetTestPassword satisfies the account password policy.
	resetTestPassword = "a perfectly fine passphrase"
)

// resetFakeStore is the passwordreset.Store a handler test needs when no
// database is involved.
type resetFakeStore struct {
	user       storage.User
	userErr    error
	createErr  error
	outcome    storage.ResetOutcome
	consumeErr error
}

// UserByEmail answers with the wired user, the wired error, or — when the
// test wired neither — errFakeUnwired, following fakeStore's rule.
//
// The zero storage.User is not an option here: returning it with a nil
// error says "an account exists, with a nil id and no address", and these
// are the enumeration tests, whose whole subject is which addresses exist.
// A test that silently built on that lie would still pass while proving
// nothing.
func (f *resetFakeStore) UserByEmail(context.Context, string) (storage.User, error) {
	if f.userErr != nil {
		return storage.User{}, f.userErr
	}
	if f.user.ID == uuid.Nil {
		return storage.User{}, errFakeUnwired
	}
	return f.user, nil
}

func (f *resetFakeStore) CreatePasswordResetToken(context.Context, uuid.UUID, []byte, time.Duration) error {
	return f.createErr
}

func (f *resetFakeStore) ConsumePasswordReset(context.Context, []byte, string) (uuid.UUID, storage.ResetOutcome, error) {
	if f.consumeErr != nil {
		return uuid.Nil, storage.ResetOutcomeUnknown, f.consumeErr
	}
	return uuid.New(), f.outcome, nil
}

// newResetService builds a service over store for one test, returning it
// alongside the mailer that records what it dispatches. Callers install it
// with WithPasswordReset; the handlers reach it as apiServer.reset.
func newResetService(t *testing.T, store passwordreset.Store, cfg passwordreset.Config) (*passwordreset.Service, *mailer.Recorder) {
	t.Helper()

	var recorder mailer.Recorder
	svc, err := passwordreset.New(store, &recorder, cfg)
	if err != nil {
		t.Fatalf("build reset service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, &recorder
}

// resetHandler is the common case: a handler whose only wiring is the reset
// service. The reset endpoints and the instance document never touch Store —
// the service owns their persistence — so these tests need none.
func resetHandler(t *testing.T, store passwordreset.Store, cfg passwordreset.Config) (http.Handler, *mailer.Recorder) {
	t.Helper()

	svc, recorder := newResetService(t, store, cfg)
	return httpserver.Handler(nil, httpserver.WithPasswordReset(svc)), recorder
}

// deliverableConfig is the configuration of an instance that can actually
// send mail.
func deliverableConfig() passwordreset.Config {
	return passwordreset.Config{BaseURL: resetBaseURL, Deliverable: true}
}

func TestGetInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		build         func(t *testing.T) http.Handler
		wantAvailable bool
	}{
		{
			name:          "no service configured",
			build:         func(*testing.T) http.Handler { return httpserver.Handler(nil) },
			wantAvailable: false,
		},
		{
			name: "null mailer, no public URL",
			build: func(t *testing.T) http.Handler {
				transport, err := mailer.New(mailer.Config{}, passwordreset.TokenTTL)
				if err != nil {
					t.Fatalf("mailer.New: %v", err)
				}
				svc, err := passwordreset.New(&resetFakeStore{}, transport, passwordreset.Config{})
				if err != nil {
					t.Fatalf("build reset service: %v", err)
				}
				t.Cleanup(svc.Close)
				return httpserver.Handler(nil, httpserver.WithPasswordReset(svc))
			},
			wantAvailable: false,
		},
		{
			name: "transport and public URL configured",
			build: func(t *testing.T) http.Handler {
				handler, _ := resetHandler(t, &resetFakeStore{}, deliverableConfig())
				return handler
			},
			wantAvailable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := doHandler(t, tc.build(t), request(http.MethodGet, "/api/v1/instance", ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d (body %s), want 200", rec.Code, rec.Body.String())
			}

			var got api.InstanceInfo
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rec.Body.String(), err)
			}
			if got.PasswordMinLength != uservalidate.MinPasswordLen {
				t.Errorf("password_min_length = %d, want the policy's %d",
					got.PasswordMinLength, uservalidate.MinPasswordLen)
			}
			if got.PasswordResetAvailable != tc.wantAvailable {
				t.Errorf("password_reset_available = %v, want %v",
					got.PasswordResetAvailable, tc.wantAvailable)
			}
		})
	}
}

// TestInstanceReportsResetAvailabilityFromTheEnvironment walks the wiring
// main.go builds — environment to FromEnv to WithPasswordReset — and pins
// what the sign-in screen reads at the end of it. The screen omits the
// "Forgot password?" link on false, so this is the honesty check for the
// whole feature: no mail transport, no link.
func TestInstanceReportsResetAvailabilityFromTheEnvironment(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	clearSMTPEnv(t)

	assertAvailable := func(t *testing.T, want bool) {
		t.Helper()

		svc, err := passwordreset.FromEnv(&resetFakeStore{})
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		t.Cleanup(svc.Close)

		rec := doHandler(t, httpserver.Handler(nil, httpserver.WithPasswordReset(svc)), request(http.MethodGet, "/api/v1/instance", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d (body %s), want 200", rec.Code, rec.Body.String())
		}
		var got api.InstanceInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if got.PasswordResetAvailable != want {
			t.Errorf("password_reset_available = %v, want %v", got.PasswordResetAvailable, want)
		}
	}

	t.Run("no SMTP configured", func(t *testing.T) {
		clearSMTPEnv(t)
		assertAvailable(t, false)
	})

	t.Run("SMTP and a public URL configured", func(t *testing.T) {
		clearSMTPEnv(t)
		t.Setenv(mailer.EnvHost, "smtp.example.invalid")
		t.Setenv(mailer.EnvFrom, "hamlaneh@example.invalid")
		t.Setenv(passwordreset.EnvPublicURL, resetBaseURL)
		assertAvailable(t, true)
	})
}

// clearSMTPEnv unsets everything FromEnv reads, so a test's own settings are
// the only ones in play.
func clearSMTPEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		mailer.EnvHost, mailer.EnvPort, mailer.EnvUsername, mailer.EnvPassword,
		mailer.EnvFrom, mailer.EnvFromName, mailer.EnvEncryption,
		passwordreset.EnvPublicURL,
	} {
		t.Setenv(name, "")
	}
}

func TestRequestPasswordResetRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	handler, _ := resetHandler(t, &resetFakeStore{userErr: storage.ErrNotFound}, deliverableConfig())

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"not an object", `"someone@example.com"`},
		{"missing email", `{}`},
		{"empty email", `{"email":""}`},
		{"whitespace email", `{"email":"   "}`},
		{"oversized email", `{"email":"` + strings.Repeat("a", 321) + `"}`},
		{"header injection", `{"email":"a@example.com\r\nBcc: b@example.com"}`},
		{"trailing content", `{"email":"a@example.com"}{}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", tc.body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestRequestPasswordResetIsEnumerationSafe is the whole point of the
// endpoint: a known and an unknown address must be indistinguishable.
func TestRequestPasswordResetIsEnumerationSafe(t *testing.T) {
	t.Parallel()

	known := "owner@example.com"
	store := &resetFakeStore{user: storage.User{ID: uuid.New(), Email: &known, Locale: "en"}}
	handler, recorder := resetHandler(t, store, deliverableConfig())

	hit := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"`+known+`"}`, withRemoteAddr("203.0.113.1:54321")))
	if hit.Code != http.StatusAccepted {
		t.Fatalf("known address: status %d (body %s), want 202", hit.Code, hit.Body.String())
	}
	if hit.Body.Len() != 0 {
		t.Errorf("known address: body %q, want empty", hit.Body.String())
	}

	store.userErr = storage.ErrNotFound
	miss := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"nobody@example.com"}`, withRemoteAddr("203.0.113.2:54321")))

	assertIdentical(t, "known versus unknown address", hit, miss)

	sent := recorder.WaitFor(1, resetMailWait)
	if len(sent) != 1 {
		t.Fatalf("dispatched %d messages, want exactly the one for the known address", len(sent))
	}
	if sent[0].To != known {
		t.Errorf("mailed %q, want %q", sent[0].To, known)
	}
}

// TestRequestPasswordResetHidesInternalFailures pins the subtlest leak: a
// storage failure can only happen on the path where the address exists, so
// surfacing it would answer the question the endpoint refuses to answer.
func TestRequestPasswordResetHidesInternalFailures(t *testing.T) {
	t.Parallel()

	known := "owner@example.com"
	store := &resetFakeStore{
		user:      storage.User{ID: uuid.New(), Email: &known, Locale: "en"},
		createErr: errors.New("database is on fire"),
	}
	handler, _ := resetHandler(t, store, deliverableConfig())

	broken := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"`+known+`"}`, withRemoteAddr("203.0.113.3:54321")))
	if broken.Code != http.StatusAccepted {
		t.Fatalf("status %d (body %s), want 202", broken.Code, broken.Body.String())
	}

	store.userErr = storage.ErrNotFound
	miss := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"nobody@example.com"}`, withRemoteAddr("203.0.113.4:54321")))
	assertIdentical(t, "failing storage versus unknown address", broken, miss)
}

func TestRequestPasswordResetWithoutAServiceStillAnswers202(t *testing.T) {
	t.Parallel()

	handler := httpserver.Handler(nil)

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"owner@example.com"}`))
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q, want an empty 202", rec.Code, rec.Body.String())
	}
}

func TestRequestPasswordResetRateLimitsPerAddress(t *testing.T) {
	t.Parallel()

	handler, _ := resetHandler(t, &resetFakeStore{userErr: storage.ErrNotFound}, deliverableConfig())

	const address = "target@example.com"
	limited := false
	for attempt := range 20 {
		// A different client each time, so only the address budget can trip.
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"`+address+`"}`, withRemoteAddr("198.51.100."+strconv.Itoa(attempt%250+1)+":54321")))
		if rec.Code == http.StatusTooManyRequests {
			wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("repeated requests for one address were never rate limited")
	}

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"someone.else@example.com"}`, withRemoteAddr("198.51.100.99:54321")))
	if rec.Code != http.StatusAccepted {
		t.Errorf("a different address: status %d, want 202", rec.Code)
	}
}

func TestRequestPasswordResetRateLimitsPerClient(t *testing.T) {
	t.Parallel()

	handler, _ := resetHandler(t, &resetFakeStore{userErr: storage.ErrNotFound}, deliverableConfig())

	const client = "203.0.113.50:54321"
	limited := false
	for attempt := range 60 {
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"person`+strconv.Itoa(attempt)+`@example.com"}`, withRemoteAddr(client)))
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("one client asking about many addresses was never rate limited")
	}

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"fresh@example.com"}`, withRemoteAddr("203.0.113.51:54321")))
	if rec.Code != http.StatusAccepted {
		t.Errorf("a different client: status %d, want 202", rec.Code)
	}
}

func TestCompletePasswordResetRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	handler, _ := resetHandler(t, &resetFakeStore{outcome: storage.ResetOutcomeApplied}, deliverableConfig())

	const validToken = "0123456789abcdef0123456789abcdef012345678"

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing token", `{"new_password":"` + resetTestPassword + `"}`},
		{"short token", `{"token":"tooshort","new_password":"` + resetTestPassword + `"}`},
		{"long token", `{"token":"` + strings.Repeat("t", 129) + `","new_password":"` + resetTestPassword + `"}`},
		{"missing password", `{"token":"` + validToken + `"}`},
		{"short password", `{"token":"` + validToken + `","new_password":"short"}`},
		{"oversized password", `{"token":"` + validToken + `","new_password":"` + strings.Repeat("p", 1025) + `"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", tc.body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestCompletePasswordResetOutcomes(t *testing.T) {
	t.Parallel()

	const token = "0123456789abcdef0123456789abcdef012345678"
	body := `{"token":"` + token + `","new_password":"` + resetTestPassword + `"}`

	t.Run("applied", func(t *testing.T) {
		handler, _ := resetHandler(t, &resetFakeStore{outcome: storage.ResetOutcomeApplied}, deliverableConfig())

		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", body))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d (body %s), want 204", rec.Code, rec.Body.String())
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Error("the reset set cookies; the user must sign in fresh")
		}
	})

	// Unknown, expired and used must be one answer, byte for byte.
	var previous *httptest.ResponseRecorder
	for _, outcome := range []storage.ResetOutcome{
		storage.ResetOutcomeUnknown,
		storage.ResetOutcomeExpired,
		storage.ResetOutcomeUsed,
	} {
		t.Run(outcome.String(), func(t *testing.T) {
			handler, _ := resetHandler(t, &resetFakeStore{outcome: outcome}, deliverableConfig())

			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", body))
			wantError(t, rec, http.StatusUnauthorized, "invalid_reset_token")
			if previous != nil {
				assertIdentical(t, "rejected token outcomes", previous, rec)
			}
			previous = rec
		})
	}
}

func TestCompletePasswordResetWithoutAServiceRejectsTheToken(t *testing.T) {
	t.Parallel()

	handler := httpserver.Handler(nil)

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", `{"token":"0123456789abcdef0123456789abcdef012345678","new_password":"`+resetTestPassword+`"}`))
	wantError(t, rec, http.StatusUnauthorized, "invalid_reset_token")
}

func TestCompletePasswordResetSurfacesInternalFailures(t *testing.T) {
	t.Parallel()

	handler, _ := resetHandler(t, &resetFakeStore{consumeErr: errors.New("database is on fire")}, deliverableConfig())

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", `{"token":"0123456789abcdef0123456789abcdef012345678","new_password":"`+resetTestPassword+`"}`))
	wantError(t, rec, http.StatusInternalServerError, "internal_error")
}

// TestPasswordResetFlowIntegration walks the whole feature through the real
// router and a real database: request, mail, completion, session
// revocation, and every way a token can fail afterwards.
func TestPasswordResetFlowIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	svc, recorder := newResetService(t, store, deliverableConfig())
	handler := httpserver.Handler(store, httpserver.WithPasswordReset(svc))

	const (
		oldPassword = "the original passphrase"
		newPassword = "the replacement passphrase"
		address     = "resetflow@example.com"
	)
	email := address
	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "resetflow", Email: &email, PasswordHash: password.Hash(oldPassword), Locale: "fa",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	// A live session, so the reset has something to revoke.
	signedIn := login(t, handler, "resetflow", oldPassword)
	if code := me(t, handler, signedIn); code != http.StatusOK {
		t.Fatalf("the fixture session does not work: /users/me = %d", code)
	}

	// A request that matches, and one that does not.
	hit := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"`+strings.ToUpper(address)+`"}`, withRemoteAddr("203.0.113.60:54321")))
	miss := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"ghost@example.com"}`, withRemoteAddr("203.0.113.61:54321")))
	if hit.Code != http.StatusAccepted {
		t.Fatalf("reset request: status %d (body %s)", hit.Code, hit.Body.String())
	}
	assertIdentical(t, "known versus unknown address", hit, miss)

	sent := recorder.WaitFor(1, resetMailWait)
	if len(sent) != 1 {
		t.Fatalf("dispatched %d messages, want 1", len(sent))
	}
	if sent[0].To != address {
		t.Errorf("mailed %q, want %q", sent[0].To, address)
	}
	if sent[0].Locale != "fa" {
		t.Errorf("locale %q, want the account's fa", sent[0].Locale)
	}
	supersededToken := tokenFromURL(t, sent[0].ResetURL)

	// A second request supersedes the first link.
	second := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-request", `{"email":"`+address+`"}`, withRemoteAddr("203.0.113.62:54321")))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second reset request: status %d", second.Code)
	}
	sent = recorder.WaitFor(2, resetMailWait)
	if len(sent) != 2 {
		t.Fatalf("dispatched %d messages, want 2", len(sent))
	}
	liveToken := tokenFromURL(t, sent[1].ResetURL)
	if liveToken == supersededToken {
		t.Fatal("the second request reused the first token")
	}

	supersededRec := completeReset(t, handler, supersededToken, newPassword)
	if supersededRec.Code != http.StatusUnauthorized {
		t.Fatalf("superseded token: status %d (body %s), want 401",
			supersededRec.Code, supersededRec.Body.String())
	}

	// The live token completes the reset.
	applied := completeReset(t, handler, liveToken, newPassword)
	if applied.Code != http.StatusNoContent {
		t.Fatalf("live token: status %d (body %s), want 204", applied.Code, applied.Body.String())
	}
	if len(applied.Result().Cookies()) != 0 {
		t.Error("the reset set cookies; the user must sign in fresh")
	}

	// Every session family is gone.
	if code := me(t, handler, signedIn); code != http.StatusUnauthorized {
		t.Errorf("the pre-existing session still works: /users/me = %d, want 401", code)
	}

	// The new password works and the old one does not.
	if rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", `{"identifier":"resetflow","password":"`+newPassword+`"}`)); rec.Code != http.StatusOK {
		t.Errorf("login with the new password: status %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", `{"identifier":"resetflow","password":"`+oldPassword+`"}`)); rec.Code != http.StatusUnauthorized {
		t.Errorf("login with the old password: status %d, want 401", rec.Code)
	}

	// Replay, an expired link and a token that never existed are one answer.
	replay := completeReset(t, handler, liveToken, newPassword)
	garbage := completeReset(t, handler, strings.Repeat("z", 43), newPassword)
	expired := expiredTokenResponse(ctx, t, store, handler, newPassword)

	assertIdentical(t, "replayed versus unknown token", replay, garbage)
	assertIdentical(t, "expired versus unknown token", expired, garbage)
	assertIdentical(t, "superseded versus unknown token", supersededRec, garbage)
	if replay.Code != http.StatusUnauthorized {
		t.Errorf("replayed token: status %d, want 401", replay.Code)
	}
}

// expiredTokenResponse mints a token that is already past its expiry and
// returns what reset-complete answers for it.
func expiredTokenResponse(ctx context.Context, t *testing.T, store *storage.Store, handler http.Handler, newPassword string) *httptest.ResponseRecorder {
	t.Helper()

	raw, hash := session.NewToken()
	user, err := store.UserByEmail(ctx, "resetflow@example.com")
	if err != nil {
		t.Fatalf("look up fixture user: %v", err)
	}
	if err := store.CreatePasswordResetToken(ctx, user.ID, hash, -time.Minute); err != nil {
		t.Fatalf("mint an expired token: %v", err)
	}
	return completeReset(t, handler, raw, newPassword)
}

// completeReset posts one reset-complete request.
func completeReset(t *testing.T, handler http.Handler, token, newPassword string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(api.PasswordResetCompleteRequest{Token: token, NewPassword: newPassword})
	if err != nil {
		t.Fatalf("marshal reset-complete body: %v", err)
	}
	return doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/reset-complete", string(body), withRemoteAddr("203.0.113.70:54321")))
}

// tokenFromURL pulls the raw token out of an emailed link, the way the
// reset page's client-side script will: from the FRAGMENT. It also pins the
// security property of the link's shape — a token in the query string would
// reach the server and sit in access logs and Referer headers, so a link
// carrying one fails the test.
func tokenFromURL(t *testing.T, link string) string {
	t.Helper()

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse reset URL %q: %v", link, err)
	}
	if parsed.RawQuery != "" {
		t.Fatalf("reset URL %q carries a query string; the token must live in the fragment", link)
	}
	token, ok := strings.CutPrefix(parsed.Fragment, "token=")
	if !ok || token == "" {
		t.Fatalf("reset URL %q carries no #token= fragment", link)
	}
	return token
}
