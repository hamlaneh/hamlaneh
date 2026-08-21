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

// New returns an *http.Server bound to addr with the Hamlaneh router and
// hardened timeouts configured. ready backs the /readyz probe and may be nil
// (readyz then always reports degraded). The caller owns the server's
// lifecycle.
func New(addr string, ready ReadinessChecker) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(ready),
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
//	/api/v1/*             contract endpoints (501 stubs until Phase 1.1)
//
// Contract routes are registered by the generated api package, so the router
// can never drift from docs/api/openapi.yaml. Anything else is a 404.
func Handler(ready ReadinessChecker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.Handle("GET /static/", noDirListing(http.FileServerFS(staticFS)))
	return api.HandlerFromMux(&apiServer{ready: ready}, mux)
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
