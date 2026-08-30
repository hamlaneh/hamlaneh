package httpserver_test

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/hotp"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
	"github.com/hamlaneh/hamlaneh/server/internal/totp"
)

const (
	totpFixturePassword = "a perfectly serviceable passphrase"
	challengeCookieName = "hamlaneh_2fa"
	challengeCookiePath = "/api/v1/auth/login/totp"
)

// twoStepAccount is a signed-in fixture account plus everything the tests
// need to act as its authenticator.
type twoStepAccount struct {
	user    storage.User
	cookies sessionCookies
	secret  []byte
	codes   []string
	// setupCode is the authenticator code that verified the setup. It is
	// already spent: the replay guard does not care that it was spent on a
	// different step of the flow.
	setupCode string
}

// newTotpFixture creates a user, signs it in, and returns the store, the
// scratch DSN and the handler under test.
func newTotpFixture(t *testing.T, username string) (testdb.Store, string, http.Handler, twoStepAccount) {
	t.Helper()

	store, dsn := testdb.New(t)
	handler := httpserver.Handler(store)

	email := username + "@example.com"
	user, err := store.CreateUser(context.Background(), storage.NewUser{
		Username:     username,
		Email:        &email,
		PasswordHash: password.Hash(totpFixturePassword),
		Locale:       "en",
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	return store, dsn, handler, twoStepAccount{
		user:    user,
		cookies: login(t, handler, username, totpFixturePassword),
	}
}

// startSetup runs step 1 and returns the decoded secret.
func startSetup(t *testing.T, handler http.Handler, sc sessionCookies) []byte {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/setup", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("start setup: status %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("setup Cache-Control is %q, want no-store", got)
	}

	var setup api.TotpSetup
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
		t.Fatalf("setup body is not TotpSetup: %v", err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatalf("manual key is not base32: %v", err)
	}
	if len(secret) != totp.SecretBytes {
		t.Fatalf("secret is %d bytes, want %d", len(secret), totp.SecretBytes)
	}
	if !strings.HasPrefix(setup.QrSvg, "<svg ") {
		t.Error("setup returned no inline QR")
	}
	if !strings.Contains(setup.OtpauthUri, "otpauth://totp/") {
		t.Errorf("otpauth URI is %q", setup.OtpauthUri)
	}
	return secret
}

// codeAt returns the six digits an authenticator would show for secret at t.
func codeAt(t *testing.T, secret []byte, at time.Time) string {
	t.Helper()

	step := totp.Step(at)
	if step < 0 {
		t.Fatalf("negative step for %s", at)
	}
	code, err := hotp.GenerateCode(totp.EncodeSecret(secret), uint64(step))
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

// nextCode returns the code the authenticator will show next. Sign-in tests
// use it because the code that verified the setup is already spent — an
// accepted step is never accepted twice.
func nextCode(t *testing.T, secret []byte) string {
	t.Helper()
	return codeAt(t, secret, time.Now().Add(totp.Period))
}

// verifySetup runs step 2 with a correct code and returns the recovery codes
// alongside the code it spent.
func verifySetup(t *testing.T, handler http.Handler, sc sessionCookies, secret []byte) ([]string, string) {
	t.Helper()

	used := codeAt(t, secret, time.Now())
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
		`{"code":"`+used+`"}`, withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify setup: status %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("verify Cache-Control is %q, want no-store", got)
	}

	var codes api.RecoveryCodes
	if err := json.Unmarshal(rec.Body.Bytes(), &codes); err != nil {
		t.Fatalf("verify body is not RecoveryCodes: %v", err)
	}
	if len(codes.Codes) != totp.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes.Codes), totp.RecoveryCodeCount)
	}
	return codes.Codes, used
}

// enableTwoStep walks the whole three-step activation through the API.
func enableTwoStep(t *testing.T, handler http.Handler, acct *twoStepAccount) {
	t.Helper()

	acct.secret = startSetup(t, handler, acct.cookies)
	acct.codes, acct.setupCode = verifySetup(t, handler, acct.cookies, acct.secret)

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/activate", "", withSession(acct.cookies)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("activate: status %d (body %s)", rec.Code, rec.Body.String())
	}
}

// totpStatus reads the Security card's state.
func totpStatus(t *testing.T, handler http.Handler, sc sessionCookies) api.TotpStatus {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/users/me/totp", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("totp status: status %d (body %s)", rec.Code, rec.Body.String())
	}
	var status api.TotpStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("status body is not TotpStatus: %v", err)
	}
	return status
}

// mintChallenge creates the half-authenticated state the 202 login answer
// sets, and returns the cookie value that carries it.
func mintChallenge(t *testing.T, store testdb.Store, userID uuid.UUID) string {
	t.Helper()

	raw, hash := session.NewToken()
	if err := store.CreateTotpChallenge(context.Background(), userID, hash, totp.ChallengeTTL); err != nil {
		t.Fatalf("CreateTotpChallenge: %v", err)
	}
	return raw
}

func withChallengeCookie(value string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: challengeCookieName, Value: value})
	}
}

// completeLogin posts one code against a challenge cookie.
func completeLogin(t *testing.T, handler http.Handler, challenge, code string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	return doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
		string(body), withChallengeCookie(challenge)))
}

// TestTotpSetupHappyPathIntegration walks setup, verify, activate and a
// two-step sign-in through the real stack, then pins the replay guard.
func TestTotpSetupHappyPathIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "walker2fa")

	if status := totpStatus(t, handler, acct.cookies); status.Enabled {
		t.Fatal("a fresh account already reports two-step verification on")
	}

	acct.secret = startSetup(t, handler, acct.cookies)

	// Step 1 and 2 leave the account password-only: nothing is on until
	// activate, so closing the panel can never strand the user.
	if status := totpStatus(t, handler, acct.cookies); status.Enabled {
		t.Error("a pending setup reports as enabled")
	}
	_, acct.setupCode = verifySetup(t, handler, acct.cookies, acct.secret)
	if status := totpStatus(t, handler, acct.cookies); status.Enabled {
		t.Error("a verified but unactivated setup reports as enabled")
	}

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/activate", "", withSession(acct.cookies)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("activate: status %d (body %s)", rec.Code, rec.Body.String())
	}

	status := totpStatus(t, handler, acct.cookies)
	if !status.Enabled {
		t.Fatal("two-step verification is off after activation")
	}
	if status.ActivatedAt == nil || status.ActivatedAt.IsZero() {
		t.Error("status carries no activation time")
	}
	if status.RecoveryCodesRemaining == nil || *status.RecoveryCodesRemaining != totp.RecoveryCodeCount {
		t.Error("status does not report the full recovery set")
	}
	if status.RecoveryCodesTotal == nil || *status.RecoveryCodesTotal != totp.RecoveryCodeCount {
		t.Error("status does not report the recovery set size")
	}

	// The code that verified the setup is spent: an accepted step is never
	// accepted a second time, whatever the flow asks it for.
	rec = completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), acct.setupCode)
	wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")

	// Sign in with the authenticator: only this endpoint mints session
	// cookies for a two-step account.
	challenge := mintChallenge(t, store, acct.user.ID)
	code := nextCode(t, acct.secret)
	rec = completeLogin(t, handler, challenge, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete login: status %d (body %s)", rec.Code, rec.Body.String())
	}

	cookies := responseCookies(rec)
	signedIn := sessionCookies{
		access:  cookieByName(t, cookies, session.AccessCookie).Value,
		refresh: cookieByName(t, cookies, session.RefreshCookie).Value,
		csrf:    cookieByName(t, cookies, session.CSRFCookie).Value,
	}
	if signedIn.access == "" {
		t.Fatal("no session was minted")
	}
	if got := me(t, handler, signedIn); got != http.StatusOK {
		t.Errorf("the minted session does not authenticate: got %d", got)
	}

	// The challenge cookie is cleared by the successful completion.
	cleared := cookieByName(t, cookies, challengeCookieName)
	assertChallengeCookieAttrs(t, cleared)
	if cleared.MaxAge >= 0 {
		t.Errorf("challenge cookie was not cleared: MaxAge %d", cleared.MaxAge)
	}

	// Replay: the same code inside the same window is refused, and the spent
	// challenge is gone with it.
	replay := mintChallenge(t, store, acct.user.ID)
	rec = completeLogin(t, handler, replay, code)
	wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")
}

// TestTwoStepLoginIntegration walks the seam between the two halves of a
// sign-in through the real stack: the same correct password that mints a
// session before activation earns only a challenge after it, and the cookie
// that challenge sets is what completes the sign-in.
func TestTwoStepLoginIntegration(t *testing.T) {
	t.Parallel()

	_, _, handler, acct := newTotpFixture(t, "twostep2fa")

	// Password-only: the ordinary 200 and the three session cookies.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("twostep2fa", totpFixturePassword)))
	if rec.Code != http.StatusOK {
		t.Fatalf("password-only login: status %d (body %s)", rec.Code, rec.Body.String())
	}
	before := responseCookies(rec)
	if len(before) != 3 {
		t.Fatalf("password-only login set %d cookies, want the 3 session cookies", len(before))
	}
	for _, c := range before {
		if c.Name == challengeCookieName {
			t.Error("an account without a second factor was challenged")
		}
	}

	enableTwoStep(t, handler, &acct)

	// The same password now stops one step short.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("twostep2fa", totpFixturePassword)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("two-step login: status %d (body %s), want 202", rec.Code, rec.Body.String())
	}

	cookies := responseCookies(rec)
	for _, c := range cookies {
		switch c.Name {
		case session.AccessCookie, session.RefreshCookie, session.CSRFCookie:
			t.Errorf("the password step minted %s: the second factor was skipped", c.Name)
		}
	}
	challenge := cookieByName(t, cookies, challengeCookieName)
	assertChallengeCookieAttrs(t, challenge)
	if challenge.Value == "" || challenge.MaxAge <= 0 {
		t.Fatalf("challenge cookie is not live: value %q, max-age %d", challenge.Value, challenge.MaxAge)
	}

	var offered api.TwoFactorChallenge
	if err := json.Unmarshal(rec.Body.Bytes(), &offered); err != nil {
		t.Fatalf("202 body is not TwoFactorChallenge: %v", err)
	}
	if len(offered.Methods) != 1 || offered.Methods[0] != api.Totp {
		t.Errorf("offered methods %v, want exactly [totp]", offered.Methods)
	}

	// The cookie the 202 set is the one that finishes the sign-in.
	rec = completeLogin(t, handler, challenge.Value, nextCode(t, acct.secret))
	if rec.Code != http.StatusOK {
		t.Fatalf("complete login: status %d (body %s)", rec.Code, rec.Body.String())
	}
	signedIn := sessionCookies{
		access:  cookieByName(t, responseCookies(rec), session.AccessCookie).Value,
		refresh: cookieByName(t, responseCookies(rec), session.RefreshCookie).Value,
		csrf:    cookieByName(t, responseCookies(rec), session.CSRFCookie).Value,
	}
	if got := me(t, handler, signedIn); got != http.StatusOK {
		t.Errorf("the session minted by the code step does not authenticate: got %d", got)
	}

	// A wrong password is still just a wrong password: the second factor is
	// never reached, so no challenge leaks out of a failed first step.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
		loginBody("twostep2fa", "not the passphrase at all")))
	wantError(t, rec, http.StatusUnauthorized, "invalid_credentials")
	if len(responseCookies(rec)) != 0 {
		t.Error("a failed password step set cookies")
	}
}

// TestTotpChallengeIsSingleUseIntegration pins that a challenge dies on the
// attempt that used it, whatever the client does with the cookie afterwards.
func TestTotpChallengeIsSingleUseIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "singleuse2fa")
	enableTwoStep(t, handler, &acct)

	challenge := mintChallenge(t, store, acct.user.ID)
	code := nextCode(t, acct.secret)
	if rec := completeLogin(t, handler, challenge, code); rec.Code != http.StatusOK {
		t.Fatalf("first completion: status %d (body %s)", rec.Code, rec.Body.String())
	}

	// Presenting the cookie again is not a wrong code — there is no
	// challenge left to answer, which is a different answer on purpose.
	rec := completeLogin(t, handler, challenge, code)
	wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	assertChallengeCookieAttrs(t, cookieByName(t, responseCookies(rec), challengeCookieName))
}

// TestTotpLoginAttemptCapIntegration pins that five wrong codes end the
// challenge and send the caller back to the password step.
func TestTotpLoginAttemptCapIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "logincap2fa")
	enableTwoStep(t, handler, &acct)

	challenge := mintChallenge(t, store, acct.user.ID)
	for attempt := 1; attempt <= totp.MaxChallengeAttempts; attempt++ {
		rec := completeLogin(t, handler, challenge, "000000")
		if attempt < totp.MaxChallengeAttempts {
			wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")
			continue
		}
		wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	}

	// The correct code cannot revive a revoked challenge.
	rec := completeLogin(t, handler, challenge, nextCode(t, acct.secret))
	wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	if len(responseCookies(rec)) == 0 {
		t.Error("a dead challenge did not clear its cookie")
	}
}

// assertRetryAfter pins that a 429 carries a usable Retry-After: a whole
// number of seconds, at least 1.
func assertRetryAfter(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	header := rec.Header().Get("Retry-After")
	if header == "" {
		t.Fatal("429 carries no Retry-After header")
	}
	seconds, err := strconv.Atoi(header)
	if err != nil {
		t.Fatalf("Retry-After %q is not a whole number of seconds: %v", header, err)
	}
	if seconds < 1 {
		t.Errorf("Retry-After = %d, want at least 1", seconds)
	}
}

// passwordLogin posts one login with the fixture password from addr and
// returns the recorder.
func passwordLogin(t *testing.T, handler http.Handler, identifier, addr string) *httptest.ResponseRecorder {
	t.Helper()
	return doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login",
		loginBody(identifier, totpFixturePassword), withRemoteAddr(addr)))
}

// secondTwoStepAccount provisions another two-step account on the SAME
// handler (and so the same rate limiters) as an existing fixture.
func secondTwoStepAccount(t *testing.T, store testdb.Store, handler http.Handler, username string) twoStepAccount {
	t.Helper()

	email := username + "@example.com"
	user, err := store.CreateUser(context.Background(), storage.NewUser{
		Username:     username,
		Email:        &email,
		PasswordHash: password.Hash(totpFixturePassword),
		Locale:       "en",
	})
	if err != nil {
		t.Fatalf("create second fixture user: %v", err)
	}
	acct := twoStepAccount{
		user:    user,
		cookies: login(t, handler, username, totpFixturePassword),
	}
	enableTwoStep(t, handler, &acct)
	return acct
}

// TestTwoStepLoginMintingConsumesLoginBudgetIntegration pins the fix for
// free challenge minting: a correct password on a two-step account earns a
// challenge AND spends one unit of the login-attempt budget on both keys,
// so an attacker holding the password cannot mint five-guess windows
// without limit. Without the budget spend every one of these logins would
// answer 202 forever and this test fails on the missing 429.
func TestTwoStepLoginMintingConsumesLoginBudgetIntegration(t *testing.T) {
	t.Parallel()

	t.Run("per identifier, across IPs", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "mintbudget2fa")
		enableTwoStep(t, handler, &acct)

		// A distinct IP per attempt keeps the per-IP window out of the way:
		// only the identifier's budget can trip.
		for i := 1; i <= 10; i++ {
			rec := passwordLogin(t, handler, "mintbudget2fa",
				fmt.Sprintf("203.0.113.%d", i))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("mint %d: status %d (body %s), want 202", i, rec.Code, rec.Body.String())
			}
		}

		rec := passwordLogin(t, handler, "mintbudget2fa", "203.0.113.111")
		wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
		assertRetryAfter(t, rec)
		if len(responseCookies(rec)) != 0 {
			t.Error("a rate-limited login still minted a challenge cookie")
		}
	})

	t.Run("per IP, across identifiers", func(t *testing.T) {
		t.Parallel()

		store, _, handler, first := newTotpFixture(t, "mintipa2fa")
		enableTwoStep(t, handler, &first)
		second := secondTwoStepAccount(t, store, handler, "mintipb2fa")

		// Alternating identifiers from ONE address: each identifier stays at
		// five mints — half its own budget — so only the shared per-IP
		// window can be what trips.
		const attackerIP = "198.51.100.77"
		for i := 1; i <= 10; i++ {
			username := first.user.Username
			if i%2 == 0 {
				username = second.user.Username
			}
			rec := passwordLogin(t, handler, username, attackerIP)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("mint %d as %s: status %d (body %s), want 202", i, username, rec.Code, rec.Body.String())
			}
		}

		rec := passwordLogin(t, handler, first.user.Username, attackerIP)
		wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
		assertRetryAfter(t, rec)
	})
}

// TestCompleteTotpLoginRateLimitPerAccountIntegration pins the per-account
// window on the code step itself, independently of the per-challenge cap: a
// distributed attacker rotating IPs and burning challenge after challenge
// still runs into one budget keyed on the victim's account, checked BEFORE
// the presented code is evaluated. Without the limiter the eleventh wrong
// code answers 401 invalid_totp_code (and a correct one signs in), never
// the contract's 429.
func TestCompleteTotpLoginRateLimitPerAccountIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "acctlimit2fa")
	enableTwoStep(t, handler, &acct)

	// Ten wrong codes across two challenges — the per-challenge cap revokes
	// each after five — from ten different addresses.
	ip := 0
	for range 2 {
		challenge := mintChallenge(t, store, acct.user.ID)
		for attempt := range totp.MaxChallengeAttempts {
			ip++
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
				`{"code":"000000"}`, withChallengeCookie(challenge), withRemoteAddr(fmt.Sprintf("203.0.113.%d:1234", ip))))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("wrong code %d of the challenge: status %d (body %s), want 401",
					attempt+1, rec.Code, rec.Body.String())
			}
		}
	}

	// A fresh challenge buys no fresh budget, and the refusal comes before
	// the code is looked at: even the CORRECT code answers 429 from an
	// address the endpoint has never seen.
	challenge := mintChallenge(t, store, acct.user.ID)
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
		`{"code":"`+nextCode(t, acct.secret)+`"}`, withChallengeCookie(challenge), withRemoteAddr("198.51.100.200:1234")))
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	assertRetryAfter(t, rec)
	for _, c := range responseCookies(rec) {
		switch c.Name {
		case session.AccessCookie, session.RefreshCookie, session.CSRFCookie:
			t.Errorf("a rate-limited completion minted %s", c.Name)
		}
	}
}

// TestCompleteTotpLoginRateLimitPerIPIntegration pins the per-IP window on
// the code step: one address guessing across DIFFERENT accounts is bounded
// even though each account keeps half its own budget — and the account a
// third party hammered from that address stays signed-in-able from
// anywhere else.
func TestCompleteTotpLoginRateLimitPerIPIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, first := newTotpFixture(t, "iplimita2fa")
	enableTwoStep(t, handler, &first)
	second := secondTwoStepAccount(t, store, handler, "iplimitb2fa")

	const attackerIP = "198.51.100.99"
	for _, victim := range []*twoStepAccount{&first, &second} {
		challenge := mintChallenge(t, store, victim.user.ID)
		for attempt := range totp.MaxChallengeAttempts {
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
				`{"code":"000000"}`, withChallengeCookie(challenge), withRemoteAddr(attackerIP)))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("wrong code %d against %s: status %d (body %s), want 401",
					attempt+1, victim.user.Username, rec.Code, rec.Body.String())
			}
		}
	}

	// The eleventh attempt from that address is refused whatever it carries.
	challenge := mintChallenge(t, store, first.user.ID)
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
		`{"code":"000000"}`, withChallengeCookie(challenge), withRemoteAddr(attackerIP)))
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	assertRetryAfter(t, rec)

	// The victim is not locked out: five wrong codes against the account is
	// half its budget, so from their own address the correct code signs in.
	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
		`{"code":"`+nextCode(t, first.secret)+`"}`, withChallengeCookie(challenge), withRemoteAddr("192.0.2.55:1234")))
	if rec.Code != http.StatusOK {
		t.Fatalf("the legitimate user was locked out: status %d (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCompleteTotpLoginRejectsBadRequests(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "badrequest2fa")
	enableTwoStep(t, handler, &acct)

	t.Run("no challenge cookie", func(t *testing.T) {
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp", `{"code":"123456"}`))
		wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	})

	t.Run("unknown challenge token", func(t *testing.T) {
		rec := completeLogin(t, handler, "not-a-real-challenge-token", "123456")
		wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	})

	t.Run("code outside the contract bounds", func(t *testing.T) {
		challenge := mintChallenge(t, store, acct.user.ID)
		for _, code := range []string{"", "12345", "this code is far too long"} {
			rec := completeLogin(t, handler, challenge, code)
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		challenge := mintChallenge(t, store, acct.user.ID)
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login/totp",
			`{"code":`, withChallengeCookie(challenge)))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a code of no valid shape is a wrong code", func(t *testing.T) {
		challenge := mintChallenge(t, store, acct.user.ID)
		rec := completeLogin(t, handler, challenge, "!!!!!!!!")
		wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")
	})
}

// TestTotpChallengeCookieGrantsNothingElse pins the whole point of the
// class: a challenge is authority for exactly one endpoint.
func TestTotpChallengeCookieGrantsNothingElse(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "scoped2fa")
	enableTwoStep(t, handler, &acct)
	challenge := mintChallenge(t, store, acct.user.ID)

	elsewhere := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/users/me"},
		{method: http.MethodGet, path: "/api/v1/users/me/totp"},
		{method: http.MethodPost, path: "/api/v1/users/me/totp/setup"},
		{method: http.MethodPost, path: "/api/v1/users/me/totp/disable"},
		{method: http.MethodPost, path: "/api/v1/auth/logout"},
		{method: http.MethodGet, path: "/api/v1/channels"},
	}
	for _, tt := range elsewhere {
		rec := doHandler(t, handler, request(tt.method, tt.path, "", withChallengeCookie(challenge)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with only a challenge cookie: got %d, want 401", tt.method, tt.path, rec.Code)
		}
	}

	// And it is still good for the one endpoint it is scoped to.
	if rec := completeLogin(t, handler, challenge, nextCode(t, acct.secret)); rec.Code != http.StatusOK {
		t.Errorf("the challenge stopped working for its own endpoint: %d", rec.Code)
	}
}

// TestTotpSetupGuardsIntegration pins the refusals around the three steps.
func TestTotpSetupGuardsIntegration(t *testing.T) {
	t.Parallel()

	t.Run("activate refuses an unverified setup", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "unverified2fa")
		startSetup(t, handler, acct.cookies)

		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/activate", "", withSession(acct.cookies)))
		wantError(t, rec, http.StatusConflict, "totp_setup_not_verified")
		if totpStatus(t, handler, acct.cookies).Enabled {
			t.Error("a refused activation switched two-step verification on")
		}
	})

	t.Run("activate refuses when nothing was started", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "nosetup2fa")
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/activate", "", withSession(acct.cookies)))
		wantError(t, rec, http.StatusConflict, "totp_setup_not_verified")
	})

	t.Run("setup refuses an account that already has it on", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "already2fa")
		enableTwoStep(t, handler, &acct)

		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/setup", "", withSession(acct.cookies)))
		wantError(t, rec, http.StatusConflict, "totp_already_enabled")
	})

	t.Run("verify refuses a code that is not six digits", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "shape2fa")
		startSetup(t, handler, acct.cookies)

		for _, code := range []string{"", "12345", "1234567", "abcdef", "4T7M-9QKX"} {
			rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
				`{"code":"`+code+`"}`, withSession(acct.cookies)))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		}
	})

	t.Run("verify without a pending setup is a conflict", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "verifynone2fa")
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
			`{"code":"123456"}`, withSession(acct.cookies)))
		wantError(t, rec, http.StatusConflict, "totp_setup_expired")
	})

	t.Run("restarting setup replaces the secret", func(t *testing.T) {
		t.Parallel()

		_, _, handler, acct := newTotpFixture(t, "restart2fa")
		first := startSetup(t, handler, acct.cookies)
		second := startSetup(t, handler, acct.cookies)
		if string(first) == string(second) {
			t.Fatal("restarting setup reused the secret")
		}

		// A code for the abandoned secret no longer verifies.
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
			`{"code":"`+codeAt(t, first, time.Now())+`"}`, withSession(acct.cookies)))
		wantError(t, rec, http.StatusForbidden, "invalid_totp_code")
	})
}

// TestTotpSettingsRateLimitIntegration pins the 429 the contract declares
// on totp/setup, totp/verify, totp/disable and totp/recovery-codes — four
// endpoints that promised one and, until this budget existed, could not
// produce it on any path.
//
// The budget is one window per account across all four, so the table walks
// each endpoint as the entry point and then checks that a DIFFERENT one is
// refused too: per-endpoint budgets would let a caller simply move along
// the row. The refusal must also land before confirmPassword, because the
// argon2id verification it guards is the cost being refused — a 429 written
// after it would have paid for nothing.
func TestTotpSettingsRateLimitIntegration(t *testing.T) {
	t.Parallel()

	confirm := `{"password":"` + totpFixturePassword + `"}`

	tests := []struct {
		name string
		path string
		body string
	}{
		{"setup", "/api/v1/users/me/totp/setup", ""},
		{"verify", "/api/v1/users/me/totp/verify", `{"code":"000000"}`},
		{"disable", "/api/v1/users/me/totp/disable", confirm},
		{"recovery codes", "/api/v1/users/me/totp/recovery-codes", confirm},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			slug := strings.ReplaceAll(tc.name, " ", "")
			store, _, handler, acct := newTotpFixture(t, "limit"+slug)

			limited := false
			for range httpserver.TotpSettingsRateLimit + 1 {
				rec := doHandler(t, handler, request(http.MethodPost, tc.path, tc.body, withSession(acct.cookies)))
				if rec.Code == http.StatusTooManyRequests {
					wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
					assertRetryAfter(t, rec)
					limited = true
					break
				}
			}
			if !limited {
				t.Fatalf("%s never answered 429 in %d calls", tc.path, httpserver.TotpSettingsRateLimit+1)
			}

			// One window covers all four: an exhausted account cannot move
			// to a neighbouring endpoint for a fresh budget. The password is
			// CORRECT here, so a 429 proves the refusal beat the argon2id
			// verification rather than following it.
			rec := doHandler(t, handler, request(http.MethodPost,
				"/api/v1/users/me/totp/disable", confirm, withSession(acct.cookies)))
			wantError(t, rec, http.StatusTooManyRequests, "rate_limited")

			// The window is keyed on the account, not shared process state:
			// a second account on the SAME handler — so the very same
			// limiter — still gets its own budget.
			other := signedInAccount(t, store, handler, "fresh"+slug)
			fresh := doHandler(t, handler, request(http.MethodPost,
				"/api/v1/users/me/totp/setup", "", withSession(other)))
			if fresh.Code != http.StatusOK {
				t.Errorf("an unrelated account was rate limited: status %d (body %s)",
					fresh.Code, fresh.Body.String())
			}
		})
	}
}

// signedInAccount creates a plain password-only account on an existing
// store and signs it in through handler.
func signedInAccount(t *testing.T, store testdb.Store, handler http.Handler, username string) sessionCookies {
	t.Helper()

	email := username + "@example.com"
	if _, err := store.CreateUser(context.Background(), storage.NewUser{
		Username:     username,
		Email:        &email,
		PasswordHash: password.Hash(totpFixturePassword),
		Locale:       "en",
	}); err != nil {
		t.Fatalf("create fixture user %s: %v", username, err)
	}
	return login(t, handler, username, totpFixturePassword)
}

// TestTotpVerifyAttemptCapIntegration pins that five wrong codes revoke the
// pending setup rather than leaving a brute-force oracle open.
func TestTotpVerifyAttemptCapIntegration(t *testing.T) {
	t.Parallel()

	_, _, handler, acct := newTotpFixture(t, "verifycap2fa")
	secret := startSetup(t, handler, acct.cookies)

	for attempt := 1; attempt <= totp.MaxSetupAttempts; attempt++ {
		rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
			`{"code":"000000"}`, withSession(acct.cookies)))
		if attempt < totp.MaxSetupAttempts {
			// A wrong code does not restart setup.
			wantError(t, rec, http.StatusForbidden, "invalid_totp_code")
			continue
		}
		wantError(t, rec, http.StatusConflict, "totp_setup_expired")
	}

	// The right code cannot revive a revoked setup.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/verify",
		`{"code":"`+codeAt(t, secret, time.Now())+`"}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusConflict, "totp_setup_expired")

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/activate", "", withSession(acct.cookies)))
	wantError(t, rec, http.StatusConflict, "totp_setup_not_verified")
}

// TestRecoveryCodeLoginIntegration pins that a recovery code signs in once
// and is dead afterwards.
func TestRecoveryCodeLoginIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "recovery2fa")
	enableTwoStep(t, handler, &acct)

	// Typed the way a human would: lower case, spaces, no hyphen.
	typed := strings.ToLower(strings.ReplaceAll(acct.codes[0], "-", " "))
	rec := completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), typed)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery sign-in: status %d (body %s)", rec.Code, rec.Body.String())
	}
	if cookieByName(t, responseCookies(rec), session.AccessCookie).Value == "" {
		t.Error("recovery sign-in minted no session")
	}

	status := totpStatus(t, handler, acct.cookies)
	if status.RecoveryCodesRemaining == nil || *status.RecoveryCodesRemaining != totp.RecoveryCodeCount-1 {
		t.Errorf("the spent code was not counted: %v", status.RecoveryCodesRemaining)
	}
	if status.RecoveryCodesTotal == nil || *status.RecoveryCodesTotal != totp.RecoveryCodeCount {
		t.Error("spending a code shrank the set")
	}

	// The same code never works twice.
	rec = completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), acct.codes[0])
	wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")

	// A different code still does.
	rec = completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), acct.codes[1])
	if rec.Code != http.StatusOK {
		t.Errorf("an unused recovery code was refused: %d (body %s)", rec.Code, rec.Body.String())
	}
}

// TestRecoveryCodesAreArgon2idAtRest pins the migration's deliberate
// exception: forty bits of entropy behind a bare digest would fall to an
// offline attacker in minutes, so the stored form is argon2id.
func TestRecoveryCodesAreArgon2idAtRest(t *testing.T) {
	t.Parallel()

	_, dsn, handler, acct := newTotpFixture(t, "atrest2fa")
	enableTwoStep(t, handler, &acct)

	stored := storedRecoveryHashes(t, dsn, acct.user.ID)
	if len(stored) != totp.RecoveryCodeCount {
		t.Fatalf("stored %d hashes, want %d", len(stored), totp.RecoveryCodeCount)
	}

	plaintext := map[string]bool{}
	for _, code := range acct.codes {
		plaintext[code] = true
		plaintext[strings.ReplaceAll(code, "-", "")] = true
	}

	matches := 0
	for _, hash := range stored {
		if plaintext[hash] {
			t.Fatal("a recovery code is stored in the clear")
		}
		if !strings.HasPrefix(hash, "$argon2id$") {
			t.Fatalf("stored form %q is not an argon2id hash", hash)
		}
		// A bare SHA-256 would be 64 hex characters or 44 base64 ones; an
		// argon2id PHC string is neither, and the salt makes every hash of
		// the same code different anyway.
		if len(hash) == 64 || len(hash) == 44 {
			t.Fatalf("stored form looks like a bare digest: %q", hash)
		}
		ok, _, err := password.Verify(acct.codes[0], hash)
		if err != nil {
			t.Fatalf("stored hash does not parse: %v", err)
		}
		if ok {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("the first issued code matches %d stored hashes, want exactly 1", matches)
	}
}

// TestDisableTotpIntegration pins the password re-ask and that a refused
// disable changes nothing.
func TestDisableTotpIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "disable2fa")
	// A second device, signed in before the second factor went on: disabling
	// must leave it alone.
	other := login(t, handler, "disable2fa", totpFixturePassword)
	enableTwoStep(t, handler, &acct)

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/disable",
		`{"password":"not the right passphrase"}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusForbidden, "invalid_current_password")
	if !totpStatus(t, handler, acct.cookies).Enabled {
		t.Fatal("a wrong password disabled two-step verification")
	}

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/disable",
		`{"password":""}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusBadRequest, "invalid_request")

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/disable",
		`{"password":"`+totpFixturePassword+`"}`, withSession(acct.cookies)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable: status %d (body %s)", rec.Code, rec.Body.String())
	}

	status := totpStatus(t, handler, acct.cookies)
	if status.Enabled || status.RecoveryCodesRemaining != nil {
		t.Error("two-step verification survived a confirmed disable")
	}

	// Sessions are deliberately untouched: the threat is a hijacked session
	// entrenching itself, and revoking families would only punish the
	// legitimate user's other devices.
	if got := me(t, handler, other); got != http.StatusOK {
		t.Errorf("disabling revoked another session: got %d, want 200", got)
	}
	if got := me(t, handler, acct.cookies); got != http.StatusOK {
		t.Errorf("disabling revoked its own session: got %d, want 200", got)
	}

	// A recovery code from the void set is worthless.
	rec = completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), acct.codes[0])
	wantError(t, rec, http.StatusUnauthorized, "not_authenticated")

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/disable",
		`{"password":"`+totpFixturePassword+`"}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusConflict, "totp_not_enabled")
}

// TestRegenerateRecoveryCodesIntegration pins that regeneration replaces the
// entire previous set and re-asks for the password.
func TestRegenerateRecoveryCodesIntegration(t *testing.T) {
	t.Parallel()

	store, _, handler, acct := newTotpFixture(t, "regen2fa")

	// Off, it is refused outright.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/recovery-codes",
		`{"password":"`+totpFixturePassword+`"}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusConflict, "totp_not_enabled")

	enableTwoStep(t, handler, &acct)

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/recovery-codes",
		`{"password":"the wrong passphrase entirely"}`, withSession(acct.cookies)))
	wantError(t, rec, http.StatusForbidden, "invalid_current_password")

	rec = doHandler(t, handler, request(http.MethodPost, "/api/v1/users/me/totp/recovery-codes",
		`{"password":"`+totpFixturePassword+`"}`, withSession(acct.cookies)))
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate: status %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("regenerate Cache-Control is %q, want no-store", got)
	}

	var fresh api.RecoveryCodes
	if err := json.Unmarshal(rec.Body.Bytes(), &fresh); err != nil {
		t.Fatalf("regenerate body is not RecoveryCodes: %v", err)
	}
	if len(fresh.Codes) != totp.RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(fresh.Codes), totp.RecoveryCodeCount)
	}

	// Every previous code is void; none is reissued. Sign-in is tried with a
	// sample rather than all ten, because each refusal costs a full pass of
	// argon2 comparisons.
	previous := map[string]bool{}
	for _, code := range acct.codes {
		previous[code] = true
	}
	for _, code := range acct.codes[:2] {
		rec := completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), code)
		wantError(t, rec, http.StatusUnauthorized, "invalid_totp_code")
	}
	for _, code := range fresh.Codes {
		if previous[code] {
			t.Errorf("regeneration reissued %q", code)
		}
	}
	if rec := completeLogin(t, handler, mintChallenge(t, store, acct.user.ID), fresh.Codes[0]); rec.Code != http.StatusOK {
		t.Errorf("a fresh recovery code was refused: %d", rec.Code)
	}

	status := totpStatus(t, handler, acct.cookies)
	if status.RecoveryCodesTotal == nil || *status.RecoveryCodesTotal != totp.RecoveryCodeCount {
		t.Error("the replacement set is not the full size")
	}
}

// assertChallengeCookieAttrs pins the attributes the contract fixes for the
// two-step challenge cookie.
func assertChallengeCookieAttrs(t *testing.T, c *http.Cookie) {
	t.Helper()

	if c.Path != challengeCookiePath {
		t.Errorf("challenge cookie path is %q, want %q", c.Path, challengeCookiePath)
	}
	if !c.HttpOnly {
		t.Error("challenge cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Error("challenge cookie is not Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("challenge cookie SameSite is %v, want Strict", c.SameSite)
	}
}

// storedRecoveryHashes reads the code hashes straight out of the database.
func storedRecoveryHashes(t *testing.T, dsn string, userID uuid.UUID) []string {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(ctx); closeErr != nil {
			t.Errorf("close scratch connection: %v", closeErr)
		}
	}()

	rows, err := conn.Query(ctx, `SELECT code_hash FROM user_recovery_codes WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatalf("read recovery hashes: %v", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan recovery hash: %v", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read recovery hashes: %v", err)
	}
	return hashes
}
