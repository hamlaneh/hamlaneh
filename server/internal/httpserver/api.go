package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Store is everything the HTTP layer needs from persistent storage.
// *storage.Store implements it; tests substitute fakes. Phase 4's SQLite
// driver implements the same surface (CLAUDE.md: one storage interface,
// two drivers).
//
// It is one interface on purpose. Login asks TotpByUser whether an account
// has a second factor before it mints anything, and that question must not
// be answerable with "this store cannot say" at runtime: a store that cannot
// serve two-step verification fails to compile instead.
type Store interface {
	// Ready backs the /readyz probe.
	Ready(ctx context.Context) error

	UserByIdentifier(ctx context.Context, identifier string) (storage.User, error)
	CreateUser(ctx context.Context, nu storage.NewUser) (storage.User, error)
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, keepFamilyID uuid.UUID) error
	UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error
	ListUsers(ctx context.Context, params storage.ListUsersParams) ([]storage.User, error)
	// ListDirectory is the people-picker read: username order, filtered,
	// and a different query from ListUsers rather than a mode flag on it
	// (storage/directory.go says why).
	ListDirectory(ctx context.Context, params storage.ListDirectoryParams) ([]storage.User, error)
	// UserByID names the person in a membership event. The events carry a
	// UserSummary, and broadcasting one with an empty username is worse than
	// not broadcasting at all.
	UserByID(ctx context.Context, id uuid.UUID) (storage.User, error)

	CreateSession(ctx context.Context, ns storage.NewSession) (storage.Session, error)
	SessionUserByAccessHash(ctx context.Context, accessHash []byte) (storage.Session, storage.User, error)
	RotateSession(ctx context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error

	// Self-service session management: the read model behind the settings
	// Sessions list and the two revocations it offers.
	ListSessionFamilies(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error)
	RevokeUserFamily(ctx context.Context, userID, familyID uuid.UUID) error
	RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error

	// Two-step verification: the three setup steps, the two settings actions,
	// and the challenge half of a two-step sign-in.
	TotpByUser(ctx context.Context, userID uuid.UUID) (storage.Totp, error)
	RecoveryCodeCounts(ctx context.Context, userID uuid.UUID) (remaining, total int, err error)
	StartTotpSetup(ctx context.Context, userID uuid.UUID, secret []byte, ttl time.Duration) error
	VerifyTotpSetup(ctx context.Context, v storage.TotpSetupVerification) (storage.TotpVerifyOutcome, error)
	ActivateTotp(ctx context.Context, userID uuid.UUID) (time.Time, error)
	DisableTotp(ctx context.Context, userID uuid.UUID) error
	// ReplaceRecoveryCodes takes a callback, not the hashes: ten argon2id
	// hashes are nearly a second of CPU, and they must not be spent before
	// the store has confirmed the account has a second factor to reissue
	// codes for. The store calls it inside the transaction, after the check.
	ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes func() []string) error
	CreateTotpChallenge(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error
	// TotpChallengeUserByTokenHash identifies the account behind a challenge
	// token so CompleteTotpLogin can consult the per-account limiter BEFORE
	// the presented code is evaluated — a 429 that fired after the check
	// would not have stopped the guess.
	TotpChallengeUserByTokenHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error)
	CompleteTotpChallenge(ctx context.Context, att storage.TotpChallengeAttempt) (storage.User, storage.Session, storage.TotpChallengeOutcome, error)

	// Conversations. ChannelForUser is the one to reach for in a handler:
	// it fills the per-caller counts the contract makes required on every
	// Channel. ChannelByID answers without a caller and leaves those zero,
	// so it is only correct where no user is in scope.
	CreateChannel(ctx context.Context, nc storage.NewChannel) (storage.Channel, error)
	ChannelForUser(ctx context.Context, channelID, userID uuid.UUID) (storage.Channel, error)
	UpdateChannelTopic(ctx context.Context, id uuid.UUID, topic string) (storage.Channel, error)
	ListChannelsForUser(ctx context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error)
	OpenDirectMessage(ctx context.Context, callerID, peerID uuid.UUID) (storage.Channel, bool, error)
	AddChannelMember(ctx context.Context, channelID, userID, addedBy uuid.UUID) error
	RemoveChannelMember(ctx context.Context, channelID, userID uuid.UUID) error
	ListChannelMembers(ctx context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error)
	// IsChannelMember is the fact authz.Can decides channel actions on. It
	// is read per request, never cached across one: membership changes while
	// a client is connected, and a stale copy is an authorization bug.
	IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error)

	CreateMessage(ctx context.Context, nm storage.NewMessage) (storage.Message, bool, error)
	ListMessages(ctx context.Context, params storage.ListMessagesParams) (storage.MessagePage, error)
	SetReadPosition(ctx context.Context, channelID, userID, messageID uuid.UUID) error
}

// The production store satisfies the whole surface at compile time.
var _ Store = (*storage.Store)(nil)

// Login rate limit (Phase 1.1 security design): sliding window per client
// IP and per lowercased identifier, each 10 attempts per 5 minutes. Login
// checks both keys up front and records only the attempts that keep an
// attacker in the game: failed authentications, and two-step challenge
// mints — a correct password on a two-step account opens a code-guessing
// window, and unmetered windows are the brute-force this budget exists to
// stop. A login that ends in a session consumes nothing.
const (
	loginRateLimit  = 10
	loginRateWindow = 5 * time.Minute
)

// Two-step code rate limit: sliding window per client IP and per account on
// POST /api/v1/auth/login/totp. The per-challenge cap (totp.MaxChallengeAttempts)
// bounds one challenge; these bound the stream of challenges — without them
// an attacker holding the password could mint a fresh five-guess budget per
// password login and walk the whole 10^6 code space. 10 wrong codes per 5
// minutes never touches a fumbling human and caps guessing throughput at a
// rate that turns the expected ~333k-guess search into months.
//
// Deliberately a rate limit and NOT a persistent account lockout: a lockout
// would let anyone who knows a username freeze the legitimate user out of
// their own second factor at will.
const (
	totpRateLimit  = 10
	totpRateWindow = 5 * time.Minute
)

// Two-step settings rate limit: one sliding window per account across the
// four endpoints the contract gives a 429 — totp/setup, totp/verify,
// totp/disable and totp/recovery-codes.
//
// The budget is about server work, not guessing, which is why it is keyed
// on the account alone and why every call spends a unit rather than only
// the failures. Two of the four run a full argon2id password verification
// (64 MiB) before they decide anything, and recovery-codes then hashes ten
// more; a session that has been hijacked, or simply a stuck client, can
// otherwise repeat that indefinitely. There is no IP key because there is
// no anonymous reach here: all four sit behind a session, so the account IS
// the caller.
//
// One window covers all four because an attacker who has a session picks
// whichever endpoint is cheapest for them, and per-endpoint budgets would
// just multiply the total. 10 in 5 minutes is far above any real use — a
// user regenerates codes once — and far below what makes the argon2 cost
// worth paying.
const (
	totpSettingsRateLimit  = 10
	totpSettingsRateWindow = 5 * time.Minute
)

// readyzTimeout bounds the whole readiness probe: a stalled database must
// become a fast 503, not a hung request.
const readyzTimeout = 2 * time.Second

// Health probe bodies are fixed literals so those handlers have no
// marshalling error paths. Their shapes match the HealthStatus contract
// schema (pinned by tests against the generated types).
const (
	healthOKBody       = `{"status":"ok"}`
	healthDegradedBody = `{"status":"degraded"}`
)

// errNoStorage is the failure for a server wired without storage.
// Production always has a store; some unit tests construct storage-less
// handlers.
var errNoStorage = errors.New("no storage configured")

// apiServer implements the generated api.ServerInterface. Route-level
// security (CSRF, sessions, the must-change gate, admin) is enforced by
// securityMiddleware before any handler runs.
type apiServer struct {
	store Store

	// realtime delivers ws-protocol.md §4 events. Never nil — it defaults to
	// noRealtime, so a handler announces unconditionally and no event can be
	// lost to a forgotten nil check.
	realtime Realtime

	// reset is the password-reset policy. A nil service means reset is not
	// configured, which is exactly what a zero-config install is:
	// GET /api/v1/instance reports password_reset_available false, a request
	// answers the same empty 202 it always does, and any token presented to
	// reset-complete is invalid.
	reset *passwordreset.Service

	loginIPLimiter         *ratelimit.Limiter
	loginIdentifierLimiter *ratelimit.Limiter
	totpIPLimiter          *ratelimit.Limiter
	totpAccountLimiter     *ratelimit.Limiter
	totpSettingsLimiter    *ratelimit.Limiter
}

var _ api.ServerInterface = (*apiServer)(nil)

// newAPIServer wires an apiServer with fresh rate limiters, then applies
// opts. Limiters live on the apiServer — never in package state — so every
// Handler carries its own windows and tests cannot bleed budget into each
// other.
func newAPIServer(store Store, opts ...Option) *apiServer {
	s := &apiServer{
		store:                  store,
		realtime:               noRealtime{},
		loginIPLimiter:         ratelimit.New(loginRateLimit, loginRateWindow),
		loginIdentifierLimiter: ratelimit.New(loginRateLimit, loginRateWindow),
		totpIPLimiter:          ratelimit.New(totpRateLimit, totpRateWindow),
		totpAccountLimiter:     ratelimit.New(totpRateLimit, totpRateWindow),
		totpSettingsLimiter:    ratelimit.New(totpSettingsRateLimit, totpSettingsRateWindow),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// GetHealthz is the liveness probe: the process is up; no dependencies are
// checked.
func (s *apiServer) GetHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, healthOKBody)
}

// GetReadyz is the readiness probe: 200 only when the database answers a
// ping and its schema matches this binary's migrations, both within
// readyzTimeout.
func (s *apiServer) GetReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
	defer cancel()

	if err := s.checkReady(ctx); err != nil {
		slog.Warn("readiness check failed", "error", err)
		writeJSON(w, r, http.StatusServiceUnavailable, healthDegradedBody)
		return
	}
	writeJSON(w, r, http.StatusOK, healthOKBody)
}

func (s *apiServer) checkReady(ctx context.Context) error {
	if s.store == nil {
		return errNoStorage
	}
	return s.store.Ready(ctx)
}

// requireStore resolves the configured store for a handler that needs one.
// A server wired without storage is a unit-test fixture, never production;
// answering 500 rather than proceeding is what keeps the two-step check fail-
// closed, because "I cannot ask" must never read as "this account has no
// second factor".
func (s *apiServer) requireStore(w http.ResponseWriter, r *http.Request) (Store, bool) {
	if s.store == nil {
		internalError(w, r, errNoStorage)
		return nil, false
	}
	return s.store, true
}

// writeJSON sends a fixed JSON body with the given status.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeBody(w, r, []byte(body))
}
