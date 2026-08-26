package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/oidc"
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
	// UserByEmail backs one thing here: phrasing the SSO refusal for an
	// identity whose email collides with a local account. It never decides
	// who signs in — (issuer, subject) is the whole login key (ADR 004).
	UserByEmail(ctx context.Context, email string) (storage.User, error)
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
	// UpdateUserProfile is the settings panel's own edit of the caller's
	// account — the locale among them, so a language choice follows the
	// person rather than the browser it was made in.
	UpdateUserProfile(ctx context.Context, userID uuid.UUID, upd storage.UserProfileUpdate) (storage.User, error)

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
	// Single sign-on identities (ADR 004 slice 2). The lookup also records
	// the use (last_login_at); the two conflicts of a link map to
	// storage.ErrOidcAccountLinked / ErrOidcIdentityTaken.
	UserByOidcIdentity(ctx context.Context, issuer, subject string) (storage.User, error)
	LinkOidcIdentity(ctx context.Context, userID uuid.UUID, issuer, subject string, emailAtLink *string) error
	UnlinkOidcIdentity(ctx context.Context, userID uuid.UUID) error
	// Pending links: the server-side state that decides link vs sign-in at
	// the callback. Create is called only by the session-gated link
	// endpoint; Consume atomically deletes and returns the target account
	// (ErrNotFound when there is no live pending link), so the callback can
	// never be steered by a forged cookie.
	CreateOidcLinkRequest(ctx context.Context, stateHash, secretHash []byte, userID uuid.UUID, ttl time.Duration) error
	ConsumeOidcLinkRequest(ctx context.Context, stateHash, secretHash []byte) (uuid.UUID, error)

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

	// The tamper-evident log (internal/audit). The append takes a seal
	// callback rather than a finished hash because the entry cannot be
	// sealed until its place in the chain is fixed, and that happens inside
	// the transaction — storage decides the position, audit holds the key.
	AppendAuditEntry(ctx context.Context, e storage.AuditEntry, seal func(storage.AuditEntry) []byte) (storage.AuditEntry, error)
	ListAuditEntries(ctx context.Context, params storage.ListAuditParams) ([]storage.AuditEntry, error)

	// CreateAttachment records an upload whose bytes are already on disk.
	// Reading attachments back is not a call of its own: they arrive with
	// the messages that carry them, in one query per page rather than one
	// per message (storage.Message.Attachments).
	CreateAttachment(ctx context.Context, na storage.NewAttachment) (storage.Attachment, error)

	// Admin user lifecycle. UpdateUserAdmin owns the last-admin rule — the
	// refusal is a fact about a set, so it cannot be decided by a handler
	// reading rows one at a time.
	UpdateUserAdmin(ctx context.Context, userID uuid.UUID, upd storage.AdminUserUpdate) (storage.User, error)
	SetTemporaryPassword(ctx context.Context, userID uuid.UUID, passwordHash string) (storage.User, error)

	// Invitations. Only the hash is ever stored, so nothing here takes or
	// returns a raw token except by its digest.
	CreateInvite(ctx context.Context, createdBy uuid.UUID, tokenHash []byte, note string, ttl time.Duration) (storage.Invite, error)
	ListOpenInvites(ctx context.Context, params storage.ListInvitesParams) ([]storage.Invite, error)
	RevokeInvite(ctx context.Context, id uuid.UUID) error
	OpenInviteByTokenHash(ctx context.Context, tokenHash []byte) (storage.Invite, error)
	RedeemInvite(ctx context.Context, tokenHash []byte, nu storage.NewUser) (storage.User, error)

	// Instance settings, including the derived accounts-without-two-step
	// count the enforcement switch is read beside.
	OrgSettings(ctx context.Context) (storage.OrgSettings, error)
	UpdateOrgSettings(ctx context.Context, patch storage.OrgSettingsPatch) (storage.OrgSettings, error)
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

// Every other budget this server enforces — two-step settings, search, the
// user directory, and the conversation and message writes — is per endpoint
// and lives in the table in ratelimits.go, spent by middleware rather than by
// hand inside a handler.

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

	// sso is the OIDC relying party. Nil means no provider is configured —
	// the zero-config install: instance info reports sso disabled, the
	// start/callback/link endpoints answer 503 sso_unavailable, and
	// unlinking (a plain database delete) still works.
	sso *oidc.Service

	// The budgets that cannot be decided from a route, spent by the handlers
	// that own them (ratelimits.go explains why each one stayed).
	loginIPLimiter         *ratelimit.Limiter
	loginIdentifierLimiter *ratelimit.Limiter
	totpIPLimiter          *ratelimit.Limiter
	totpAccountLimiter     *ratelimit.Limiter

	// budgets holds one limiter per named per-endpoint budget, spent by
	// rateLimitMiddleware against the route the request matched.
	budgets map[budgetName]*ratelimit.Limiter

	// previews is the asynchronous link-preview pipeline
	// (preview_enricher.go). Nil means enrichment is not wired, and messages
	// carry no preview cards.
	previews PreviewEnricher

	// files wires the cookie-less files origin (files_origin.go). Its zero
	// value is an install with no upload pipeline configured, and the two
	// file routes then answer 404 — which is what that install is.
	files Files

	// blobs holds uploaded file bytes. A nil one is a server wired without
	// the upload pipeline — a unit-test fixture, never production — and
	// UploadFile answers 500 rather than pretending to have stored anything.
	blobs *blobstore.Store

	// fileSigner mints the URLs every serialized Attachment carries. Never
	// nil: it defaults to the unsigned placeholder, so serialization needs no
	// nil check and no attachment can be written without the url the
	// contract makes required.
	fileSigner fileURLSigner

	// audit records administrative actions. Never nil — it defaults to
	// noAudit, so a handler records unconditionally and no action can be
	// lost to a forgotten nil check (the same reason realtime works that
	// way).
	audit AuditRecorder

	// auditChain verifies the pages the log reads back. Nil is a server
	// wired without the audit key — a unit-test fixture, never production,
	// because main refuses to start without one — and the audit endpoint
	// then answers 500 rather than claiming a page verified when nothing
	// checked it (audit_handlers.go).
	auditChain *audit.Chain

	// publicURL is the instance's absolute public origin, used to build the
	// invitation link the dashboard shows once. Empty is an install that was
	// never told its own origin; the link is then site-relative, which still
	// resolves for the admin looking at it (admin_handlers.go).
	publicURL string
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
		budgets:                newBudgetLimiters(),
		fileSigner:             unsignedFileURLs{},
		audit:                  noAudit{},
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
