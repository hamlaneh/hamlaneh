package httpserver_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// loginStore wires the fake for successful logins by the fixture user.
func loginStore(t *testing.T) (*fakeStore, *storage.NewSession) {
	t.Helper()

	created := &storage.NewSession{}
	user := fixtureUser()
	return &fakeStore{
		userByIdentifier: func(_ context.Context, identifier string) (storage.User, error) {
			if strings.EqualFold(identifier, user.Username) {
				return user, nil
			}
			return storage.User{}, storage.ErrNotFound
		},
		createSession: func(_ context.Context, ns storage.NewSession) (storage.Session, error) {
			*created = ns
			return fixtureSession(), nil
		},
	}, created
}

func TestLoginValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"not json", "not-json"},
		{"missing identifier", `{"password":"x"}`},
		{"missing password", `{"identifier":"member"}`},
		{"identifier too long", loginBody(strings.Repeat("a", 321), "pw")},
		{"password too long", loginBody("member", strings.Repeat("a", 1025))},
		{"trailing garbage", loginBody("member", "pw") + `{"again":true}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, _ := loginStore(t)
			rec := do(t, store, request(http.MethodPost, "/api/v1/auth/login", tt.body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestLoginNoEnumeration pins the anti-enumeration contract: the response
// for an unknown identifier and for a wrong password must be byte-identical.
func TestLoginNoEnumeration(t *testing.T) {
	t.Parallel()

	store, _ := loginStore(t)
	unknownUser := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("ghost", "any password at all")))
	wrongPassword := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", "definitely wrong password")))

	wantError(t, unknownUser, http.StatusUnauthorized, "invalid_credentials")
	wantError(t, wrongPassword, http.StatusUnauthorized, "invalid_credentials")
	assertIdentical(t, "unknown user versus wrong password", unknownUser, wrongPassword)
	if len(responseCookies(unknownUser)) != 0 || len(responseCookies(wrongPassword)) != 0 {
		t.Error("failed login set cookies")
	}
}

func TestLoginSuccess(t *testing.T) {
	t.Parallel()

	store, created := loginStore(t)
	rec := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", fixturePassword)))

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"username":"member"`) {
		t.Errorf("response body %s does not carry the user", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "argon2id") {
		t.Error("response leaked the password hash")
	}

	cookies := responseCookies(rec)
	if len(cookies) != 3 {
		t.Fatalf("login set %d cookies, want 3", len(cookies))
	}

	access := cookieByName(t, cookies, session.AccessCookie)
	assertCookieAttrs(t, access, "/", 900, true)
	refresh := cookieByName(t, cookies, session.RefreshCookie)
	assertCookieAttrs(t, refresh, "/api/v1/auth/refresh", 2592000, true)
	csrf := cookieByName(t, cookies, session.CSRFCookie)
	assertCookieAttrs(t, csrf, "/", 2592000, false)

	for _, c := range []*http.Cookie{access, refresh, csrf} {
		if c.Value == "" {
			t.Errorf("cookie %s has an empty value", c.Name)
		}
	}
	if access.Value == refresh.Value {
		t.Error("access and refresh tokens are identical")
	}

	// The stored hashes must be the SHA-256 of the cookie values.
	if string(created.AccessTokenHash) != string(session.HashToken(access.Value)) {
		t.Error("stored access hash does not match the access cookie")
	}
	if string(created.RefreshTokenHash) != string(session.HashToken(refresh.Value)) {
		t.Error("stored refresh hash does not match the refresh cookie")
	}
	if created.AccessTTL != session.AccessTTL || created.RefreshTTL != session.RefreshTTL {
		t.Errorf("session TTLs = %v/%v, want %v/%v",
			created.AccessTTL, created.RefreshTTL, session.AccessTTL, session.RefreshTTL)
	}
}

// TestLoginOnATwoStepAccountMintsNoSession pins the seam between the two
// halves of a two-step sign-in: the password earns a challenge and nothing
// else. If Login stopped consulting the second factor this test would see a
// 200 with three session cookies instead.
func TestLoginOnATwoStepAccountMintsNoSession(t *testing.T) {
	t.Parallel()

	store, created := loginStore(t)
	activated := time.Now()
	store.totpByUser = func(_ context.Context, userID uuid.UUID) (storage.Totp, error) {
		return storage.Totp{UserID: userID, ActivatedAt: &activated}, nil
	}
	var challengeUser uuid.UUID
	var challengeHash []byte
	store.createTotpChallenge = func(_ context.Context, userID uuid.UUID, tokenHash []byte, _ time.Duration) error {
		challengeUser, challengeHash = userID, tokenHash
		return nil
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", fixturePassword)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}

	cookies := responseCookies(rec)
	if len(cookies) != 1 {
		t.Fatalf("the password step set %d cookies, want only the challenge", len(cookies))
	}
	for _, c := range cookies {
		switch c.Name {
		case session.AccessCookie, session.RefreshCookie, session.CSRFCookie:
			t.Errorf("the password step minted %s on a two-step account", c.Name)
		}
	}
	if created.UserID != uuid.Nil {
		t.Error("the password step created a session on a two-step account")
	}

	challenge := cookies[0]
	if challenge.Name != "hamlaneh_2fa" {
		t.Fatalf("cookie %q is not the challenge cookie", challenge.Name)
	}
	if challenge.Value == "" || challenge.MaxAge <= 0 {
		t.Errorf("challenge cookie is not live: value %q, max-age %d", challenge.Value, challenge.MaxAge)
	}
	if challengeUser != fixtureUser().ID {
		t.Errorf("challenge minted for %s, want the authenticated user %s", challengeUser, fixtureUser().ID)
	}
	// The cookie carries the raw token; only its hash is stored.
	if string(challengeHash) != string(session.HashToken(challenge.Value)) {
		t.Error("stored challenge hash does not match the challenge cookie")
	}
}

// TestLoginFailsClosedWhenTwoStepCannotBeDetermined pins the direction the
// second-factor check fails in. A store that cannot say whether the account
// has one must answer 500, never fall through into a password-only session:
// "I don't know" must not read as "this account has none".
func TestLoginFailsClosedWhenTwoStepCannotBeDetermined(t *testing.T) {
	t.Parallel()

	store, created := loginStore(t)
	store.totpByUser = func(context.Context, uuid.UUID) (storage.Totp, error) {
		return storage.Totp{}, errors.New("database is on fire")
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", fixturePassword)))
	wantError(t, rec, http.StatusInternalServerError, "internal_error")
	if len(responseCookies(rec)) != 0 {
		t.Error("a login that could not check the second factor set cookies")
	}
	if created.UserID != uuid.Nil {
		t.Error("a login that could not check the second factor created a session")
	}
}

// assertCookieAttrs pins the security attributes every session cookie needs.
func assertCookieAttrs(t *testing.T, c *http.Cookie, path string, maxAge int, httpOnly bool) {
	t.Helper()

	if c.Path != path {
		t.Errorf("cookie %s path = %q, want %q", c.Name, c.Path, path)
	}
	if c.MaxAge != maxAge {
		t.Errorf("cookie %s max-age = %d, want %d", c.Name, c.MaxAge, maxAge)
	}
	if c.HttpOnly != httpOnly {
		t.Errorf("cookie %s httponly = %v, want %v", c.Name, c.HttpOnly, httpOnly)
	}
	if !c.Secure {
		t.Errorf("cookie %s is not Secure", c.Name)
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie %s samesite = %v, want Strict", c.Name, c.SameSite)
	}
}

// TestLoginWithoutStorageAnswers500 pins the nil-store guard: a
// storage-less server (unit-test wiring) must answer 500, never panic.
func TestLoginWithoutStorageAnswers500(t *testing.T) {
	t.Parallel()

	rec := do(t, nil, request(http.MethodPost, "/api/v1/auth/login", loginBody("member", fixturePassword)))
	wantError(t, rec, http.StatusInternalServerError, "internal_error")
}

// TestLoginRateLimitIgnoresSuccesses pins that the limiters count only
// failed authentications: a run of successful logins well past the limit —
// one office IP, one identifier — must never answer 429 and must leave the
// full failure budget untouched.
func TestLoginRateLimitIgnoresSuccesses(t *testing.T) {
	t.Parallel()

	store, _ := loginStore(t)
	handler := httpserver.Handler(store)

	for i := 1; i <= 12; i++ {
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
			loginBody("member", fixturePassword)))
		if rec.Code != http.StatusOK {
			t.Fatalf("successful attempt %d: got status %d, want 200 (body %s)", i, rec.Code, rec.Body.String())
		}
	}

	// The buckets are unpoisoned: all 10 failures are still allowed before
	// the 11th trips both windows.
	for i := 1; i <= 10; i++ {
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
			loginBody("member", "definitely wrong password")))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("failed attempt %d rate-limited; successful logins consumed budget", i)
		}
	}
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", "definitely wrong password")))
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
}

func TestLoginRateLimitPerIP(t *testing.T) {
	t.Parallel()

	store, _ := loginStore(t)
	handler := httpserver.Handler(store)

	// Same client IP, different identifiers: the per-IP window must trip on
	// the 11th attempt.
	for i := 1; i <= 10; i++ {
		req := request(http.MethodPost, "/api/v1/auth/login", loginBody(fmt.Sprintf("user-%d", i), "wrong"))
		rec := doHandler(t, handler, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d rate-limited, want the first 10 allowed", i)
		}
	}
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody("user-11", "wrong")))
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
}

func TestLoginRateLimitPerIdentifier(t *testing.T) {
	t.Parallel()

	store, _ := loginStore(t)
	handler := httpserver.Handler(store)

	// Different public client IPs, same identifier (case varied — the key is
	// lowercased): the per-identifier window must trip on the 11th attempt.
	for i := 1; i <= 10; i++ {
		req := request(http.MethodPost, "/api/v1/auth/login", loginBody("Target-User", "wrong"),
			withRemoteAddr(fmt.Sprintf("203.0.113.%d:1234", i)))
		rec := doHandler(t, handler, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d rate-limited, want the first 10 allowed", i)
		}
	}
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody("target-user", "wrong"),
		withRemoteAddr("203.0.113.111:1234")))
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
}

// TestLoginRateLimitTrustsForwardedForBehindAProxy pins the proxy trust model
// on a server told it has one — the compose stack, where Caddy terminates
// every connection. X-Forwarded-For distinguishes clients only when the
// direct peer sits where Caddy sits, and is ignored from public peers.
//
// Both subtests are the server-mode behaviour that must not change:
// WithTrustedProxy(true) is exactly what cmd/hamlaneh-server passes there.
func TestLoginRateLimitTrustsForwardedForBehindAProxy(t *testing.T) {
	t.Parallel()

	t.Run("private peer: forwarded clients are distinct", func(t *testing.T) {
		t.Parallel()
		store, _ := loginStore(t)
		handler := httpserver.Handler(store, httpserver.WithTrustedProxy(true))

		// 15 attempts from one private peer, each a distinct forwarded
		// client and identifier: no limit trips.
		for i := 1; i <= 15; i++ {
			req := request(http.MethodPost, "/api/v1/auth/login", loginBody(fmt.Sprintf("u%d", i), "wrong"),
				withRemoteAddr("10.0.0.2:5000"),
				withForwardedFor(fmt.Sprintf("198.51.100.%d", i)))
			rec := doHandler(t, handler, req)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("attempt %d rate-limited despite distinct forwarded clients", i)
			}
		}
	})

	t.Run("public peer: forwarded header is ignored", func(t *testing.T) {
		t.Parallel()
		store, _ := loginStore(t)
		handler := httpserver.Handler(store, httpserver.WithTrustedProxy(true))

		// A public peer spoofing distinct X-Forwarded-For values must still
		// be limited by its own address.
		for i := 1; i <= 10; i++ {
			req := request(http.MethodPost, "/api/v1/auth/login", loginBody(fmt.Sprintf("u%d", i), "wrong"),
				withRemoteAddr("198.51.100.7:5000"),
				withForwardedFor(fmt.Sprintf("203.0.113.%d", i)))
			rec := doHandler(t, handler, req)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("attempt %d rate-limited early", i)
			}
		}
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody("u11", "wrong"),
			withRemoteAddr("198.51.100.7:5000"),
			withForwardedFor("203.0.113.99")))
		wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	})
}

// TestLoginRateLimitIgnoresForwardedForWithNoProxy is the home-mode half, and
// the regression this whole option exists for.
//
// Home mode binds 127.0.0.1 with nothing in front of it, so the loopback peer
// is the caller and not a proxy. Before WithTrustedProxy, the address SHAPE
// was the whole test, which made every local process — and the page's own
// JavaScript, since X-Forwarded-For is not a forbidden header name — able to
// mint a fresh sign-in budget per attempt by rotating the header.
//
// A default-constructed handler is what proves the default is the safe one:
// nothing here passes an option, and the header is still ignored.
func TestLoginRateLimitIgnoresForwardedForWithNoProxy(t *testing.T) {
	t.Parallel()

	for _, peer := range []string{"127.0.0.1:5000", "10.0.0.2:5000"} {
		t.Run(peer, func(t *testing.T) {
			t.Parallel()
			store, _ := loginStore(t)
			handler := httpserver.Handler(store)

			// Ten attempts, each claiming a different client. Without the
			// fix these are ten separate budgets and none of them trips.
			for i := 1; i <= 10; i++ {
				req := request(http.MethodPost, "/api/v1/auth/login", loginBody(fmt.Sprintf("u%d", i), "wrong"),
					withRemoteAddr(peer),
					withForwardedFor(fmt.Sprintf("198.51.100.%d", i)))
				rec := doHandler(t, handler, req)
				if rec.Code == http.StatusTooManyRequests {
					t.Fatalf("attempt %d rate-limited early", i)
				}
			}
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody("u11", "wrong"),
				withRemoteAddr(peer),
				withForwardedFor("198.51.100.99")))
			wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
		})
	}
}

func TestRefresh(t *testing.T) {
	t.Parallel()

	t.Run("without refresh cookie clears cookies and answers 401", func(t *testing.T) {
		t.Parallel()
		rec := do(t, &fakeStore{}, request(http.MethodPost, "/api/v1/auth/refresh", ""))
		wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
		assertClearedCookies(t, rec)
	})

	t.Run("rotation sets new access and refresh cookies", func(t *testing.T) {
		t.Parallel()
		presented := "the-refresh-token"
		var rotatedNext storage.SessionTokens
		store := &fakeStore{
			rotateSession: func(_ context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error) {
				if string(refreshHash) != string(session.HashToken(presented)) {
					t.Errorf("rotate got a hash that is not SHA-256 of the presented cookie")
				}
				rotatedNext = next
				return fixtureSession(), storage.RotateOutcomeRotated, nil
			},
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/refresh", "", withRefreshCookie(presented)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}

		cookies := responseCookies(rec)
		if len(cookies) != 2 {
			t.Fatalf("refresh set %d cookies, want 2 (access + refresh; csrf is per-login)", len(cookies))
		}
		access := cookieByName(t, cookies, session.AccessCookie)
		assertCookieAttrs(t, access, "/", 900, true)
		refresh := cookieByName(t, cookies, session.RefreshCookie)
		assertCookieAttrs(t, refresh, "/api/v1/auth/refresh", 2592000, true)
		if string(rotatedNext.AccessTokenHash) != string(session.HashToken(access.Value)) {
			t.Error("stored next access hash does not match the new access cookie")
		}
		if string(rotatedNext.RefreshTokenHash) != string(session.HashToken(refresh.Value)) {
			t.Error("stored next refresh hash does not match the new refresh cookie")
		}
	})

	for _, outcome := range []storage.RotateOutcome{storage.RotateOutcomeInvalid, storage.RotateOutcomeReuseDetected} {
		t.Run(fmt.Sprintf("outcome %d clears cookies and answers 401", outcome), func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{
				rotateSession: func(context.Context, []byte, storage.SessionTokens) (storage.Session, storage.RotateOutcome, error) {
					return storage.Session{}, outcome, nil
				},
			}
			rec := do(t, store, request(http.MethodPost, "/api/v1/auth/refresh", "", withRefreshCookie("presented")))
			wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
			assertClearedCookies(t, rec)
		})
	}
}

func TestLogoutClearsCookiesAndRevokesFamily(t *testing.T) {
	t.Parallel()

	var revoked uuid.UUID
	store := authedStore(fixtureUser())
	store.revokeFamily = func(_ context.Context, familyID uuid.UUID) error {
		revoked = familyID
		return nil
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/auth/logout", "",
		withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if revoked != fixtureSession().FamilyID {
		t.Errorf("revoked family %s, want the session's family %s", revoked, fixtureSession().FamilyID)
	}
	assertClearedCookies(t, rec)
}

func TestChangePassword(t *testing.T) {
	t.Parallel()

	t.Run("wrong current password is 403", func(t *testing.T) {
		t.Parallel()
		body := `{"current_password":"not the right one","new_password":"a brand new passphrase"}`
		rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusForbidden, "invalid_current_password")
	})

	t.Run("weak new password is 400", func(t *testing.T) {
		t.Parallel()
		body := `{"current_password":"` + fixturePassword + `","new_password":"short"}`
		rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("empty current password is 400", func(t *testing.T) {
		t.Parallel()
		body := `{"current_password":"","new_password":"a brand new passphrase"}`
		rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("success updates the hash keeping the current family", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser, gotKeep uuid.UUID
		var gotHash string
		store.updatePassword = func(_ context.Context, userID uuid.UUID, hash string, keep uuid.UUID) error {
			gotUser, gotHash, gotKeep = userID, hash, keep
			return nil
		}
		body := `{"current_password":"` + fixturePassword + `","new_password":"a brand new passphrase"}`
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID {
			t.Errorf("updated user %s, want %s", gotUser, fixtureUser().ID)
		}
		if gotKeep != fixtureSession().FamilyID {
			t.Errorf("kept family %s, want the current session family %s", gotKeep, fixtureSession().FamilyID)
		}
		if !strings.HasPrefix(gotHash, "$argon2id$") {
			t.Errorf("stored hash %q is not argon2id", gotHash)
		}
	})
}

// assertClearedCookies pins that all three session cookies are expired with
// matching attributes.
func assertClearedCookies(t *testing.T, rec interface{ Header() http.Header }) {
	t.Helper()

	res := http.Response{Header: rec.Header()}
	cookies := res.Cookies()
	if len(cookies) != 3 {
		t.Fatalf("%d cookies set, want all 3 cleared", len(cookies))
	}
	for _, c := range cookies {
		if c.Value != "" {
			t.Errorf("cookie %s not cleared: value %q", c.Name, c.Value)
		}
		if c.MaxAge >= 0 {
			t.Errorf("cookie %s max-age = %d, want negative (expired)", c.Name, c.MaxAge)
		}
	}
	access := cookieByName(t, cookies, session.AccessCookie)
	if access.Path != "/" {
		t.Errorf("cleared access cookie path %q, want /", access.Path)
	}
	refresh := cookieByName(t, cookies, session.RefreshCookie)
	if refresh.Path != "/api/v1/auth/refresh" {
		t.Errorf("cleared refresh cookie path %q, want /api/v1/auth/refresh", refresh.Path)
	}
	cookieByName(t, cookies, session.CSRFCookie)
}

// doHandler serves a request against an existing handler (for tests that
// need rate-limiter state to persist across requests).
func doHandler(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func withRemoteAddr(addr string) func(*http.Request) {
	return func(r *http.Request) { r.RemoteAddr = addr }
}

func withForwardedFor(value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-Forwarded-For", value) }
}

func withRefreshCookie(value string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: session.RefreshCookie, Value: value})
	}
}

// TestLoginRefusesAnAccountWithNoPassword pins the guard ADR 004 calls for.
// Migration 0014 made password_hash nullable for accounts a directory
// provisioned or single sign-on created, and the canonical projection reads
// NULL back as "". Without an explicit refusal, every password attempt
// against one of those reaches the argon2 verifier as a malformed hash and
// is logged as an internal defect, which it is not.
//
// The refusal is byte-identical to the one a wrong password gets, because
// "this account has no password" is not something an unauthenticated caller
// gets to learn.
func TestLoginRefusesAnAccountWithNoPassword(t *testing.T) {
	t.Parallel()

	provisioned := fixtureUser()
	provisioned.PasswordHash = ""
	store := &fakeStore{
		userByIdentifier: func(_ context.Context, identifier string) (storage.User, error) {
			if strings.EqualFold(identifier, provisioned.Username) {
				return provisioned, nil
			}
			return storage.User{}, storage.ErrNotFound
		},
		createSession: func(context.Context, storage.NewSession) (storage.Session, error) {
			t.Error("a session was minted for an account with no password credential")
			return fixtureSession(), nil
		},
	}

	noPassword := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", "any password at all")))
	wantError(t, noPassword, http.StatusUnauthorized, "invalid_credentials")
	if len(responseCookies(noPassword)) != 0 {
		t.Error("the refusal set cookies")
	}

	withPassword, _ := loginStore(t)
	wrongPassword := do(t, withPassword, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("member", "definitely wrong password")))
	assertIdentical(t, "no password versus wrong password", noPassword, wrongPassword)
}
