package httpserver_test

// Single sign-on (ADR 004 slice 2), driven end to end against a fake
// OpenID Connect provider: an httptest.Server publishing a discovery
// document, a JWKS from a test-generated RSA key, and a token endpoint
// that enforces PKCE and single-use codes, minting real signed ID tokens.
// Every security property named in the ADR gets its negative here: state
// before anything else, nonce against the cookie, issuer/audience/expiry,
// signature only from the JWKS, HS256 refused, the local-account email
// collision refused rather than linked, and provider text never reflected.

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/oidc"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const (
	ssoClientID     = "hamlaneh-web"
	ssoClientSecret = "a test client secret"
	ssoPublicURL    = "https://chat.example.com"
	txnCookieName   = "hamlaneh_oidc"
)

// idpCode is one issued authorization code: what the authorize URL bound it
// to and what the person at the provider proved.
type idpCode struct {
	sub, email, nonce string
	challenge         string // PKCE S256 code_challenge
	redirectURI       string
}

// fakeIDP is a minimal OpenID Connect provider: discovery, JWKS, and a
// token endpoint with PKCE verification and single-use codes. The
// authorize endpoint is never served — tests play the provider's half by
// parsing the authorization URL and calling grant.
type fakeIDP struct {
	srv *httptest.Server

	mu         sync.Mutex
	key        *rsa.PrivateKey
	kid        string
	rogueKey   *rsa.PrivateKey // never in the JWKS
	codes      map[string]idpCode
	codeSeq    int
	down       bool
	tokenCalls int
	// nextClaims overrides claims of the next minted ID token (one shot).
	// A nil VALUE removes that claim instead of setting it to null.
	nextClaims map[string]any
	// signMode of the next token (one shot): "" (RS256, real key),
	// "hs256" (HMAC over the client secret), "rogue" (RS256, unknown key).
	signMode string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate idp key: %v", err)
	}
	rogue, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}
	idp := &fakeIDP{key: key, kid: "key-1", rogueKey: rogue, codes: map[string]idpCode{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", idp.serveDiscovery)
	mux.HandleFunc("GET /jwks", idp.serveJWKS)
	mux.HandleFunc("POST /token", idp.serveToken)
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *fakeIDP) issuer() string { return i.srv.URL }

func (i *fakeIDP) setDown(down bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.down = down
}

// rotateKey replaces the signing key and its kid: the JWKS serves only the
// new key from now on, which is what forces a verifier holding the old set
// to refetch on the unknown kid.
func (i *fakeIDP) rotateKey(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rotate idp key: %v", err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.key = key
	i.kid = "key-" + fmt.Sprint(time.Now().UnixNano())
}

func (i *fakeIDP) overrideNextClaims(claims map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.nextClaims = claims
}

func (i *fakeIDP) signNextWith(mode string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.signMode = mode
}

func (i *fakeIDP) tokenCallCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.tokenCalls
}

func (i *fakeIDP) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	down := i.down
	i.mu.Unlock()
	if down {
		http.Error(w, "provider outage", http.StatusServiceUnavailable)
		return
	}
	writeIdpJSON(w, map[string]any{
		"issuer":                                i.issuer(),
		"authorization_endpoint":                i.issuer() + "/authorize",
		"token_endpoint":                        i.issuer() + "/token",
		"jwks_uri":                              i.issuer() + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (i *fakeIDP) serveJWKS(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	down, key, kid := i.down, i.key, i.kid
	i.mu.Unlock()
	if down {
		http.Error(w, "provider outage", http.StatusServiceUnavailable)
		return
	}
	writeIdpJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}), // 65537
	}}})
}

// grant plays the provider and the person signed in at it: it checks the
// authorization URL's shape (client id, PKCE), then binds a single-use code
// to everything the URL carried plus the granted subject. It returns the
// full callback URL the provider would redirect the browser to.
func (i *fakeIDP) grant(t *testing.T, authorizeURL, sub, email string) string {
	t.Helper()

	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL %q: %v", authorizeURL, err)
	}
	if got := i.issuer() + "/authorize"; !strings.HasPrefix(authorizeURL, got) {
		t.Fatalf("authorize URL %q is not at %q", authorizeURL, got)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != ssoClientID {
		t.Fatalf("authorize client_id %q, want %q", got, ssoClientID)
	}
	// PKCE is pinned on EVERY flow, not in one dedicated test: a grant
	// without a challenge fails whichever test asked for it.
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method %q, want S256", got)
	}
	if q.Get("code_challenge") == "" {
		t.Fatal("authorize URL carries no code_challenge")
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		t.Fatalf("scope %q does not request openid", q.Get("scope"))
	}

	i.mu.Lock()
	i.codeSeq++
	code := fmt.Sprintf("code-%d", i.codeSeq)
	i.codes[code] = idpCode{
		sub: sub, email: email,
		nonce:       q.Get("nonce"),
		challenge:   q.Get("code_challenge"),
		redirectURI: q.Get("redirect_uri"),
	}
	i.mu.Unlock()

	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatalf("redirect_uri %q: %v", q.Get("redirect_uri"), err)
	}
	cb.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	return cb.String()
}

func (i *fakeIDP) serveToken(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenCalls++
	if i.down {
		http.Error(w, "provider outage", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	clientID, clientSecret, hasBasic := r.BasicAuth()
	if !hasBasic {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if clientID != ssoClientID || clientSecret != ssoClientSecret {
		writeIdpTokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}

	code, ok := i.codes[r.PostForm.Get("code")]
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" {
		writeIdpTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// Single use: a replayed code is refused.
	delete(i.codes, r.PostForm.Get("code"))

	if got := r.PostForm.Get("redirect_uri"); got != code.redirectURI {
		writeIdpTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	// PKCE: the verifier must S256-hash to the challenge the flow started
	// with. A confidential client that skipped the verifier fails here.
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
		writeIdpTokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}

	writeIdpJSON(w, map[string]any{
		"access_token": "at-" + fmt.Sprint(i.codeSeq),
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     i.mintIDToken(code),
	})
}

// mintIDToken signs the ID token for a redeemed code. Callers hold i.mu.
func (i *fakeIDP) mintIDToken(code idpCode) string {
	now := time.Now()
	claims := map[string]any{
		"iss":   i.issuer(),
		"sub":   code.sub,
		"aud":   ssoClientID,
		"exp":   now.Add(5 * time.Minute).Unix(),
		"iat":   now.Unix(),
		"nonce": code.nonce,
	}
	if code.email != "" {
		// A directory-backed provider asserts both: the address, and that
		// this subject owns it. Tests that care about the second override
		// it — email_verified: false, or nil to leave the claim out
		// entirely, which is a different thing from false and must be
		// treated the same way.
		claims["email"], claims["email_verified"] = code.email, true
	}
	for k, v := range i.nextClaims {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	i.nextClaims = nil

	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": i.kid}
	signer := func(input []byte) []byte {
		sum := sha256.Sum256(input)
		sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
		if err != nil {
			panic(err)
		}
		return sig
	}
	switch i.signMode {
	case "hs256":
		// The key-confusion swap: symmetric signature over a secret.
		header["alg"] = "HS256"
		signer = func(input []byte) []byte {
			mac := hmac.New(sha256.New, []byte(ssoClientSecret))
			mac.Write(input)
			return mac.Sum(nil)
		}
	case "rogue":
		// Well-formed RS256 under the advertised kid, but by a key the
		// JWKS has never contained.
		signer = func(input []byte) []byte {
			sum := sha256.Sum256(input)
			sig, err := rsa.SignPKCS1v15(rand.Reader, i.rogueKey, crypto.SHA256, sum[:])
			if err != nil {
				panic(err)
			}
			return sig
		}
	}
	i.signMode = ""

	headerJSON, err := json.Marshal(header)
	if err != nil {
		panic(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	input := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	return input + "." + base64.RawURLEncoding.EncodeToString(signer([]byte(input)))
}

func writeIdpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

func writeIdpTokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := fmt.Fprintf(w, `{"error":%q}`, code); err != nil {
		panic(err)
	}
}

// ssoFixture is one test's whole world: a migrated database, a fake
// provider, and a handler configured against it.
type ssoFixture struct {
	store   *storage.Store
	handler http.Handler
	idp     *fakeIDP
	audit   *recordingAudit
}

func newSSOFixture(t *testing.T) *ssoFixture {
	t.Helper()

	store, _ := testdb.New(t)
	idp := newFakeIDP(t)
	svc := oidc.New(oidc.Config{
		Issuer:       idp.issuer(),
		ClientID:     ssoClientID,
		ClientSecret: ssoClientSecret,
		ProviderName: "FakeIdP",
		RedirectURL:  ssoPublicURL + "/api/v1/auth/oidc/callback",
	})
	audit := &recordingAudit{}
	return &ssoFixture{
		store:   store,
		handler: httpserver.Handler(store, httpserver.WithSSO(svc), httpserver.WithAudit(audit)),
		idp:     idp,
		audit:   audit,
	}
}

// newLinkedUser creates an account already linked to the provider identity
// sub, the state slice 2 signs in: linking-by-admin-of-own-account happened
// earlier (or via the Settings flow under test elsewhere).
func (f *ssoFixture) newLinkedUser(ctx context.Context, t *testing.T, username, email, sub string) storage.User {
	t.Helper()
	user := f.newPasswordUser(ctx, t, username, email)
	if err := f.store.LinkOidcIdentity(ctx, user.ID, f.idp.issuer(), sub, nil); err != nil {
		t.Fatalf("link fixture identity: %v", err)
	}
	return user
}

func (f *ssoFixture) newPasswordUser(ctx context.Context, t *testing.T, username, email string) storage.User {
	t.Helper()
	nu := storage.NewUser{
		Username:     username,
		PasswordHash: password.Hash(totpFixturePassword),
		Locale:       "en",
	}
	if email != "" {
		nu.Email = &email
	}
	user, err := f.store.CreateUser(ctx, nu)
	if err != nil {
		t.Fatalf("create fixture user %s: %v", username, err)
	}
	return user
}

// startSSO drives GET /auth/oidc/start: the 302 to the provider plus the
// transaction cookie the callback must present.
func startSSO(t *testing.T, handler http.Handler, mods ...func(*http.Request)) (string, *http.Cookie) {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/auth/oidc/start", "", mods...))
	if rec.Code != http.StatusFound {
		t.Fatalf("sso start: got status %d, want 302 (body %s)", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location"), cookieByName(t, responseCookies(rec), txnCookieName)
}

// completeSSO drives the callback URL with the transaction cookie attached
// (nil sends none).
func completeSSO(t *testing.T, handler http.Handler, callbackURL string, txn *http.Cookie, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	withTxn := func(r *http.Request) {
		if txn != nil {
			r.AddCookie(&http.Cookie{Name: txn.Name, Value: txn.Value})
		}
	}
	return doHandler(t, handler, request(http.MethodGet, callbackURL, "", append([]func(*http.Request){withTxn}, mods...)...))
}

// signInSSO walks the whole happy flow for sub and returns the callback
// response.
func signInSSO(t *testing.T, f *ssoFixture, sub, email string) *httptest.ResponseRecorder {
	t.Helper()
	authorizeURL, txn := startSSO(t, f.handler)
	return completeSSO(t, f.handler, f.idp.grant(t, authorizeURL, sub, email), txn)
}

// wantSSORedirect asserts a callback outcome: the 302 target, and that the
// single-use transaction cookie was cleared.
func wantSSORedirect(t *testing.T, rec *httptest.ResponseRecorder, target string) {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("got status %d, want 302 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != target {
		t.Errorf("redirected to %q, want %q", got, target)
	}
	cleared := cookieByName(t, responseCookies(rec), txnCookieName)
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Error("transaction cookie was not cleared; it must be single-use")
	}
}

// wantSSOFailure asserts a refused callback: the fixed code in the redirect
// and NO session cookie minted.
func wantSSOFailure(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	wantSSORedirect(t, rec, "/?sso_error="+code)
	for _, c := range responseCookies(rec) {
		if c.Name == session.AccessCookie || c.Name == session.RefreshCookie {
			t.Errorf("a refused callback set session cookie %q", c.Name)
		}
	}
}

// ssoSession extracts the session cookies a successful callback set.
func ssoSession(t *testing.T, rec *httptest.ResponseRecorder) sessionCookies {
	t.Helper()
	cookies := responseCookies(rec)
	return sessionCookies{
		access:  cookieByName(t, cookies, session.AccessCookie).Value,
		refresh: cookieByName(t, cookies, session.RefreshCookie).Value,
		csrf:    cookieByName(t, cookies, session.CSRFCookie).Value,
	}
}

// signInEvents filters the audit log down to completed sign-ins.
func signInEvents(rec *recordingAudit) []httpserver.AuditEvent {
	out := []httpserver.AuditEvent{}
	for _, ev := range rec.snapshot() {
		if ev.Action == "user.signed_in" {
			out = append(out, ev)
		}
	}
	return out
}

// TestOidcSignInIntegration walks the happy path: a linked account signs in
// through the provider and ends with the same session a password login
// mints, audited with its own method.
func TestOidcSignInIntegration(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	user := f.newLinkedUser(ctx, t, "ssouser", "ssouser@corp.example", "sub-ssouser")

	rec := signInSSO(t, f, "sub-ssouser", "ssouser@corp.example")
	wantSSORedirect(t, rec, "/")

	sc := ssoSession(t, rec)
	me := currentUser(t, f.handler, sc)
	if me.Id != user.ID {
		t.Errorf("signed in as %s, want %s", me.Id, user.ID)
	}
	if !me.SsoLinked {
		t.Error("users/me does not report sso_linked for a linked account")
	}
	if me.TotpEnrollmentRequired {
		t.Error("session flagged for enrolment with no org policy on")
	}

	events := signInEvents(f.audit)
	if len(events) != 1 {
		t.Fatalf("recorded %d user.signed_in events, want 1", len(events))
	}
	if events[0].ActorID != user.ID || events[0].Detail["method"] != "sso" {
		t.Errorf("signed_in actor=%s method=%v, want actor=%s method=sso",
			events[0].ActorID, events[0].Detail["method"], user.ID)
	}
}

// TestOidcStartShape pins the authorization request's parts the grant
// helper does not already enforce: the transaction cookie's exact
// attributes, and that redirect_uri comes from configuration even when the
// request lies about its Host — a header an attacker chooses.
func TestOidcStartShape(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	authorizeURL, txn := startSSO(t, f.handler, func(r *http.Request) {
		r.Host = "evil.example"
		r.Header.Set("X-Forwarded-Host", "evil.example")
	})

	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL: %v", err)
	}
	if got := u.Query().Get("redirect_uri"); got != ssoPublicURL+"/api/v1/auth/oidc/callback" {
		t.Errorf("redirect_uri %q leaked the request host, want the configured %q",
			got, ssoPublicURL+"/api/v1/auth/oidc/callback")
	}

	// The one deliberate SameSite=Lax cookie in the codebase: the callback
	// is a top-level cross-site navigation. Everything else stays strict.
	if txn.SameSite != http.SameSiteLaxMode {
		t.Errorf("transaction cookie SameSite %v, want Lax", txn.SameSite)
	}
	if !txn.HttpOnly || !txn.Secure {
		t.Error("transaction cookie must be HttpOnly and Secure")
	}
	if txn.Path != "/api/v1/auth/oidc/callback" {
		t.Errorf("transaction cookie path %q, want the callback path", txn.Path)
	}
}

// TestOidcUnknownIdentityRefused pins what a default instance does: an
// identity matching nobody creates nothing (just-in-time provisioning is off
// unless an administrator turns it on — oidc_jit_test.go covers it on), and
// an email collision with a local password account is REFUSED rather than
// linked, whatever that setting says — the takeover ADR 004 rules out.
func TestOidcUnknownIdentityRefused(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	local := f.newPasswordUser(ctx, t, "localacct", "local@corp.example")

	t.Run("email of a local password account", func(t *testing.T) {
		rec := signInSSO(t, f, "sub-collider", "local@corp.example")
		wantSSOFailure(t, rec, "sso_account_exists")
	})
	t.Run("email matching nobody", func(t *testing.T) {
		rec := signInSSO(t, f, "sub-nobody", "nobody@corp.example")
		wantSSOFailure(t, rec, "sso_account_unknown")
	})
	t.Run("no email claim at all", func(t *testing.T) {
		rec := signInSSO(t, f, "sub-quiet", "")
		wantSSOFailure(t, rec, "sso_account_unknown")
	})

	// Nothing was created or linked by any refusal.
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-collider"); err == nil {
		t.Error("the refused email-collision identity was linked anyway")
	}
	after, err := f.store.UserByID(ctx, local.ID)
	if err != nil || after.SsoLinked {
		t.Errorf("local account sso_linked=%v err=%v after refusals, want unlinked", after.SsoLinked, err)
	}
	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("refused callbacks recorded %d sign-ins, want 0", got)
	}
}

// TestOidcCallbackRejections is the negative table for the callback's
// verification order and the token checks: state first (the provider is
// not even consulted when it fails), then the provider error, then the
// exchange whose ID-token verification enforces issuer, audience,
// signature source, algorithm, expiry and nonce.
func TestOidcCallbackRejections(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newLinkedUser(ctx, t, "rejectee", "rejectee@corp.example", "sub-rejectee")

	// Each case runs from its own address so the per-IP flow budget never
	// refuses a case the one before it paid for.
	addr := 0
	fromOwnAddr := func() func(*http.Request) {
		addr++
		return withRemoteAddr(fmt.Sprintf("[2001:db8:1::%x]:443", addr))
	}

	grant := func(t *testing.T, mod func(*http.Request)) (string, *http.Cookie) {
		authorizeURL, txn := startSSO(t, f.handler, mod)
		return f.idp.grant(t, authorizeURL, "sub-rejectee", "rejectee@corp.example"), txn
	}

	t.Run("no transaction cookie", func(t *testing.T) {
		mod := fromOwnAddr()
		callbackURL, _ := grant(t, mod)
		rec := completeSSO(t, f.handler, callbackURL, nil, mod)
		wantSSOFailure(t, rec, "sso_failed")
	})

	t.Run("state mismatch consults nothing", func(t *testing.T) {
		mod := fromOwnAddr()
		callbackURL, txn := grant(t, mod)
		before := f.idp.tokenCallCount()
		tampered := strings.Replace(callbackURL, "state=", "state=wrong", 1)
		rec := completeSSO(t, f.handler, tampered, txn, mod)
		wantSSOFailure(t, rec, "sso_failed")
		if got := f.idp.tokenCallCount(); got != before {
			t.Error("a state mismatch still reached the token endpoint; state must be compared before anything else")
		}
	})

	t.Run("missing state consults nothing", func(t *testing.T) {
		mod := fromOwnAddr()
		callbackURL, txn := grant(t, mod)
		u, err := url.Parse(callbackURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Del("state")
		u.RawQuery = q.Encode()
		before := f.idp.tokenCallCount()
		rec := completeSSO(t, f.handler, u.String(), txn, mod)
		wantSSOFailure(t, rec, "sso_failed")
		if got := f.idp.tokenCallCount(); got != before {
			t.Error("a missing state still reached the token endpoint")
		}
	})

	t.Run("provider error is logged not reflected", func(t *testing.T) {
		mod := fromOwnAddr()
		authorizeURL, txn := startSSO(t, f.handler, mod)
		u, err := url.Parse(authorizeURL)
		if err != nil {
			t.Fatal(err)
		}
		// The provider denies: same state, no code, hostile description.
		cb, err := url.Parse(u.Query().Get("redirect_uri"))
		if err != nil {
			t.Fatal(err)
		}
		cb.RawQuery = url.Values{
			"state":             {u.Query().Get("state")},
			"error":             {"access_denied"},
			"error_description": {`<script>alert("pwned")</script>`},
		}.Encode()
		rec := completeSSO(t, f.handler, cb.String(), txn, mod)
		wantSSOFailure(t, rec, "sso_failed")
		if loc := rec.Header().Get("Location"); strings.Contains(loc, "access_denied") ||
			strings.Contains(strings.ToLower(loc), "script") {
			t.Errorf("provider-supplied text reached the redirect URL: %q", loc)
		}
	})

	t.Run("missing code", func(t *testing.T) {
		mod := fromOwnAddr()
		callbackURL, txn := grant(t, mod)
		u, err := url.Parse(callbackURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Del("code")
		u.RawQuery = q.Encode()
		rec := completeSSO(t, f.handler, u.String(), txn, mod)
		wantSSOFailure(t, rec, "sso_failed")
	})

	tokenNegatives := []struct {
		name    string
		arrange func()
	}{
		{"nonce mismatch", func() { f.idp.overrideNextClaims(map[string]any{"nonce": "someone else's flow"}) }},
		{"hs256 signature refused", func() { f.idp.signNextWith("hs256") }},
		{"signature from outside the jwks", func() { f.idp.signNextWith("rogue") }},
		{"issuer mismatch", func() { f.idp.overrideNextClaims(map[string]any{"iss": f.idp.issuer() + "/tenant2"}) }},
		{"audience is somebody else", func() { f.idp.overrideNextClaims(map[string]any{"aud": "another-client"}) }},
		{"expired token", func() { f.idp.overrideNextClaims(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}) }},
	}
	for _, tc := range tokenNegatives {
		t.Run(tc.name, func(t *testing.T) {
			mod := fromOwnAddr()
			callbackURL, txn := grant(t, mod)
			tc.arrange()
			rec := completeSSO(t, f.handler, callbackURL, txn, mod)
			wantSSOFailure(t, rec, "sso_failed")
		})
	}

	t.Run("replayed callback", func(t *testing.T) {
		mod := fromOwnAddr()
		callbackURL, txn := grant(t, mod)
		first := completeSSO(t, f.handler, callbackURL, txn, mod)
		wantSSORedirect(t, first, "/")
		replay := completeSSO(t, f.handler, callbackURL, txn, mod)
		wantSSOFailure(t, replay, "sso_failed")
	})

	// Exactly one sign-in came out of all of that: the replay case's first
	// pass. Every rejection minted nothing.
	if got := len(signInEvents(f.audit)); got != 1 {
		t.Errorf("the rejection table recorded %d sign-ins, want 1", got)
	}
}

// TestOidcKeyRotationRefetch pins the unknown-kid refetch: after the
// provider rotates its JWKS, a token under the new kid still verifies
// because the key set is refetched rather than trusted stale.
func TestOidcKeyRotationRefetch(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newLinkedUser(ctx, t, "rotator", "rotator@corp.example", "sub-rotator")

	wantSSORedirect(t, signInSSO(t, f, "sub-rotator", ""), "/")
	f.idp.rotateKey(t)
	wantSSORedirect(t, signInSSO(t, f, "sub-rotator", ""), "/")
}

// TestOidcTwoStepChallenge: an account with an activated second factor gets
// the EXISTING challenge from the callback — same storage, same cookie —
// and lands on the challenge screen holding no session. Completing it goes
// through the same endpoint as a password sign-in.
func TestOidcTwoStepChallenge(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	user := f.newLinkedUser(ctx, t, "sso2fa", "sso2fa@corp.example", "sub-sso2fa")

	// Enrolment needs a signed-in session, and that password login is
	// itself an audited sign-in — the baseline the assertions below count
	// from.
	acct := twoStepAccount{user: user, cookies: login(t, f.handler, "sso2fa", totpFixturePassword)}
	enableTwoStep(t, f.handler, &acct)
	baseline := len(signInEvents(f.audit))

	rec := signInSSO(t, f, "sub-sso2fa", "")
	// The application root with the method parameter — never a distinct
	// path, so the server's client-route list stays closed (the contract).
	wantSSORedirect(t, rec, "/?sso=totp")
	challenge := cookieByName(t, responseCookies(rec), challengeCookieName)
	if challenge.Value == "" {
		t.Fatal("challenge redirect set no challenge cookie")
	}
	if challenge.Path != challengeCookiePath {
		t.Errorf("challenge cookie path %q, want %q — the SSO path must reuse the cookie verbatim",
			challenge.Path, challengeCookiePath)
	}
	for _, c := range responseCookies(rec) {
		if c.Name == session.AccessCookie || c.Name == session.RefreshCookie {
			t.Errorf("challenge redirect minted session cookie %q", c.Name)
		}
	}
	if got := len(signInEvents(f.audit)); got != baseline {
		t.Errorf("a challenged callback recorded a sign-in; the sign-in is not complete")
	}

	done := completeLogin(t, f.handler, challenge.Value, nextCode(t, acct.secret))
	if done.Code != http.StatusOK {
		t.Fatalf("completing the challenge: got %d (body %s)", done.Code, done.Body.String())
	}
	events := signInEvents(f.audit)
	if len(events) != baseline+1 {
		t.Fatalf("after completion: %d sign-ins, want %d", len(events), baseline+1)
	}
	if got := events[len(events)-1].Detail["method"]; got != "totp" {
		t.Errorf("completed sign-in method %v, want totp", got)
	}
}

// TestOidcSignInUnderOrgPolicy: the org's require_totp binds to an SSO mint
// exactly as to a password mint — the session comes out flagged and gated.
func TestOidcSignInUnderOrgPolicy(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newLinkedUser(ctx, t, "ssopolicy", "ssopolicy@corp.example", "sub-ssopolicy")
	requireTotp(ctx, t, f.store)

	rec := signInSSO(t, f, "sub-ssopolicy", "")
	wantSSORedirect(t, rec, "/")
	sc := ssoSession(t, rec)

	if me := currentUser(t, f.handler, sc); !me.TotpEnrollmentRequired {
		t.Error("an SSO session minted under the policy is not flagged; SSO must not bypass org 2FA")
	}
	gated := channelsStatus(t, f.handler, sc)
	if gated.Code != http.StatusForbidden {
		t.Fatalf("flagged SSO session reached channels: got %d, want 403", gated.Code)
	}
	wantError(t, gated, http.StatusForbidden, "totp_enrollment_required")
}

// TestOidcDeactivatedAccountRefused: deactivation closes the SSO door with
// the same generic failure as everything else — account state leaks to
// nobody.
func TestOidcDeactivatedAccountRefused(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	user := f.newLinkedUser(ctx, t, "ssogone", "ssogone@corp.example", "sub-ssogone")
	// The deactivation goes through the admin path, whose last-admin rule
	// needs an admin who can still sign in to exist.
	if _, err := f.store.CreateUser(ctx, storage.NewUser{
		Username: "ssoadmin", PasswordHash: password.Hash(totpFixturePassword), Locale: "en", IsAdmin: true,
	}); err != nil {
		t.Fatalf("create fixture admin: %v", err)
	}
	inactive := false
	if _, err := f.store.UpdateUserAdmin(ctx, user.ID, storage.AdminUserUpdate{IsActive: &inactive}); err != nil {
		t.Fatalf("deactivate fixture user: %v", err)
	}

	wantSSOFailure(t, signInSSO(t, f, "sub-ssogone", ""), "sso_failed")
	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("a deactivated account's callback recorded %d sign-ins, want 0", got)
	}
}

// TestOidcDiscoveryIsLazyAndRetried: a provider that is down yields 503 at
// the door — the server itself booted fine and password login is untouched
// — and the first successful discovery afterwards heals it with no restart.
func TestOidcDiscoveryIsLazyAndRetried(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newPasswordUser(ctx, t, "resilient", "resilient@corp.example")
	f.idp.setDown(true)

	rec := doHandler(t, f.handler, request(http.MethodGet, "/api/v1/auth/oidc/start", ""))
	wantError(t, rec, http.StatusServiceUnavailable, "sso_unavailable")

	// The button still exists: enabled follows configuration, not health.
	info := instanceInfo(t, f.handler)
	if info.Sso == nil || !info.Sso.Enabled {
		t.Error("instance info hides sso while the provider is down; enabled follows configuration")
	}

	// Password sign-in is untouched while the provider is down.
	login(t, f.handler, "resilient", totpFixturePassword)

	f.idp.setDown(false)
	if _, txn := startSSO(t, f.handler); txn.Value == "" {
		t.Error("recovered start set no transaction cookie")
	}
}

// TestOidcLinkAndUnlink walks the Settings machine: link from a signed-in
// session (the account id rides the server-minted cookie, not the
// navigation), the second link refused, unlink, and the audit trail of
// both.
func TestOidcLinkAndUnlink(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	user := f.newPasswordUser(ctx, t, "linker", "linker@corp.example")
	sc := login(t, f.handler, "linker", totpFixturePassword)

	// POST answers the provider URL as JSON plus the transaction cookie.
	rec := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/users/me/oidc", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("link start: got %d (body %s)", rec.Code, rec.Body.String())
	}
	var redirect api.OidcRedirect
	if err := json.Unmarshal(rec.Body.Bytes(), &redirect); err != nil {
		t.Fatalf("link body is not OidcRedirect: %v", err)
	}
	txn := cookieByName(t, responseCookies(rec), txnCookieName)

	// The callback records the identity against the cookie's account — no
	// session cookie is attached here on purpose: the real navigation
	// would not carry one either (SameSite=Strict).
	callbackURL := f.idp.grant(t, redirect.RedirectUrl, "sub-linker", "linker@corp.example")
	done := completeSSO(t, f.handler, callbackURL, txn)
	// A completed link lands bare on the root like a sign-in; the client
	// learns the outcome from users/me.sso_linked, not from the URL.
	wantSSORedirect(t, done, "/")
	for _, c := range responseCookies(done) {
		if c.Name == session.AccessCookie {
			t.Error("a link callback minted a session")
		}
	}

	if me := currentUser(t, f.handler, sc); !me.SsoLinked {
		t.Error("users/me does not report the fresh link")
	}
	linked, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-linker")
	if err != nil || linked.ID != user.ID {
		t.Fatalf("identity resolves to (%v, %v), want %s", linked.ID, err, user.ID)
	}

	// A second link is refused up front.
	again := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/users/me/oidc", "", withSession(sc)))
	wantError(t, again, http.StatusConflict, "sso_already_linked")

	// The freshly linked identity signs in.
	wantSSORedirect(t, signInSSO(t, f, "sub-linker", ""), "/")

	// Unlink, then unlinking again is the honest 404.
	gone := doHandler(t, f.handler, request(http.MethodDelete, "/api/v1/users/me/oidc", "", withSession(sc)))
	if gone.Code != http.StatusNoContent {
		t.Fatalf("unlink: got %d (body %s)", gone.Code, gone.Body.String())
	}
	if me := currentUser(t, f.handler, sc); me.SsoLinked {
		t.Error("users/me still reports a link after unlink")
	}
	wantError(t, doHandler(t, f.handler,
		request(http.MethodDelete, "/api/v1/users/me/oidc", "", withSession(sc))),
		http.StatusNotFound, "sso_not_linked")

	// Both settings actions are audited, attributed to the account.
	var linkedEv, unlinkedEv *httpserver.AuditEvent
	for _, ev := range f.audit.snapshot() {
		switch ev.Action {
		case "sso.linked":
			linkedEv = &ev
		case "sso.unlinked":
			unlinkedEv = &ev
		}
	}
	if linkedEv == nil || linkedEv.ActorID != user.ID || linkedEv.TargetLabel != "linker" {
		t.Errorf("sso.linked event %+v, want actor %s label linker", linkedEv, user.ID)
	}
	if unlinkedEv == nil || unlinkedEv.ActorID != user.ID {
		t.Errorf("sso.unlinked event %+v, want actor %s", unlinkedEv, user.ID)
	}
}

// TestOidcLinkIdentityTakenRefused: the callback refuses to move an
// identity that already belongs to another account — the second half of
// the one-account-per-identity rule, at the API layer.
func TestOidcLinkIdentityTakenRefused(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newLinkedUser(ctx, t, "first", "first@corp.example", "sub-shared")
	second := f.newPasswordUser(ctx, t, "second", "second@corp.example")
	sc := login(t, f.handler, "second", totpFixturePassword)

	rec := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/users/me/oidc", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("link start: got %d", rec.Code)
	}
	var redirect api.OidcRedirect
	if err := json.Unmarshal(rec.Body.Bytes(), &redirect); err != nil {
		t.Fatal(err)
	}
	txn := cookieByName(t, responseCookies(rec), txnCookieName)

	done := completeSSO(t, f.handler, f.idp.grant(t, redirect.RedirectUrl, "sub-shared", ""), txn)
	wantSSOFailure(t, done, "sso_failed")

	after, err := f.store.UserByID(ctx, second.ID)
	if err != nil || after.SsoLinked {
		t.Errorf("second account sso_linked=%v err=%v, want unlinked", after.SsoLinked, err)
	}
}

// TestOidcUnlinkRefusedWithoutPassword: an account whose only way in is the
// identity keeps it; the recovery path is an admin-issued temporary
// password, not a self-inflicted lockout.
func TestOidcUnlinkRefusedWithoutPassword(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()

	// The password-less state SSO/SCIM-created accounts will occupy from
	// slice 3 on; the schema already stores it as the empty sentinel.
	user, err := f.store.CreateUser(ctx, storage.NewUser{Username: "ssoonly", Locale: "en"})
	if err != nil {
		t.Fatalf("create password-less user: %v", err)
	}
	if err := f.store.LinkOidcIdentity(ctx, user.ID, f.idp.issuer(), "sub-ssoonly", nil); err != nil {
		t.Fatalf("link: %v", err)
	}

	accessRaw, accessHash := session.NewToken()
	refreshRaw, refreshHash := session.NewToken()
	if _, err := f.store.CreateSession(ctx, storage.NewSession{
		UserID: user.ID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash: accessHash, RefreshTokenHash: refreshHash,
			AccessTTL: session.AccessTTL, RefreshTTL: session.RefreshTTL,
		},
	}); err != nil {
		t.Fatalf("mint fixture session: %v", err)
	}
	sc := sessionCookies{access: accessRaw, refresh: refreshRaw, csrf: "fixture-csrf"}

	rec := doHandler(t, f.handler, request(http.MethodDelete, "/api/v1/users/me/oidc", "", withSession(sc)))
	wantError(t, rec, http.StatusConflict, "sso_unlink_no_password")
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-ssoonly"); err != nil {
		t.Errorf("the refused unlink removed the identity: %v", err)
	}
}

// instanceInfo reads the public instance document.
func instanceInfo(t *testing.T, handler http.Handler) api.InstanceInfo {
	t.Helper()
	rec := doHandler(t, handler, request(http.MethodGet, "/api/v1/instance", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("instance: got %d", rec.Code)
	}
	var info api.InstanceInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("instance body: %v", err)
	}
	return info
}

// TestInstanceInfoSso pins the sign-in screen's source of truth: the sso
// object says enabled with the configured name, or enabled false when no
// provider exists. Needs no database — the document is static policy.
func TestInstanceInfoSso(t *testing.T) {
	t.Parallel()

	svc := oidc.New(oidc.Config{
		Issuer: "https://idp.example.com", ClientID: "x", ClientSecret: "y",
		ProviderName: "FakeIdP", RedirectURL: ssoPublicURL + "/api/v1/auth/oidc/callback",
	})
	withSSO := instanceInfo(t, httpserver.Handler(&fakeStore{}, httpserver.WithSSO(svc)))
	if withSSO.Sso == nil || !withSSO.Sso.Enabled {
		t.Fatal("configured sso not reported enabled")
	}
	if withSSO.Sso.ProviderName == nil || *withSSO.Sso.ProviderName != "FakeIdP" {
		t.Error("provider name missing from instance info")
	}

	// The contract pairs the fields: enabled true always carries a name,
	// so an operator who set none still gets a label the button can use.
	unnamed := oidc.New(oidc.Config{
		Issuer: "https://idp.example.com", ClientID: "x", ClientSecret: "y",
		RedirectURL: ssoPublicURL + "/api/v1/auth/oidc/callback",
	})
	generic := instanceInfo(t, httpserver.Handler(&fakeStore{}, httpserver.WithSSO(unnamed)))
	if generic.Sso == nil || !generic.Sso.Enabled ||
		generic.Sso.ProviderName == nil || *generic.Sso.ProviderName == "" {
		t.Error("enabled sso without a display name must still carry a generic provider_name")
	}

	without := instanceInfo(t, httpserver.Handler(&fakeStore{}))
	if without.Sso == nil || without.Sso.Enabled {
		t.Fatal("unconfigured sso not reported disabled")
	}
	if without.Sso.ProviderName != nil {
		t.Error("a disabled provider has a name")
	}
}

// TestOidcSignInSessionIsReal closes the loop on "the same mint as any
// other sign-in": the session an SSO callback sets refreshes and logs out
// through the ordinary endpoints.
func TestOidcSignInSessionIsReal(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	f.newLinkedUser(ctx, t, "realsess", "realsess@corp.example", "sub-realsess")

	rec := signInSSO(t, f, "sub-realsess", "")
	wantSSORedirect(t, rec, "/")
	sc := ssoSession(t, rec)

	sc = refreshed(t, f.handler, sc)
	out := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/auth/logout", "", withSession(sc)))
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout of an SSO session: got %d", out.Code)
	}
	if got := me(t, f.handler, sc); got != http.StatusUnauthorized {
		t.Errorf("SSO session survived logout: got %d, want 401", got)
	}
}

// forgeTxnField forges a transaction cookie: it decodes the real one the
// server minted, sets one JSON field, and re-encodes. It works on raw JSON
// precisely so a test expresses an attack the same way against the fixed
// code and against a mutated version — the mutation checks depend on that.
func forgeTxnField(t *testing.T, txnValue, key, value string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(txnValue)
	if err != nil {
		t.Fatalf("decode transaction cookie: %v", err)
	}
	var m map[string]any
	if unmarshalErr := json.Unmarshal(raw, &m); unmarshalErr != nil {
		t.Fatalf("unmarshal transaction cookie: %v", unmarshalErr)
	}
	m[key] = value
	forged, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal forged cookie: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(forged)
}

// injectLinkUserID splices a link_user_id naming another account into the
// cookie — the pre-fix takeover, which the current code ignores.
func injectLinkUserID(t *testing.T, txnValue, userID string) string {
	t.Helper()
	return forgeTxnField(t, txnValue, "link_user_id", userID)
}

// mustQueryParam extracts one required query parameter from a URL.
func mustQueryParam(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL %q: %v", rawURL, err)
	}
	v := u.Query().Get(key)
	if v == "" {
		t.Fatalf("URL %q has no %s parameter", rawURL, key)
	}
	return v
}

// withState returns an authorize URL with its state parameter replaced —
// how an attacker drives their own flow carrying a victim's observed state.
func withState(t *testing.T, authorizeURL, state string) string {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL %q: %v", authorizeURL, err)
	}
	q := u.Query()
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

// TestOidcLinkFixationRefused is the account-takeover regression. An
// attacker with their own identity at the provider drives a real sign-in
// flow as themselves, then hand-builds the transaction cookie to name a
// victim's account — exactly what a curl Cookie header can do, since
// HttpOnly/Secure/SameSite constrain a browser, not a script. The identity
// must NOT link to the victim, and no victim session may be minted.
//
// Mutation-checked: reverting to the cookie-carried link intent turns this
// red (the attacker's identity links to the victim and a session is set),
// which is the takeover this fix removes.
func TestOidcLinkFixationRefused(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	// The victim: an admin whose UUID an attacker reads from any member list.
	victim := f.newPasswordUser(ctx, t, "victimadmin", "victim@corp.example")

	// The attacker starts a REAL flow as themselves: a legitimate
	// state/nonce/verifier cookie, and a real code for their own identity.
	authorizeURL, txn := startSSO(t, f.handler)
	forged := injectLinkUserID(t, txn.Value, victim.ID.String())
	callbackURL := f.idp.grant(t, authorizeURL, "sub-attacker", "attacker@corp.example")

	// No session cookie — the attack runs entirely in the attacker's own
	// client — and the forged transaction cookie naming the victim.
	rec := completeSSO(t, f.handler, callbackURL,
		&http.Cookie{Name: txnCookieName, Value: forged})

	// Nothing links to the victim, and no session is minted for them: with
	// no pending-link row the callback is a plain sign-in, and the
	// attacker's own identity is unknown (registration-off), so it refuses.
	wantSSOFailure(t, rec, "sso_account_unknown")

	after, err := f.store.UserByID(ctx, victim.ID)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if after.SsoLinked {
		t.Fatal("TAKEOVER: the attacker's identity linked to the victim's account")
	}
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-attacker"); err == nil {
		t.Error("the attacker's identity was recorded against some account")
	}
	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("the forged callback recorded %d sign-ins, want 0", got)
	}
}

// TestOidcLinkStartedByOneAccountCannotCompleteAgainstAnother pins that the
// pending-link target is fixed by the authenticated starter and cannot be
// steered elsewhere at the callback. Account A starts a link (creating its
// own pending row); an attacker replays the callback with A's forged cookie
// naming B. The identity links to A — the row's owner — never to B.
func TestOidcLinkStartedByOneAccountCannotCompleteAgainstAnother(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	starter := f.newPasswordUser(ctx, t, "starter", "starter@corp.example")
	other := f.newPasswordUser(ctx, t, "other", "other@corp.example")
	sc := login(t, f.handler, "starter", totpFixturePassword)

	// A begins a genuine link.
	rec := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/users/me/oidc", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("link start: got %d (body %s)", rec.Code, rec.Body.String())
	}
	var redirect api.OidcRedirect
	if err := json.Unmarshal(rec.Body.Bytes(), &redirect); err != nil {
		t.Fatal(err)
	}
	txn := cookieByName(t, responseCookies(rec), txnCookieName)
	forged := injectLinkUserID(t, txn.Value, other.ID.String())

	done := completeSSO(t, f.handler, f.idp.grant(t, redirect.RedirectUrl, "sub-starter", ""),
		&http.Cookie{Name: txnCookieName, Value: forged})
	wantSSORedirect(t, done, "/")

	// The identity landed on A (the pending row's owner), not on B.
	linked, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-starter")
	if err != nil {
		t.Fatalf("identity not linked at all: %v", err)
	}
	if linked.ID != starter.ID {
		t.Fatalf("identity linked to %s, want the starter %s", linked.ID, starter.ID)
	}
	if b, err := f.store.UserByID(ctx, other.ID); err != nil || b.SsoLinked {
		t.Errorf("the forged target account got the identity: sso_linked=%v err=%v", b.SsoLinked, err)
	}
}

// TestOidcLinkObservedStateWithoutSecretRefused is the second-factor
// regression the residual finding demanded. A victim's outstanding state is
// observable — it rides in the address bar, browser history, any client-side
// proxy log, and at the provider — so an attacker can learn it. That must not
// be enough to complete a link: the pending row is also gated on a secret
// that only ever lived in the victim's cookie, which the attacker does not
// have.
//
// The attack, faithfully: the victim starts a genuine link (a pending row
// now exists). The attacker learns the state, runs their OWN flow for a
// self-consistent nonce/verifier, splices the victim's state into their
// authorize URL, grants their OWN identity, and forges a cookie carrying the
// victim's state but no secret. The link must not happen.
//
// Mutation-checked: matching the consuming DELETE on state alone (dropping
// the secret) turns this red — the attacker's identity links to the victim.
func TestOidcLinkObservedStateWithoutSecretRefused(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()

	// The victim starts a real link from their session.
	victim := f.newPasswordUser(ctx, t, "victim", "victim@corp.example")
	vsc := login(t, f.handler, "victim", totpFixturePassword)
	vrec := doHandler(t, f.handler, request(http.MethodPost, "/api/v1/users/me/oidc", "", withSession(vsc)))
	if vrec.Code != http.StatusOK {
		t.Fatalf("victim link start: got %d (body %s)", vrec.Code, vrec.Body.String())
	}
	var vredirect api.OidcRedirect
	if err := json.Unmarshal(vrec.Body.Bytes(), &vredirect); err != nil {
		t.Fatal(err)
	}
	victimTxn := cookieByName(t, responseCookies(vrec), txnCookieName)
	victimState := mustQueryParam(t, vredirect.RedirectUrl, "state")

	// The victim's password login above is itself an audited sign-in, so the
	// forged callback must add NOTHING to this baseline.
	baseline := len(signInEvents(f.audit))

	// The attacker observes only the state. Their own /start gives a
	// consistent nonce/verifier cookie; they splice the victim's state into
	// their authorize URL and grant their OWN identity against it.
	attackerAuthorize, attackerTxn := startSSO(t, f.handler)
	spliced := withState(t, attackerAuthorize, victimState)
	callbackURL := f.idp.grant(t, spliced, "sub-attacker", "attacker@corp.example")

	// Forge the cookie: the victim's state, the attacker's own
	// nonce/verifier, and no link secret — they never held it.
	forged := forgeTxnField(t, attackerTxn.Value, "state", victimState)
	rec := completeSSO(t, f.handler, callbackURL,
		&http.Cookie{Name: txnCookieName, Value: forged})

	// No pending row matched (the secret was absent), so it is a plain
	// sign-in of an unknown identity, refused — nothing linked to the victim.
	wantSSOFailure(t, rec, "sso_account_unknown")

	after, err := f.store.UserByID(ctx, victim.ID)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if after.SsoLinked {
		t.Fatal("TAKEOVER: an observed state alone linked the attacker's identity to the victim")
	}
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-attacker"); err == nil {
		t.Error("the attacker's identity was recorded against some account")
	}
	if got := len(signInEvents(f.audit)); got != baseline {
		t.Errorf("the forged callback recorded %d sign-ins over the baseline %d, want none", got-baseline, baseline)
	}

	// The wrong-secret attempt must not have consumed the victim's pending
	// row: the victim can still complete their own link with the real cookie.
	vdone := completeSSO(t, f.handler,
		f.idp.grant(t, vredirect.RedirectUrl, "sub-victim", "victim@corp.example"),
		&http.Cookie{Name: txnCookieName, Value: victimTxn.Value})
	wantSSORedirect(t, vdone, "/")
	if healed, err := f.store.UserByID(ctx, victim.ID); err != nil || !healed.SsoLinked {
		t.Errorf("the attack consumed the victim's pending link: sso_linked=%v err=%v", healed.SsoLinked, err)
	}
}
