package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// routeClass is the route-level security classification of a contract
// endpoint. The zero value is deliberately unclassified: an endpoint
// missing from routePolicies fails closed.
type routeClass int

const (
	classUnclassified routeClass = iota
	// classPublic needs no authentication (health probes, login).
	classPublic
	// classRefreshCookie is authenticated by the refresh cookie inside the
	// handler itself, not by a session (POST /api/v1/auth/refresh). It is
	// deliberately usable while must_change_password is set — the gate
	// binds to session-authenticated routes, and logging the user out
	// mid-password-change would help nobody.
	classRefreshCookie
	// classChallengeCookie is authenticated by the two-step challenge cookie
	// inside the handler, not by a session — the sibling of classRefreshCookie.
	// A password-verified login on a two-step account holds nothing else, and
	// this class grants nothing beyond completing that one sign-in.
	classChallengeCookie
	// classSessionMustChangeAllowed requires a valid session and stays
	// reachable while must_change_password is set (the allowed trio:
	// change-password, logout, users/me).
	classSessionMustChangeAllowed
	// classSession requires a valid session and is blocked while
	// must_change_password is set: every session route outside the trio.
	// It carries no resource-level meaning — which channel a member may
	// read or write is an explicit authz.Can call inside the handler, not
	// a route class.
	classSession
	// classAdmin requires a valid session plus an authz admin check, and
	// is blocked while must_change_password is set.
	classAdmin
)

// routePolicy classifies one contract endpoint. action names the authz
// action for classAdmin routes.
type routePolicy struct {
	class  routeClass
	action authz.Action
}

// routePolicies is the route-level security table for every contract
// endpoint, keyed on the ServeMux pattern that matched the request
// (http.Request.Pattern) — "METHOD /path" with the contract's {template}
// segments left in place, exactly as the generated router registers them.
//
// Keying on the pattern rather than the concrete URL path is what lets
// path-parameterised routes be classified at all: one entry covers every
// channel id. It cannot be spoofed either, because the pattern is chosen by
// the router after matching, not taken from the request.
//
// TestEveryContractRouteIsClassified walks the routes the generated api
// package registers and fails when one has no entry here, so the table
// cannot silently miss an endpoint the contract added.
var routePolicies = map[string]routePolicy{
	"GET /healthz":                      {class: classPublic},
	"GET /readyz":                       {class: classPublic},
	"POST /api/v1/auth/login":           {class: classPublic},
	"POST /api/v1/auth/refresh":         {class: classRefreshCookie},
	"POST /api/v1/auth/logout":          {class: classSessionMustChangeAllowed},
	"POST /api/v1/auth/change-password": {class: classSessionMustChangeAllowed},
	"GET /api/v1/users/me":              {class: classSessionMustChangeAllowed},
	"GET /api/v1/instance":              {class: classPublic},
	"POST /api/v1/auth/reset-request":   {class: classPublic},
	"POST /api/v1/auth/reset-complete":  {class: classPublic},
	"POST /api/v1/auth/login/totp":      {class: classChallengeCookie},
	"GET /api/v1/admin/users":           {class: classAdmin, action: authz.AdminUsersList},
	"POST /api/v1/admin/users":          {class: classAdmin, action: authz.AdminUsersCreate},

	// Phase 1.2 messaging surface. Every one of these carries sessionCookie
	// in the contract, none is admin-only, and none joins the must-change
	// trio — a user who still owes a password change gets 403 here.
	"GET /api/v1/users":                                        {class: classSession},
	"GET /api/v1/channels":                                     {class: classSession},
	"POST /api/v1/channels":                                    {class: classSession},
	"GET /api/v1/channels/{channelId}":                         {class: classSession},
	"PATCH /api/v1/channels/{channelId}":                       {class: classSession},
	"GET /api/v1/channels/{channelId}/members":                 {class: classSession},
	"POST /api/v1/channels/{channelId}/members":                {class: classSession},
	"DELETE /api/v1/channels/{channelId}/members/{userId}":     {class: classSession},
	"GET /api/v1/channels/{channelId}/messages":                {class: classSession},
	"POST /api/v1/channels/{channelId}/messages":               {class: classSession},
	"PATCH /api/v1/channels/{channelId}/messages/{messageId}":  {class: classSession},
	"DELETE /api/v1/channels/{channelId}/messages/{messageId}": {class: classSession},
	"PUT /api/v1/channels/{channelId}/read":                    {class: classSession},
	"POST /api/v1/dms":                                         {class: classSession},
	"GET /api/v1/search":                                       {class: classSession},

	// Phase 1.1b self-service security. All session-gated; none joins the
	// must-change trio, because a user who owes a password change fixes that
	// before touching their second factor or their device list.
	"GET /api/v1/users/me/totp":                    {class: classSession},
	"POST /api/v1/users/me/totp/setup":             {class: classSession},
	"POST /api/v1/users/me/totp/verify":            {class: classSession},
	"POST /api/v1/users/me/totp/activate":          {class: classSession},
	"POST /api/v1/users/me/totp/disable":           {class: classSession},
	"POST /api/v1/users/me/totp/recovery-codes":    {class: classSession},
	"GET /api/v1/users/me/sessions":                {class: classSession},
	"DELETE /api/v1/users/me/sessions/{familyId}":  {class: classSession},
	"POST /api/v1/users/me/sessions/revoke-others": {class: classSession},
	"GET /api/v1/ws":                               {class: classSession},
}

// principal is the authenticated caller: the user plus the session that
// authenticated this request.
type principal struct {
	user    storage.User
	session storage.Session
}

// principalKey is the unexported context key type for the request principal.
type principalKey struct{}

func contextWithPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the request principal placed by the security
// middleware. ok is false on public routes and would indicate a programming
// error on protected ones.
func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey{}).(principal)
	return p, ok
}

// securityMiddleware enforces the route-level security policy on every
// contract endpoint, in order: CSRF double-submit, session authentication,
// the must-change-password gate, and the admin authz gate. Handlers behind
// it receive the principal via the request context and perform only their
// own resource-level authz.Can checks.
//
// The policy is looked up by the matched route pattern. A request that
// reached a handler without one — an empty Pattern, or a pattern nobody
// classified — is refused with 500 rather than served: unclassified means
// unauthorized. net/http routes HEAD requests to GET patterns, so a HEAD
// request is classified and gated exactly like the GET it matched.
func (s *apiServer) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pol, ok := routePolicies[r.Pattern]
		if !ok || pol.class == classUnclassified {
			// A registered contract route without a security classification
			// is a programming error; fail closed rather than open.
			slog.Error("route has no security classification",
				"method", r.Method, "path", r.URL.Path, "pattern", r.Pattern)
			writeError(w, r, http.StatusInternalServerError, codeInternalError, msgInternalError)
			return
		}

		if !s.checkCSRF(w, r) {
			return
		}

		if pol.class == classPublic || pol.class == classRefreshCookie ||
			pol.class == classChallengeCookie {
			next.ServeHTTP(w, r)
			return
		}

		prin, ok := s.requireSession(w, r)
		if !ok {
			return
		}
		if prin.user.MustChangePassword && pol.class != classSessionMustChangeAllowed {
			writeError(w, r, http.StatusForbidden, codePasswordChangeRequired,
				"password change required before using this endpoint")
			return
		}
		if pol.class == classAdmin && !authz.Can(r.Context(), &prin.user, pol.action, nil) {
			writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
			return
		}

		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), prin)))
	})
}

// checkCSRF enforces the double-submit defense: every mutating request
// under /api/ that carries a session cookie must send the X-Hamlaneh-CSRF
// header matching the hamlaneh_csrf cookie. SameSite=Strict is the first
// line; this is the second. Login is exempt — no session exists yet. On
// failure it answers 403 csrf_failed and reports false.
func (s *apiServer) checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login" {
		return true
	}
	if _, err := r.Cookie(session.AccessCookie); err != nil {
		// No session cookie riding along means no ambient authority to
		// forge; refresh-cookie requests are protected by SameSite plus the
		// unguessable token itself.
		return true
	}

	csrfCookie, err := r.Cookie(session.CSRFCookie)
	if err != nil || !session.ValidCSRF(csrfCookie.Value, r.Header.Get(session.CSRFHeader)) {
		writeError(w, r, http.StatusForbidden, codeCSRFFailed, "missing or invalid CSRF token")
		return false
	}
	return true
}

// requireSession authenticates the access cookie against the session store.
// On failure it answers 401 not_authenticated and reports false. Cookies
// are not cleared here — only logout and refresh failures clear them.
func (s *apiServer) requireSession(w http.ResponseWriter, r *http.Request) (principal, bool) {
	if s.store == nil {
		internalError(w, r, errNoStorage)
		return principal{}, false
	}

	c, err := r.Cookie(session.AccessCookie)
	if err != nil || c.Value == "" {
		writeError(w, r, http.StatusUnauthorized, codeNotAuthenticated, msgNotAuthenticated)
		return principal{}, false
	}

	sess, user, err := s.store.SessionUserByAccessHash(r.Context(), session.HashToken(c.Value))
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, r, http.StatusUnauthorized, codeNotAuthenticated, msgNotAuthenticated)
		return principal{}, false
	}
	if err != nil {
		internalError(w, r, err)
		return principal{}, false
	}
	return principal{user: user, session: sess}, true
}

// clientIP identifies the calling client for rate limiting and session
// records. It returns the address (invalid when unparseable) and a
// non-empty rate-limit key.
//
// Trust model: the server sits behind Caddy on a private compose network,
// so X-Forwarded-For is consulted ONLY when the direct peer is a private,
// loopback, or link-local address. Caddy (v2.5+) drops client-supplied
// X-Forwarded-For values from untrusted sources by default, so the first
// hop of the header is the real client address as Caddy saw it. Any peer
// reaching the server directly is identified by its own address and its
// headers are ignored.
func clientIP(r *http.Request) (netip.Addr, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		// Real listeners always produce a host:port RemoteAddr; treat
		// anything else as a distinct opaque key rather than failing open
		// into a shared bucket per unique string.
		return netip.Addr{}, "peer:" + r.RemoteAddr
	}
	peer = peer.Unmap()

	if isTrustedProxy(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if fwd, fwdErr := netip.ParseAddr(strings.TrimSpace(first)); fwdErr == nil {
				fwd = fwd.Unmap()
				return fwd, fwd.String()
			}
		}
	}
	return peer, peer.String()
}

// isTrustedProxy reports whether the direct peer may speak for the client
// via X-Forwarded-For: private (RFC 1918 / ULA), loopback, or link-local
// addresses — the compose network Caddy lives on.
func isTrustedProxy(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast()
}
