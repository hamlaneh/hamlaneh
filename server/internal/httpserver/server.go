// Package httpserver provides the Hamlaneh HTTP server: routing, handlers,
// and embedded static assets for the Phase 0 walking skeleton.
//
// TLS termination and security headers (including the strict CSP) are the
// reverse proxy's job (Caddy); this server speaks plain HTTP on its address.
package httpserver

import (
	"embed"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
)

//go:embed index.html
var indexPage []byte

//go:embed static
var staticFS embed.FS

// Server timeouts guard against slow-client (slowloris-style) resource
// exhaustion. Values are generous for Phase 0 and can tighten later.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// Option configures a server beyond its storage. Every option is optional by
// construction: the zero-config install passes none.
type Option func(*apiServer)

// WithPasswordReset installs the password-reset policy. Omitting it leaves
// reset unconfigured — the honest state of an install with no mail transport
// — and the reset endpoints answer accordingly (see apiServer.reset).
func WithPasswordReset(svc *passwordreset.Service) Option {
	return func(s *apiServer) { s.reset = svc }
}

// New returns an *http.Server bound to addr with the Hamlaneh router and
// hardened timeouts configured. store backs everything stateful and may be
// nil in unit tests (readyz then reports degraded and authenticated routes
// answer 500). The caller owns the server's lifecycle.
func New(addr string, store Store, opts ...Option) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(store, opts...),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// Handler returns the root HTTP handler with every route registered:
//
//	GET /                 embedded static login page
//	GET /static/*         embedded static assets (stylesheets)
//	GET /healthz          liveness probe, 200 {"status":"ok"}
//	GET /readyz           readiness probe (database ping + schema version)
//	/api/v1/*             contract endpoints (Phase 1.1 identity core; the
//	                      Phase 1.2 messaging surface is routed and gated
//	                      but still answers 501 — see messaging_stubs.go)
//
// Contract routes are registered by the generated api package, so the router
// can never drift from docs/api/openapi.yaml. Every contract route runs
// behind securityMiddleware (CSRF, sessions, must-change gate, admin), and
// malformed request parameters answer with the contract's JSON Error shape
// instead of oapi-codegen's plain-text default. Anything else is a 404.
func Handler(store Store, opts ...Option) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.Handle("GET /static/", noDirListing(http.FileServerFS(staticFS)))

	s := newAPIServer(store, opts...)
	return api.HandlerWithOptions(s, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		Middlewares:      []api.MiddlewareFunc{s.securityMiddleware},
		ErrorHandlerFunc: requestErrorHandler,
	})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeBody(w, r, indexPage)
}

// noDirListing returns 404 for directory paths so the embedded static file
// server never renders auto-generated directory indexes.
func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeBody writes body and logs a failed write; by the time a response
// write fails the status line is already sent, so logging is all that is
// left to do.
func writeBody(w http.ResponseWriter, r *http.Request, body []byte) {
	if _, err := w.Write(body); err != nil {
		slog.Error("write response body", "path", r.URL.Path, "error", err)
	}
}
