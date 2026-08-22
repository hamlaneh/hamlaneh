package httpserver

import (
	"net/http"
	"strings"
)

// The application's content-security headers live HERE, not in
// deploy/Caddyfile, and the split is deliberate.
//
// Caddy keeps only what a TLS terminator uniquely owns (HSTS). Everything
// below describes what the application loads, so the application is the only
// thing that can keep it correct — and a policy that lives in the proxy does
// not exist at all in home mode, where a single binary serves the app with
// nothing in front of it. Two copies would be worse than one in the wrong
// place: browsers AND multiple Content-Security-Policy headers, so two
// divergent copies silently produce a third policy nobody wrote.

// cspDirectives is the Content-Security-Policy, derived from what the built
// bundle in webapp/dist actually loads rather than copied from a template.
// Every entry records why it is what it is.
var cspDirectives = []string{
	// Everything the app needs is same-origin. The directives below are the
	// exceptions and the deliberate restatements, never additions.
	"default-src 'self'",

	// The bundle is one same-origin module script. No inline script and no
	// eval: Vite emits neither, and the QR code that two-step setup injects
	// as inline SVG is inert markup precisely because this stays strict. If
	// a change ever appears to need 'unsafe-inline' or 'unsafe-eval', the
	// change is the bug — see TestCSPForbidsUnsafeSources.
	"script-src 'self'",

	// One same-origin stylesheet. React applies element styles through the
	// CSSOM, which CSP does not govern, so nothing needs an inline-style
	// allowance either.
	"style-src 'self'",

	// Stated rather than inherited from default-src. The app already opens a
	// same-origin WebSocket (wss://<host>/api/v1/ws, see
	// webapp/src/chat/realtime.ts) and Phase 1.2a puts real traffic on it.
	// Whether 'self' covers wss: is a CSP Level 3 rule — it does, for the
	// same host and port over https — that readers routinely doubt, and an
	// invisible inherited directive is the worst possible place to have that
	// argument at 3am. A bare "wss:" is deliberately NOT added: it would
	// allow a socket to any host on the internet, which is an exfiltration
	// channel, not a fix.
	"connect-src 'self'",

	// Same-origin only, which is a product decision as much as a policy one:
	// webapp/src/components/chat/MessageContent.tsx renders a markdown image
	// as a link instead of loading it, so a message can never make a
	// reader's browser reveal its IP to whatever host the author chose. The
	// browser now enforces that too. No data: — the build inlines nothing
	// (every font and mark in webapp/dist is a separate file), so allowing
	// it would buy nothing and widen the injection surface.
	"img-src 'self'",

	// Inter and Vazirmatn are self-hosted through @fontsource and land in
	// webapp/dist/assets as .woff2/.woff files: no CDN, and no data: URIs in
	// the emitted CSS (checked against the real build output, not assumed).
	"font-src 'self'",

	// The app embeds no plugins, ever.
	"object-src 'none'",

	// Nothing may frame this instance, so clickjacking has nothing to work
	// with.
	"frame-ancestors 'none'",

	// The document has no <base>, so pin it shut: an injected <base> is how
	// an attacker re-points every relative URL on the page at their host.
	"base-uri 'none'",

	// Every form the app has posts to its own API through fetch. This is
	// what stops an injected form from posting a password somewhere else.
	"form-action 'self'",
}

// contentSecurityPolicy is the assembled header value.
var contentSecurityPolicy = strings.Join(cspDirectives, "; ")

// permissionsPolicy denies the browser capabilities the product will never
// ask for, and names the ones it will.
//
// camera, microphone, display-capture and autoplay are deliberately NOT
// denied: Phase 2 puts LiveKit calls and screen share on exactly those, and
// a denial here would fail silently a year from now in a place nobody would
// think to look. They are written as (self) rather than omitted so the
// intent is on the record and nobody "tidies" them to ().
var permissionsPolicy = strings.Join([]string{
	"accelerometer=()",
	"autoplay=(self)",
	"bluetooth=()",
	"browsing-topics=()",
	"camera=(self)",
	"display-capture=(self)",
	"encrypted-media=()",
	"geolocation=()",
	"gyroscope=()",
	"hid=()",
	"idle-detection=()",
	"local-fonts=()",
	"magnetometer=()",
	"microphone=(self)",
	"midi=()",
	"payment=()",
	"serial=()",
	"usb=()",
	"xr-spatial-tracking=()",
}, ", ")

// referrerPolicy is no-referrer rather than the usual
// strict-origin-when-cross-origin: chat messages link to arbitrary
// third-party sites, and this instance's hostname is itself sensitive — it
// names a private team and, for a self-hosted install, often the
// organisation behind it. Leaking that origin to every site anyone ever
// clicks is a disclosure with no upside. Nothing here reads Referer: CSRF is
// a double-submit token (see securityMiddleware), not an origin check.
const referrerPolicy = "no-referrer"

// securityHeaders wraps h with the headers every Hamlaneh response carries,
// whatever produced it.
//
// It wraps the WHOLE handler on purpose. api.Middlewares covers only the
// generated contract routes, which would leave the HTML document — the one
// response an injection actually targets — completely bare.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", referrerPolicy)
		h.Set("Permissions-Policy", permissionsPolicy)

		// Severs this document from any window that opened it or that it
		// opens, so a popup can never reach back through window.opener.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// Other sites may not load our responses as subresources.
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		// Cross-Origin-Embedder-Policy is deliberately absent. It exists to
		// unlock SharedArrayBuffer and high-resolution timers, which nothing
		// here uses, and require-corp breaks every embed whose host has not
		// opted in — a real cost for nothing bought. Do not add it "for
		// completeness".

		next.ServeHTTP(w, r)
	})
}
