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
// action for classAdmin routes. totpEnrollmentAllowed marks the routes a
// session flagged totp_enrollment_required may still reach — logout,
// reading and patching users/me, and the TOTP enrolment endpoints (ADR
// 004); everything else answers 403 totp_enrollment_required. It is a field
// on the policy rather than a second table so a route's whole security
// posture is decided in one entry.
type routePolicy struct {
	class                 routeClass
	action                authz.Action
	totpEnrollmentAllowed bool
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
	"POST /api/v1/auth/logout":          {class: classSessionMustChangeAllowed, totpEnrollmentAllowed: true},
	"POST /api/v1/auth/change-password": {class: classSessionMustChangeAllowed},
	"GET /api/v1/users/me":              {class: classSessionMustChangeAllowed, totpEnrollmentAllowed: true},
	// The patch carries locale and nothing else, so both gates admit it:
	// somebody whose account language is wrong must not be stuck on a
	// forced screen they cannot read with the switcher inert (contract,
	// User.totp_enrollment_required).
	"PATCH /api/v1/users/me":           {class: classSessionMustChangeAllowed, totpEnrollmentAllowed: true},
	"GET /api/v1/instance":             {class: classPublic},
	"POST /api/v1/auth/reset-request":  {class: classPublic},
	"POST /api/v1/auth/reset-complete": {class: classPublic},
	"POST /api/v1/auth/login/totp":     {class: classChallengeCookie},
	"GET /api/v1/admin/users":          {class: classAdmin, action: authz.AdminUsersList},
	"POST /api/v1/admin/users":         {class: classAdmin, action: authz.AdminUsersCreate},

	// Phase 1.6 single sign-on (ADR 004 slice 2). The two flow halves are
	// public — start is the door into authentication and the callback is a
	// cross-site navigation that carries no session by design (the
	// transaction cookie, not a session, is its state). The Settings pair is
	// plain session work: neither joins the must-change trio, and neither is
	// reachable under the totp-enrolment gate — changing how an account
	// signs in is not enrolment.
	"GET /api/v1/auth/oidc/start":    {class: classPublic},
	"GET /api/v1/auth/oidc/callback": {class: classPublic},
	"POST /api/v1/users/me/oidc":     {class: classSession},
	"DELETE /api/v1/users/me/oidc":   {class: classSession},

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
	"POST /api/v1/channels/{channelId}/files":                  {class: classSession},
	"GET /api/v1/channels/{channelId}/messages":                {class: classSession},
	"POST /api/v1/channels/{channelId}/messages":               {class: classSession},
	"PATCH /api/v1/channels/{channelId}/messages/{messageId}":  {class: classSession},
	"DELETE /api/v1/channels/{channelId}/messages/{messageId}": {class: classSession},
	"PUT /api/v1/channels/{channelId}/read":                    {class: classSession},
	"POST /api/v1/dms":                                         {class: classSession},
	"GET /api/v1/search":                                       {class: classSession},

	// Phase 2 calls (ADR 005). Both are ordinary channel-scoped session
	// routes: the class carries no resource-level meaning, and membership is
	// an explicit authz.Can call inside each handler. The media server's own
	// webhook is deliberately absent — it is not a contract route, it carries
	// no session, and it is mounted outside this middleware entirely
	// (call_handlers.go).
	"GET /api/v1/channels/{channelId}/call":        {class: classSession},
	"POST /api/v1/channels/{channelId}/call/token": {class: classSession},

	// Phase 2 conferences (ADR 005). The three signed-in routes are ordinary
	// session work — making a conference needs no permission beyond a
	// session, and who may revoke one is a resource-level authz.Can call
	// inside the handler, because it turns on who made it.
	//
	// The two /meet routes are public and MUST be: a conference link is for
	// somebody with no account on this instance, which is the feature. Being
	// public leaks nothing — every unusable link answers one 404 on both, and
	// a session neither helps nor hinders, exactly as on the invite pair.
	// What keeps the door narrow is not this table but what the link buys: a
	// ticket to one media room, no session, and no other route in this table
	// honouring it.
	"GET /api/v1/conferences":                   {class: classSession},
	"POST /api/v1/conferences":                  {class: classSession},
	"DELETE /api/v1/conferences/{conferenceId}": {class: classSession},
	"GET /api/v1/meet/{token}":                  {class: classPublic},
	"POST /api/v1/meet/{token}/join":            {class: classPublic},

	// Phase 3 slice 1: the E2EE transport (ADR 006). Every one of the eight
	// is ordinary session work, and neither gate lets a flagged account past:
	// somebody who owes a password change or an enrolment fixes that before
	// registering a device or moving a group's epoch.
	//
	// The class carries no resource-level meaning here either. The five
	// channel-scoped ones are membership decisions made by an explicit
	// authz.Can call inside the handler (mls_handlers.go), and the four under
	// /users/me are decided by the session alone — their subject IS the
	// session's user, and no request on them names another.
	"POST /api/v1/users/me/mls/device":                         {class: classSession},
	"PUT /api/v1/users/me/mls/devices/{deviceId}/key-packages": {class: classSession},
	"GET /api/v1/users/me/mls/welcomes":                        {class: classSession},
	"DELETE /api/v1/users/me/mls/welcomes/{welcomeId}":         {class: classSession},
	"GET /api/v1/channels/{channelId}/mls/group":               {class: classSession},
	"POST /api/v1/channels/{channelId}/mls/group":              {class: classSession},
	"POST /api/v1/channels/{channelId}/mls/key-package-claims": {class: classSession},
	"GET /api/v1/channels/{channelId}/mls/commits":             {class: classSession},
	"POST /api/v1/channels/{channelId}/mls/commits":            {class: classSession},

	// Phase 3 slice 2: the member-device directory (ADR 007). Same class as
	// the five above and for the same reason — membership is the whole rule,
	// asked by an explicit authz.Can inside the handler. The information is
	// the class co-members already learn at claim time, so nothing about the
	// gate changes because the read is the one an eviction sweep trusts.
	"GET /api/v1/channels/{channelId}/mls/member-devices": {class: classSession},

	// Phase 1.1b self-service security. All session-gated; none joins the
	// must-change trio, because a user who owes a password change fixes that
	// before touching their second factor or their device list.
	//
	// The first four are the enrolment path — status, setup, verify,
	// activate — and stay reachable under the totp-enrolment gate: they ARE
	// the way out of it. disable and recovery-codes are deliberately not:
	// the gate exists to make an account grow a second factor, and neither
	// removing one nor minting fresh sign-in codes is enrolment.
	"GET /api/v1/users/me/totp":                    {class: classSession, totpEnrollmentAllowed: true},
	"POST /api/v1/users/me/totp/setup":             {class: classSession, totpEnrollmentAllowed: true},
	"POST /api/v1/users/me/totp/verify":            {class: classSession, totpEnrollmentAllowed: true},
	"POST /api/v1/users/me/totp/activate":          {class: classSession, totpEnrollmentAllowed: true},
	"POST /api/v1/users/me/totp/disable":           {class: classSession},
	"POST /api/v1/users/me/totp/recovery-codes":    {class: classSession},
	"GET /api/v1/users/me/sessions":                {class: classSession},
	"DELETE /api/v1/users/me/sessions/{familyId}":  {class: classSession},
	"POST /api/v1/users/me/sessions/revoke-others": {class: classSession},
	"GET /api/v1/ws":                               {class: classSession},

	// Phase 1.4 administration. Every dashboard route is classAdmin, so the
	// admin check is made once, in one place, before any handler runs.
	"PATCH /api/v1/admin/users/{userId}":               {class: classAdmin, action: authz.AdminUsersUpdate},
	"POST /api/v1/admin/users/{userId}/reset-password": {class: classAdmin, action: authz.AdminUsersResetPassword},
	"GET /api/v1/admin/invites":                        {class: classAdmin, action: authz.AdminInvitesList},
	"POST /api/v1/admin/invites":                       {class: classAdmin, action: authz.AdminInvitesCreate},
	"DELETE /api/v1/admin/invites/{inviteId}":          {class: classAdmin, action: authz.AdminInvitesRevoke},
	"GET /api/v1/admin/org":                            {class: classAdmin, action: authz.AdminOrgRead},
	"PATCH /api/v1/admin/org":                          {class: classAdmin, action: authz.AdminOrgUpdate},
	// Phase 3: the encryption mode (ADR 011). Its own route rather than a
	// field on the patch above, and the same instance-settings authority —
	// changing what conversations are born as is an org-settings write.
	"PUT /api/v1/admin/org/encryption-mode": {class: classAdmin, action: authz.AdminOrgUpdate},
	// Read-only, and the only read in the table that is also the record of
	// every other route above it.
	"GET /api/v1/admin/audit": {class: classAdmin, action: authz.AdminAuditList},

	// Phase 1.6 SCIM provisioning tokens (ADR 004 slice 3). These three are
	// ordinary admin contract routes and go through every gate above. The
	// provisioning surface they mint credentials FOR is not in this table at
	// all: /scim/v2 is mounted outside this middleware and authenticates by
	// bearer token alone, which is what makes a session cookie worthless
	// there and one of these tokens worthless here (scim.md §3).
	"GET /api/v1/admin/scim/tokens":              {class: classAdmin, action: authz.AdminScimTokensList},
	"POST /api/v1/admin/scim/tokens":             {class: classAdmin, action: authz.AdminScimTokensCreate},
	"DELETE /api/v1/admin/scim/tokens/{tokenId}": {class: classAdmin, action: authz.AdminScimTokensRevoke},

	// The two public halves of an invitation. They must be reachable before
	// anybody has an account — that is what an invitation is for — and they
	// are the only routes in this table that a signed-in session neither
	// helps nor hinders. Every unusable token answers one 404 on both, so
	// being public leaks nothing a guesser can use.
	"GET /api/v1/invites/{token}":  {class: classPublic},
	"POST /api/v1/invites/{token}": {class: classPublic},
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
// the must-change-password gate, the totp-enrolment gate, and the admin
// authz gate. Handlers behind it receive the principal via the request
// context and perform only their own resource-level authz.Can checks.
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
		// The totp-enrolment gate mirrors the must-change gate: the flag was
		// decided ONCE, when this session was minted — the middleware only
		// reads it off the session row, never org settings, which is what
		// keeps "at the next sign-in, never mid-session" true (ADR 004).
		//
		// It yields to an active must-change gate: an account carrying both
		// flags changes its password first (reachable above), then enrols —
		// gating change-password here instead would deadlock the account out
		// of both obligations. It runs before the admin check so a flagged
		// admin is gated exactly like a flagged member.
		if !prin.user.MustChangePassword && prin.session.TotpEnrollmentRequired &&
			!pol.totpEnrollmentAllowed {
			writeError(w, r, http.StatusForbidden, codeTOTPEnrollmentRequired,
				"two-step verification must be set up before using this endpoint")
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
