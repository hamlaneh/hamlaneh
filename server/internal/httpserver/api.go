package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Store is everything the HTTP layer needs from persistent storage.
// *storage.Store implements it; tests substitute fakes. Phase 4's SQLite
// driver implements the same surface (CLAUDE.md: one storage interface,
// two drivers).
type Store interface {
	// Ready backs the /readyz probe.
	Ready(ctx context.Context) error

	UserByIdentifier(ctx context.Context, identifier string) (storage.User, error)
	CreateUser(ctx context.Context, nu storage.NewUser) (storage.User, error)
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string, keepFamilyID uuid.UUID) error
	UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error
	ListUsers(ctx context.Context, params storage.ListUsersParams) ([]storage.User, error)

	CreateSession(ctx context.Context, ns storage.NewSession) (storage.Session, error)
	SessionUserByAccessHash(ctx context.Context, accessHash []byte) (storage.Session, storage.User, error)
	RotateSession(ctx context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
}

// Login rate limit (Phase 1.1 security design): sliding window per client
// IP and per lowercased identifier, each 10 failed attempts per 5 minutes.
// Successful logins consume no budget — Login checks both keys up front and
// records only failed authentications.
const (
	loginRateLimit  = 10
	loginRateWindow = 5 * time.Minute
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

	loginIPLimiter         *ratelimit.Limiter
	loginIdentifierLimiter *ratelimit.Limiter
}

var _ api.ServerInterface = (*apiServer)(nil)

// newAPIServer wires an apiServer with fresh rate limiters.
func newAPIServer(store Store) *apiServer {
	return &apiServer{
		store:                  store,
		loginIPLimiter:         ratelimit.New(loginRateLimit, loginRateWindow),
		loginIdentifierLimiter: ratelimit.New(loginRateLimit, loginRateWindow),
	}
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

// writeJSON sends a fixed JSON body with the given status.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeBody(w, r, []byte(body))
}
