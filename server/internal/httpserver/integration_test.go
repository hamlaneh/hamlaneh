package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// sessionCookies is the client half of a logged-in session in these tests.
type sessionCookies struct {
	access  string
	refresh string
	csrf    string
}

// login performs a real login through the handler and returns the cookies.
func login(t *testing.T, handler http.Handler, identifier, pw string) sessionCookies {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody(identifier, pw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s: got status %d (body %s)", identifier, rec.Code, rec.Body.String())
	}
	cookies := responseCookies(rec)
	return sessionCookies{
		access:  cookieByName(t, cookies, session.AccessCookie).Value,
		refresh: cookieByName(t, cookies, session.RefreshCookie).Value,
		csrf:    cookieByName(t, cookies, session.CSRFCookie).Value,
	}
}

// withSession attaches the session's cookies (and the CSRF pair) to a
// request the way a browser would.
func withSession(sc sessionCookies) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: session.AccessCookie, Value: sc.access})
		r.AddCookie(&http.Cookie{Name: session.RefreshCookie, Value: sc.refresh})
		r.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: sc.csrf})
		r.Header.Set(session.CSRFHeader, sc.csrf)
	}
}

// me asserts GET /users/me with the session and returns the status.
func me(t *testing.T, handler http.Handler, sc sessionCookies) int {
	t.Helper()
	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/users/me", "", withSession(sc)))
	return rec.Code
}

// TestSessionLifecycleIntegration walks the whole identity core through the
// real stack: login, authenticated reads, refresh rotation, reuse
// detection, password change with cross-session revocation, and logout.
func TestSessionLifecycleIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	const initialPassword = "the initial passphrase"
	hash := password.Hash(initialPassword)
	email := "walker@example.com"
	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: "walker", Email: &email, PasswordHash: hash, Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	// Login works case-insensitively and by email.
	first := login(t, handler, "WALKER", initialPassword)
	if got := me(t, handler, first); got != http.StatusOK {
		t.Fatalf("users/me after login: got %d, want 200", got)
	}

	// Refresh rotates: new cookies arrive, the old access token dies, the
	// new one works.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "", withSession(first)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	rotated := first
	rotated.access = cookieByName(t, responseCookies(rec), session.AccessCookie).Value
	rotated.refresh = cookieByName(t, responseCookies(rec), session.RefreshCookie).Value

	if got := me(t, handler, first); got != http.StatusUnauthorized {
		t.Errorf("old access token after rotation: got %d, want 401", got)
	}
	if got := me(t, handler, rotated); got != http.StatusOK {
		t.Errorf("new access token after rotation: got %d, want 200", got)
	}

	// Replaying the rotated-away refresh token trips reuse detection and
	// revokes the whole family: the current tokens die with it.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "", withSession(first)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh reuse: got status %d, want 401", rec.Code)
	}
	if got := me(t, handler, rotated); got != http.StatusUnauthorized {
		t.Errorf("family member access token survived reuse detection: got %d, want 401", got)
	}
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "", withSession(rotated)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("valid-generation refresh after family revocation: got %d, want 401", rec.Code)
	}

	// Two fresh sessions; changing the password in one revokes the other.
	sessionA := login(t, handler, "walker", initialPassword)
	sessionB := login(t, handler, "walker@example.com", initialPassword)

	const newPassword = "an entirely new passphrase"
	body := `{"current_password":"` + initialPassword + `","new_password":"` + newPassword + `"}`
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/change-password", body, withSession(sessionB)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change-password: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := me(t, handler, sessionA); got != http.StatusUnauthorized {
		t.Errorf("other session after password change: got %d, want 401", got)
	}
	if got := me(t, handler, sessionB); got != http.StatusOK {
		t.Errorf("changing session after password change: got %d, want 200", got)
	}

	// Old password is dead, new password works.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody("walker", initialPassword)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("login with the old password: got %d, want 401", rec.Code)
	}
	final := login(t, handler, "walker", newPassword)

	// Logout revokes and clears.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/logout", "", withSession(final)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	assertClearedCookies(t, rec)
	if got := me(t, handler, final); got != http.StatusUnauthorized {
		t.Errorf("access token after logout: got %d, want 401", got)
	}
}

// TestAccessTokenExpiryIntegration pins that expiry is enforced by the
// database clock end to end: a session whose access TTL has passed answers
// 401 through the real stack.
func TestAccessTokenExpiryIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	hash := password.Hash("some fixture password")
	user, err := store.CreateUser(ctx, storage.NewUser{Username: "expired", PasswordHash: hash, Locale: "en"})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	accessRaw, accessHash := session.NewToken()
	_, refreshHash := session.NewToken()
	if _, err := store.CreateSession(ctx, storage.NewSession{
		UserID: user.ID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash:  accessHash,
			RefreshTokenHash: refreshHash,
			AccessTTL:        -time.Second, // born expired, judged by now() in SQL
			RefreshTTL:       time.Hour,
		},
	}); err != nil {
		t.Fatalf("create expired session: %v", err)
	}

	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie(accessRaw)))
	wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
}

// TestMalformedForwardedHeadersIntegration exercises clientIP hardening
// through the stack: garbage X-Forwarded-For from a private peer must not
// break login handling.
func TestMalformedForwardedHeadersIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	handler := httpserver.Handler(store)

	rec := httptest.NewRecorder()
	req := request(http.MethodPost, "/api/v1/auth/login", loginBody("ghost", "whatever password"),
		withRemoteAddr("10.0.0.9:4444"),
		withForwardedFor("not an ip at all,,,"))
	handler.ServeHTTP(rec, req)
	wantError(t, rec, http.StatusUnauthorized, "invalid_credentials")
	if !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Errorf("unexpected body %s", rec.Body.String())
	}
}
