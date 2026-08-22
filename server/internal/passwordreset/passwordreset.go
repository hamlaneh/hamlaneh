// Package passwordreset owns the emailed password-reset policy: token
// lifetime, the enumeration-safe request path, rate limiting, and the
// absolute link that reaches the mailbox.
//
// The security shape of the feature lives here rather than in the HTTP
// handlers, because every rule it enforces is about what the endpoints must
// NOT reveal:
//
//   - A request answers the same way whether or not the address belongs to
//     an account. The token is minted and hashed before the lookup, and the
//     mail is handed to an asynchronous queue, so neither crypto nor SMTP
//     latency separates the two paths.
//   - Rate limiting is keyed on the address as presented — existing or not
//     — and on the client IP. A limiter that only counted real accounts
//     would itself answer the question the endpoint refuses to answer.
//   - Unknown, expired and already-used tokens are one outcome to the
//     caller (ErrInvalidToken); only the log tells them apart.
package passwordreset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/mailer"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TokenTTL is how long an emailed reset link stays usable. It is the single
// definition: the storage layer applies it against the database clock and
// the mail template quotes it.
const TokenTTL = 30 * time.Minute

// EnvPublicURL names the absolute, public base URL of this instance, for
// example https://chat.example.com. The server cannot infer it — Caddy owns
// the origin and terminates TLS — and without it there is no link to email.
const EnvPublicURL = "HAMLANEH_PUBLIC_URL"

// Rate-limit budgets. Requests are counted whether or not they matched an
// account, so the budgets bound mail volume as well as guessing.
const (
	// addressRateLimit bounds requests naming one address, so nobody can
	// have somebody else's inbox flooded.
	addressRateLimit  = 5
	addressRateWindow = 15 * time.Minute
	// ipRateLimit is looser: an office or a household shares one address.
	ipRateLimit  = 20
	ipRateWindow = 15 * time.Minute
	// completeRateLimit bounds token submissions per client. Guessing a
	// 256-bit token is hopeless; the budget exists to bound the argon2 work
	// one caller can make the server do.
	completeRateLimit  = 10
	completeRateWindow = 15 * time.Minute
)

// The emailed link: {base}/reset#token={raw}.
//
// The token rides in the FRAGMENT, never in a query string, and that is a
// security property, not a style choice: fragments are not sent to any
// server, so the live token cannot land in this instance's (or any
// intermediary proxy's) access logs, and it is never leaked through a
// Referer header if the reset page loads other resources. A query-string
// token would sit, valid for 30 minutes, in the logs of the very server it
// unlocks. The API is untouched by this — the client submits the token in
// the reset-complete POST body — so only client-side script ever reads it.
// Do not "fix" this back to ?token=.
const (
	resetPath          = "/reset"
	tokenFragmentParam = "token" // #nosec G101 -- a URL parameter name, not a credential
)

// Errors callers branch on with errors.Is.
var (
	// ErrRateLimited reports that the caller exhausted a budget; the HTTP
	// layer answers 429. Refusals carry it through RateLimitedError, so
	// errors.Is still matches while the duration stays reachable.
	ErrRateLimited = errors.New("passwordreset: rate limited")
	// ErrInvalidToken is the single answer for a token that is unknown,
	// expired, or already used.
	ErrInvalidToken = errors.New("passwordreset: invalid reset token")
)

// RateLimitedError is a refusal that knows how long it lasts, so the HTTP
// layer can answer with Retry-After instead of leaving the client to guess.
// A guessing client either retries too early — spending an attempt on a
// refusal it could have predicted — or gives up on a wait that has already
// passed.
//
// It unwraps to ErrRateLimited so errors.Is keeps working unchanged.
//
// The duration is not an enumeration oracle even though one of the budgets
// is keyed on the address: every request is counted whether or not it
// matched an account, so the wait describes the caller's own request
// history and never whether the address exists.
type RateLimitedError struct {
	// RetryAfter is how long the exhausted budget stays exhausted.
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("passwordreset: rate limited for %s", e.RetryAfter)
}

// Unwrap reports the sentinel so errors.Is(err, ErrRateLimited) holds.
func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// Store is what the service needs from persistent storage. *storage.Store
// implements it.
type Store interface {
	UserByEmail(ctx context.Context, email string) (storage.User, error)
	CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error
	ConsumePasswordReset(ctx context.Context, tokenHash []byte, passwordHash string) (uuid.UUID, storage.ResetOutcome, error)
}

// Config is the non-dependency half of a Service.
type Config struct {
	// BaseURL is the absolute public origin of this instance, optionally
	// with a path prefix. Required whenever Deliverable is true.
	BaseURL string
	// Deliverable reports whether mail actually leaves the instance — false
	// for the null mailer of a zero-config install. It is what
	// GET /api/v1/instance publishes as password_reset_available.
	Deliverable bool
}

// Service implements the password-reset policy.
type Service struct {
	store Store
	// queue is the dispatch path AND the thing Close drains; it satisfies
	// mailer.Mailer itself, so there is no separate transport field.
	queue   *mailer.Queue
	baseURL *url.URL

	deliverable bool

	addressLimiter  *ratelimit.Limiter
	ipLimiter       *ratelimit.Limiter
	completeLimiter *ratelimit.Limiter
}

// New returns a Service dispatching through mail. The mailer is always
// wrapped in an asynchronous queue, so no configuration can put an SMTP
// conversation on the response path.
//
// It fails when mail is deliverable but the public base URL is missing or
// unusable: a reset that mints tokens nobody can open is worse than one
// that is honestly switched off, and startup is where that has to be said.
func New(store Store, mail mailer.Mailer, cfg Config) (*Service, error) {
	if store == nil || mail == nil {
		return nil, errors.New("passwordreset: store and mailer are required")
	}

	base, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		if cfg.Deliverable {
			return nil, err
		}
		// No transport, so no link is ever built; the endpoints stay
		// reachable and answer exactly as they do for an unknown address.
		slog.Info("password reset is disabled", "reason", err)
		base = nil
	}

	return &Service{
		store:           store,
		queue:           mailer.NewQueue(mail, mailer.QueueSize, mailer.QueueWorkers),
		baseURL:         base,
		deliverable:     cfg.Deliverable && base != nil,
		addressLimiter:  ratelimit.New(addressRateLimit, addressRateWindow),
		ipLimiter:       ratelimit.New(ipRateLimit, ipRateWindow),
		completeLimiter: ratelimit.New(completeRateLimit, completeRateWindow),
	}, nil
}

// FromEnv builds the service from the process environment: the SMTP
// settings internal/mailer reads, plus EnvPublicURL. Wiring calls it at
// startup so a misconfiguration stops the process instead of surfacing at
// somebody's first forgotten password.
func FromEnv(store Store) (*Service, error) {
	mailCfg, err := mailer.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	transport, err := mailer.New(mailCfg, TokenTTL)
	if err != nil {
		return nil, err
	}
	return New(store, transport, Config{
		BaseURL:     os.Getenv(EnvPublicURL),
		Deliverable: mailCfg.Configured(),
	})
}

// Close drains the dispatch queue. Callers should run it after the HTTP
// server stopped accepting requests.
func (s *Service) Close() { s.queue.Close() }

// Available reports whether a reset can actually reach a mailbox: a
// transport is configured and a public base URL exists to build the link
// from. It is the value the sign-in screen uses to decide whether to show
// a "Forgot password?" link at all.
func (s *Service) Available() bool { return s.deliverable }

// Request handles a reset request for the presented address. It returns nil
// whether or not the address belongs to an account — the caller must not be
// able to tell, and neither must the response it writes.
//
// A non-nil error other than ErrRateLimited is an internal failure. The
// HTTP layer logs it and still answers 202: a 500 that only ever happened
// for addresses that exist would be the enumeration oracle this whole path
// is built to avoid.
func (s *Service) Request(ctx context.Context, ipKey, email string) error {
	addressKey := strings.ToLower(strings.TrimSpace(email))
	// Both budgets guard this path, so the wait is the longer of the two
	// that are actually exhausted: clearing only the shorter one would send
	// the caller back into the other's refusal.
	var wait time.Duration
	if s.ipLimiter.Limited(ipKey) {
		wait = s.ipLimiter.RetryAfter(ipKey)
	}
	if s.addressLimiter.Limited(addressKey) {
		wait = max(wait, s.addressLimiter.RetryAfter(addressKey))
	}
	if wait > 0 {
		return &RateLimitedError{RetryAfter: wait}
	}
	// Every request counts, matched or not: counting only real accounts
	// would make the 429 itself an existence check.
	s.ipLimiter.Record(ipKey)
	s.addressLimiter.Record(addressKey)

	if !s.Available() {
		return nil
	}

	// Minted before the lookup so the crypto/rand read and the SHA-256 land
	// on both paths. The database work still differs by one insert; the
	// large, variable cost — the SMTP conversation — is off the response
	// path entirely, which is what the enumeration defense rests on.
	raw, hash := session.NewToken()

	user, err := s.store.UserByEmail(ctx, email)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("passwordreset: look up address: %w", err)
	}
	if user.Email == nil {
		// Unreachable: the row matched on its email column.
		return nil
	}

	if err := s.store.CreatePasswordResetToken(ctx, user.ID, hash, TokenTTL); err != nil {
		return fmt.Errorf("passwordreset: store token: %w", err)
	}
	if err := s.queue.SendPasswordReset(ctx, *user.Email, user.Locale, s.resetURL(raw)); err != nil {
		return fmt.Errorf("passwordreset: dispatch mail: %w", err)
	}
	return nil
}

// Complete consumes a reset token and installs newPassword, returning
// ErrInvalidToken for a token that is unknown, expired, or already used.
//
// newPassword must already satisfy the account password policy: the caller
// checks it, because rejecting a weak password must not burn the token, and
// because only the caller can phrase the failure for its own contract.
func (s *Service) Complete(ctx context.Context, ipKey, token, newPassword string) error {
	if s.completeLimiter.Limited(ipKey) {
		return &RateLimitedError{RetryAfter: s.completeLimiter.RetryAfter(ipKey)}
	}
	s.completeLimiter.Record(ipKey)

	// Hashing before the token is known to be good costs one argon2 run per
	// attempt — the same trade login already makes with CompareDummy, and
	// what keeps the whole reset atomic in a single transaction.
	userID, outcome, err := s.store.ConsumePasswordReset(
		ctx, session.HashToken(token), password.Hash(newPassword))
	if err != nil {
		return fmt.Errorf("passwordreset: consume token: %w", err)
	}
	if outcome != storage.ResetOutcomeApplied {
		slog.Info("password reset token rejected", "outcome", outcome.String())
		return ErrInvalidToken
	}

	slog.Info("password reset completed; every session family revoked", "user_id", userID)
	return nil
}

// resetURL builds the absolute link that reaches the mailbox. The token
// goes in the fragment — see the resetPath constant for why that is
// load-bearing. The token is base64url (session.NewToken), every character
// of which is valid in a fragment, so the link serializes byte-for-byte.
func (s *Service) resetURL(token string) string {
	link := *s.baseURL
	link.Path = path.Join(link.Path, resetPath)
	link.Fragment = tokenFragmentParam + "=" + token
	return link.String()
}

// parseBaseURL validates the public origin the emailed link is built from.
func parseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf(
			"%s is not set, so no absolute reset link can be built "+
				"(the reverse proxy owns the public origin, not the server)",
			EnvPublicURL)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid URL: %w", EnvPublicURL, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("%s must be an http:// or https:// URL, got %q", EnvPublicURL, raw)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%s has no host, got %q", EnvPublicURL, raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must carry no query or fragment, got %q", EnvPublicURL, raw)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.User = nil
	return parsed, nil
}
