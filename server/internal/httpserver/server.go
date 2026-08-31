// Package httpserver provides the Hamlaneh HTTP server: routing, handlers,
// the built React application embedded in the binary, and the response
// headers that constrain it.
//
// TLS termination is the reverse proxy's job (Caddy); this server speaks
// plain HTTP on its address. The content-security headers, including the
// CSP, belong to the application and are set here — see securityheaders.go
// for why they are not in the proxy.
package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/webassets"
)

// Server timeouts guard against slow-client (slowloris-style) resource
// exhaustion. Values are generous for Phase 0 and can tighten later.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 120 * time.Second
)

// scimPrefix is where the provisioning surface is mounted. It is spelled
// here rather than imported from internal/scim so this package's routing
// table reads on its own; the two are pinned together by
// TestSCIMPrefixMatchesPackage.
const scimPrefix = "/scim/v2/"

// Option configures a server beyond its storage. Every option is optional by
// construction: the zero-config install passes none.
type Option func(*apiServer)

// WithPasswordReset installs the password-reset policy. Omitting it leaves
// reset unconfigured — the honest state of an install with no mail transport
// — and the reset endpoints answer accordingly (see apiServer.reset).
func WithPasswordReset(svc *passwordreset.Service) Option {
	return func(s *apiServer) { s.reset = svc }
}

// WithFiles wires the files origin: the URL signer that is the credential
// on it, plus the metadata and blob reads that serve one. Omitting it —
// or omitting any member — leaves GET /files/{id} answering 404 with the
// same headers it would carry on a hit (files_origin.go says why).
func WithFiles(f Files) Option {
	return func(s *apiServer) { s.files = f }
}

// WithSCIM mounts the SCIM 2.0 provisioning surface at /scim/v2. Omitting it
// leaves those paths answering the application's plain 404, which is the
// honest state of a server built without one.
//
// It takes a finished handler rather than a store because that is the whole
// relationship: internal/scim owns its own wire format, its own
// authentication and its own budget, and this package's only job is to serve
// it from the same listener on a prefix that is not /api. Nothing of the
// contract router — no cookie, no CSRF check, no session — reaches it, and
// nothing of it reaches the contract router.
func WithSCIM(h http.Handler) Option {
	return func(s *apiServer) { s.scim = h }
}

// WithCompression gzips the embedded web build on the way out. It takes a
// bool rather than being presence-means-on because the install decides it
// from one environment variable (EnvCompressResponses), and a conditional
// append at the only call site would read worse than this.
//
// It is off by default, and belongs to home mode: the compose stack fronts
// this server with Caddy, which already runs `encode zstd gzip`. compress.go
// carries the reasoning, including why no API response is compressed
// whatever this is set to.
func WithCompression(enabled bool) Option {
	return func(s *apiServer) { s.compressAssets = enabled }
}

// WithTrustedProxy declares that a reverse proxy terminates connections in
// front of this listener and may name the real client with X-Forwarded-For.
//
// It is off by default and must stay off by default: the header is writable
// by anything that can open a socket to this process, so honouring it on a
// deployment with nothing in front hands every caller the choice of its own
// rate-limit key and its own address in the audit log. The compose stack sets
// it because Caddy is there by construction; the single binary does not
// (cmd/hamlaneh-server/home.go). middleware.go clientIP is the whole model.
func WithTrustedProxy(trusted bool) Option {
	return func(s *apiServer) { s.trustProxy = trusted }
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

// Handler returns the root HTTP handler. What it serves, by owning file —
// each of those files registers its own routes, so this is a map and not the
// list (a list here would go stale the first time one of them added a path):
//
//	webapp.go       the built React application on every client-routed path,
//	                its content-hashed bundle under /assets/, its unhashed
//	                public files under /brand/, and the contract's JSON 404
//	                for anything else under /api/
//	files_origin.go the cookie-less files origin: signed, expiring URLs for
//	                uploaded bytes, outside the contract on purpose
//	server.go       /scim/v2/*, the SCIM 2.0 provisioning surface,
//	                authenticated by bearer token alone and likewise outside
//	                the contract (internal/scim, docs/api/scim.md)
//	call_handlers.go the media server's webhook receiver, authenticated by
//	                verifying its signature over the request body and outside
//	                the contract for the same reason
//	api (generated) /healthz, /readyz and every /api/v1 contract endpoint
//
// Contract routes are registered by the generated api package, so the router
// can never drift from docs/api/openapi.yaml. Every contract route runs
// behind securityMiddleware (CSRF, sessions, must-change gate, admin), and
// malformed request parameters answer with the contract's JSON Error shape
// instead of oapi-codegen's plain-text default. Anything else is a 404.
//
// The whole result is wrapped in securityHeaders, so the HTML document is
// covered by the same policy as the API.
func Handler(store Store, opts ...Option) http.Handler {
	return handler(store, webassets.FS(), opts...)
}

// handler is Handler with the web build injected. Tests use it to serve a
// fixture bundle: the Go CI job never runs `npm run build`, so the embedded
// build in a plain checkout is only the placeholder, and asset behaviour has
// to be exercised against something that looks like a real one.
func handler(store Store, web fs.FS, opts ...Option) http.Handler {
	mux := http.NewServeMux()

	// Built before the web routes because one option — compression — decides
	// how the build itself is served (compress.go).
	s := newAPIServer(store, opts...)
	routeWebapp(mux, web, s.compressAssets)

	// Registered on the base mux, deliberately outside the contract router
	// and its session middleware: the files origin carries no cookie to
	// check (files_origin.go).
	routeFilesOrigin(mux, s)
	// Likewise outside it, for the opposite reason: the provisioning surface
	// has a credential, and it is a bearer token rather than a session. A
	// cookie must buy nothing there and a token must buy nothing under /api
	// (docs/api/scim.md §3), which is a property of it being routed here
	// instead of through securityMiddleware.
	routeSCIM(mux, s)
	// And the third surface outside the contract router, for a third reason:
	// the media server delivers webhooks with no cookie and no CSRF token,
	// signing the body instead. The signature IS the credential
	// (call_handlers.go).
	routeCallWebhook(mux, s)

	routed := api.HandlerWithOptions(s, api.StdHTTPServerOptions{
		BaseRouter: mux,
		// READ THE ORDER BACKWARDS. The generated wrapper folds this slice
		// with handler = middleware(handler), so the LAST element ends up
		// outermost and runs FIRST: securityMiddleware, then
		// rateLimitMiddleware, then the handler.
		//
		// That order is required, not incidental. Every budget in
		// ratelimits.go is keyed on the authenticated account, which only
		// exists once securityMiddleware has put the principal in the
		// request context — and the budget must still be spent before the
		// handler does the work it exists to refuse.
		// TestAuthenticationAnswersBeforeTheBudget pins it from outside.
		Middlewares:      []api.MiddlewareFunc{s.rateLimitMiddleware, s.securityMiddleware},
		ErrorHandlerFunc: requestErrorHandler,
	})
	return securityHeaders(routed)
}

// routeSCIM mounts the provisioning surface, when one is configured, on
// everything under /scim/v2. A server built without it leaves those paths to
// the application's own 404.
//
// The prefix is registered whole rather than route by route: the SCIM mux
// answers its own 404 for a path it does not serve, and that answer must be
// a SCIM error envelope — a sync engine parses that one and not this
// server's. /scim/v2/Groups is the case that matters, and it is how Groups
// are refused (scim.md §2).
func routeSCIM(mux *http.ServeMux, s *apiServer) {
	mux.HandleFunc(scimPrefix, func(w http.ResponseWriter, r *http.Request) {
		if s.scim == nil {
			http.NotFound(w, r)
			return
		}
		s.scim.ServeHTTP(w, r)
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
