package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// ReadinessChecker reports whether the server's dependencies (database
// connectivity, applied schema migrations) are ready for traffic.
// *storage.Store implements it.
type ReadinessChecker interface {
	Ready(ctx context.Context) error
}

// readyzTimeout bounds the whole readiness probe: a stalled database must
// become a fast 503, not a hung request.
const readyzTimeout = 2 * time.Second

// Response bodies are fixed literals so the handlers have no marshalling
// error paths. Their shapes match the HealthStatus and Error contract
// schemas (pinned by tests against the generated types).
const (
	healthOKBody       = `{"status":"ok"}`
	healthDegradedBody = `{"status":"degraded"}`
	notImplementedBody = `{"error":{"code":"not_implemented","message":"endpoint not implemented yet"}}`
)

// errNoStorage is the readiness failure for a server wired without storage.
// Production always has a store; unit tests construct storage-less handlers.
var errNoStorage = errors.New("no storage configured")

// apiServer implements the generated api.ServerInterface. Everything beyond
// the health probes is a stub answering 501 until Phase 1.1 lands the real
// handlers.
type apiServer struct {
	ready ReadinessChecker
}

var _ api.ServerInterface = (*apiServer)(nil)

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
	if s.ready == nil {
		return errNoStorage
	}
	return s.ready.Ready(ctx)
}

// Login is a Phase 1.1 stub.
func (s *apiServer) Login(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// Logout is a Phase 1.1 stub.
func (s *apiServer) Logout(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// RefreshSession is a Phase 1.1 stub.
func (s *apiServer) RefreshSession(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GetCurrentUser is a Phase 1.1 stub.
func (s *apiServer) GetCurrentUser(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// AdminListUsers is a Phase 1.1 stub.
func (s *apiServer) AdminListUsers(w http.ResponseWriter, r *http.Request, _ api.AdminListUsersParams) {
	notImplemented(w, r)
}

// AdminCreateUser is a Phase 1.1 stub.
func (s *apiServer) AdminCreateUser(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// notImplemented answers a contract endpoint that has no implementation yet
// with the contract's Error shape.
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusNotImplemented, notImplementedBody)
}

// writeJSON sends a fixed JSON body with the given status.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeBody(w, r, []byte(body))
}
