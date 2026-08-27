// Package oidc is the server's OpenID Connect relying party (ADR 004): one
// provider per instance, configured by environment, validated at startup,
// discovered lazily.
//
// The protocol work — discovery, the authorization URL, the code exchange,
// and ID-token verification — is github.com/coreos/go-oidc and
// golang.org/x/oauth2. This package holds the configuration, the lazy
// discovery, and the checks the libraries leave to the caller (the nonce).
// It deliberately re-implements none of JOSE: signature verification is
// where hand-rolled OIDC dies (ADR 004).
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Environment variables that configure single sign-on. Setting the issuer is
// what turns it on; a half-configured set stops startup, the same rule the
// mailer enforces. A zero-config install sets none of them and has no SSO.
const (
	// EnvIssuer is the provider's issuer URL, byte-exact: discovery is
	// refused when the document's issuer differs, and every ID token's iss
	// claim is compared against it.
	EnvIssuer = "HAMLANEH_OIDC_ISSUER"
	// EnvClientID is the client id registered at the provider.
	EnvClientID = "HAMLANEH_OIDC_CLIENT_ID"
	// EnvClientSecret is the confidential client's secret.
	EnvClientSecret = "HAMLANEH_OIDC_CLIENT_SECRET" // #nosec G101 -- the environment variable's NAME; its value is never in the repo
	// EnvProviderName optionally names the provider on the sign-in button
	// ("Sign in with Okta"). Absent, the client shows its generic wording.
	EnvProviderName = "HAMLANEH_OIDC_PROVIDER_NAME"
)

// CallbackPath is the one registered redirect path. The full redirect URI is
// built from the configured public URL and this constant — never from a
// request's Host header, which an attacker chooses.
const CallbackPath = "/api/v1/auth/oidc/callback"

// requestTimeout bounds each outbound provider request (discovery, token
// exchange, JWKS fetch). It lives on the HTTP client rather than a context:
// go-oidc keeps the discovery context for later JWKS refetches, so a
// deadline there would poison every verification after it expired.
const requestTimeout = 10 * time.Second

// Config is a validated single sign-on configuration.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// ProviderName is what the sign-in button calls the provider; empty
	// means the client's generic wording.
	ProviderName string
	// RedirectURL is the absolute callback URL, built from the instance's
	// public URL at validation time.
	RedirectURL string
}

// ConfigFromEnv reads the SSO configuration from the process environment.
// An unset issuer with the rest unset yields (nil-config, false): SSO off,
// the supported zero-config state. Any partial set is an error — the mailer
// precedent: misconfiguration surfaces at deploy, not at somebody's sign-in.
//
// publicURL is the instance's public origin (HAMLANEH_PUBLIC_URL, already
// validated by the caller's own use); the redirect URI is built from it, so
// SSO cannot be configured on an instance that does not know its origin.
func ConfigFromEnv(publicURL string) (Config, bool, error) {
	cfg := Config{
		Issuer:       strings.TrimSpace(os.Getenv(EnvIssuer)),
		ClientID:     strings.TrimSpace(os.Getenv(EnvClientID)),
		ClientSecret: os.Getenv(EnvClientSecret),
		ProviderName: strings.TrimSpace(os.Getenv(EnvProviderName)),
	}
	if cfg.Issuer == "" && cfg.ClientID == "" && cfg.ClientSecret == "" && cfg.ProviderName == "" {
		return Config{}, false, nil
	}

	var missing []string
	for _, v := range []struct{ name, value string }{
		{EnvIssuer, cfg.Issuer},
		{EnvClientID, cfg.ClientID},
		{EnvClientSecret, cfg.ClientSecret},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, false, fmt.Errorf(
			"single sign-on is half-configured: set %s as well, or unset the others",
			strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(cfg.Issuer, "https://") && !strings.HasPrefix(cfg.Issuer, "http://") {
		return Config{}, false, fmt.Errorf("%s must be an http:// or https:// URL, got %q", EnvIssuer, cfg.Issuer)
	}

	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if base == "" {
		return Config{}, false, fmt.Errorf(
			"single sign-on needs the instance's public URL to build its redirect URI: set HAMLANEH_PUBLIC_URL")
	}
	cfg.RedirectURL = base + CallbackPath
	return cfg, true, nil
}

// Service is the relying party for the one configured provider. Safe for
// concurrent use.
type Service struct {
	cfg    Config
	client *http.Client

	// mu guards the memoized provider. Discovery runs under it, so
	// concurrent first requests make one fetch, not a stampede.
	mu       sync.Mutex
	provider *gooidc.Provider
}

// New builds the service for a validated configuration. No network happens
// here: discovery is lazy and retried, so a provider that is down does not
// stop the chat server booting (ADR 004).
func New(cfg Config) *Service {
	return &Service{cfg: cfg, client: &http.Client{Timeout: requestTimeout}}
}

// FromEnv builds the service from the environment, or nil when SSO is not
// configured. A half-configured set is an error that should stop startup.
func FromEnv(publicURL string) (*Service, error) {
	cfg, configured, err := ConfigFromEnv(publicURL)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}
	return New(cfg), nil
}

// defaultProviderName labels the sign-in button when the operator sets no
// display name. The contract makes provider_name present whenever SSO is
// enabled, so a client never has to render a button it cannot label.
const defaultProviderName = "SSO"

// ProviderName is what the sign-in button calls the provider. Never empty
// on a configured service: an unset display name yields the generic
// default rather than a label the client would have to invent.
func (s *Service) ProviderName() string {
	if s.cfg.ProviderName == "" {
		return defaultProviderName
	}
	return s.cfg.ProviderName
}

// discover returns the provider, fetching its discovery document on first
// use. Failure is not cached: every call retries until the provider answers
// once, and the memo then lasts the process. The rate limit on the SSO
// endpoints is what bounds retry traffic while the provider is down.
//
// It deliberately takes no context: see the note on NewProvider below.
//
// go-oidc refuses a discovery document whose issuer is not byte-identical
// to the configured one, which is the first half of the issuer check (the
// second is the ID token's iss claim against the provider's issuer).
func (s *Service) discover() (*gooidc.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, nil
	}

	// The context handed to NewProvider is kept by the provider's key set
	// for every later JWKS fetch, so it must outlive the request that
	// happened to run first and carry no deadline; per-request bounding
	// lives on the client's timeout instead.
	provider, err := gooidc.NewProvider(gooidc.ClientContext(context.Background(), s.client), s.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	s.provider = provider
	return provider, nil
}

// oauthConfig assembles the oauth2 half for a discovered provider.
func (s *Service) oauthConfig(provider *gooidc.Provider) oauth2.Config {
	return oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		// Built from configuration, never from a request header.
		RedirectURL: s.cfg.RedirectURL,
		Scopes:      []string{gooidc.ScopeOpenID, "email"},
	}
}

// AuthRequest is one minted authorization request: the URL to send the
// browser to, and the three secrets the callback must hold to complete it.
type AuthRequest struct {
	// State binds the callback to the browser that started the flow; the
	// callback compares it against the state query parameter before
	// anything else.
	State string
	// Nonce binds the ID token to this flow; the callback compares the
	// token's nonce claim against it.
	Nonce string
	// Verifier is the PKCE code verifier. PKCE is layered on even though
	// this is a confidential client: it costs nothing and closes code
	// injection outright.
	Verifier string
	// URL is the provider's authorization endpoint with every parameter set.
	URL string
}

// NewAuthRequest mints a fresh authorization request. An error means the
// provider could not be discovered — the caller answers 503 and password
// sign-in is untouched.
func (s *Service) NewAuthRequest(ctx context.Context) (AuthRequest, error) {
	provider, err := s.discover()
	if err != nil {
		return AuthRequest{}, err
	}

	req := AuthRequest{
		State:    randomToken(),
		Nonce:    randomToken(),
		Verifier: oauth2.GenerateVerifier(),
	}
	cfg := s.oauthConfig(provider)
	req.URL = cfg.AuthCodeURL(req.State,
		gooidc.Nonce(req.Nonce),
		oauth2.S256ChallengeOption(req.Verifier),
	)
	return req, nil
}

// Identity is what a verified ID token asserts: who, at which provider.
type Identity struct {
	// Issuer is the configured issuer — the token's iss was verified
	// byte-exact against it before this struct exists.
	Issuer string
	// Subject is the provider's stable identifier for the person. With
	// Issuer it is the whole login key (migration 0012).
	Subject string
	// Email is the token's email claim, or empty. Recorded at link time as
	// a forensic note; on its own it never decides who signs in.
	Email string
	// EmailVerified is the provider asserting that THIS subject owns that
	// address. It is a separate statement from the address itself, and the
	// only one that binds the two: a token carrying an email says what the
	// person typed into a profile somewhere, and nothing more.
	//
	// It is false when the claim is absent, false, or not a JSON boolean.
	// Absent is not true — the one caller that reads it lets an email
	// attach an identity to an account that already exists, so a provider
	// that did not say "verified" has not said it.
	EmailVerified bool
}

// errNonceMismatch reports an ID token whose nonce is not this flow's.
var errNonceMismatch = errors.New("oidc: id token nonce does not match the transaction")

// Exchange redeems an authorization code and verifies the resulting ID
// token: issuer byte-exact, audience contains our client id, signature by a
// key from the issuer's JWKS (refetched on an unknown kid), expiry strict,
// and the nonce claim equal to this flow's nonce. Symmetric algorithms are
// excluded by construction, so an HS256 token — the classic key-confusion
// swap — fails before any key is consulted.
func (s *Service) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	provider, err := s.discover()
	if err != nil {
		return Identity{}, err
	}

	// The request context bounds the exchange; the client carries its own
	// per-request timeout as well.
	cfg := s.oauthConfig(provider)
	token, err := cfg.Exchange(
		gooidc.ClientContext(ctx, s.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("oidc code exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Identity{}, errors.New("oidc: token response carries no id_token")
	}

	idToken, err := provider.VerifierContext(gooidc.ClientContext(ctx, s.client), &gooidc.Config{
		ClientID: s.cfg.ClientID,
		// Asymmetric signatures only. HS256 and its siblings key the
		// signature on the client secret, and accepting them alongside a
		// public JWKS is the RSA/HMAC confusion class; nothing in a JWKS
		// can verify them, and refusing the algorithm outright is cheaper
		// and louder than failing key lookup.
		SupportedSigningAlgs: []string{gooidc.RS256, gooidc.ES256, gooidc.EdDSA},
	}).Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("oidc id token verification: %w", err)
	}

	// The verifier proved the token is from our issuer for our client; the
	// nonce ties it to THIS flow. Compared against the transaction cookie's
	// value, constant-time, exactly like the state — present-but-different
	// must fail the same way absent does.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return Identity{}, errNonceMismatch
	}
	if idToken.Subject == "" {
		return Identity{}, errors.New("oidc: id token carries no subject")
	}

	var claims struct {
		Email string `json:"email"`
		// Deliberately any, not bool: providers exist that send this claim
		// as the STRING "true", and a bool field would fail the whole
		// claims decode for them — taking the email down with it, which
		// turns a refusal into a just-in-time create for an address that
		// already has an account. Any works for every shape, and the
		// assertion below accepts only a real boolean.
		EmailVerified any `json:"email_verified"`
	}
	// A token with unreadable extra claims still identifies its subject;
	// the email is a courtesy claim, not the login key.
	if err := idToken.Claims(&claims); err != nil {
		claims.Email, claims.EmailVerified = "", nil
	}
	// Absent is not true, and neither is "true": see Identity.EmailVerified.
	verified, _ := claims.EmailVerified.(bool)

	return Identity{
		Issuer:        s.cfg.Issuer,
		Subject:       idToken.Subject,
		Email:         claims.Email,
		EmailVerified: verified,
	}, nil
}

// randomToken mints 256 bits from crypto/rand, base64url-encoded: the state
// and nonce values. rand.Read cannot fail (its documented contract since Go
// 1.24); if the platform RNG somehow does, minting any token would be
// unsafe, and crashing is the only correct response.
func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("oidc: read random bytes: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
