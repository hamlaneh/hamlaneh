package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

// Names and shapes taken from a real `npm run build` of webapp/: one
// content-hashed module bundle plus stylesheet under assets/, the fonts
// @fontsource emits beside them, and the unhashed brand marks copied from
// webapp/public.
const (
	fixtureScript = "/assets/index-A1b2C3d4.js"
	fixtureStyle  = "/assets/index-Z9y8X7w6.css"
	fixtureFont   = "/assets/inter-latin-400-normal-C38fXH4l.woff2"
	fixtureMark   = "/brand/flat/symbol-light.svg"
)

// fixtureBuild stands in for webapp/dist. A plain checkout embeds only the
// placeholder document, so asset behaviour has to be exercised against
// something shaped like the real build.
func fixtureBuild() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><html lang="en" dir="ltr"><head>` +
			`<link rel="icon" type="image/svg+xml" href="` + fixtureMark + `">` +
			`<script type="module" crossorigin src="` + fixtureScript + `"></script>` +
			`<link rel="stylesheet" crossorigin href="` + fixtureStyle + `">` +
			`</head><body><div id="root"></div></body></html>`)},
		strings.TrimPrefix(fixtureScript, "/"): {Data: []byte("export const hamlaneh=1;\n")},
		strings.TrimPrefix(fixtureStyle, "/"):  {Data: []byte(":root{color-scheme:light dark}\n")},
		strings.TrimPrefix(fixtureFont, "/"):   {Data: []byte("wOF2 not-a-real-font")},
		strings.TrimPrefix(fixtureMark, "/"):   {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
	}
}

func getFixture(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	httpserver.HandlerWithWebBuild(nil, fixtureBuild()).ServeHTTP(rec, req)
	return rec
}

// TestSecurityHeaders pins every content-security header, by exact value, on
// every kind of response the server produces. The expected values are
// written out here rather than read from the package: changing what this
// instance promises browsers should take a deliberate edit in two places.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	const wantCSP = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self'; " +
		"connect-src 'self'; " +
		"img-src 'self'; " +
		"font-src 'self'; " +
		"object-src 'none'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'"

	const wantPermissions = "accelerometer=(), autoplay=(self), bluetooth=(), " +
		"browsing-topics=(), camera=(self), display-capture=(self), " +
		"encrypted-media=(), geolocation=(), gyroscope=(), hid=(), " +
		"idle-detection=(), local-fonts=(), magnetometer=(), " +
		"microphone=(self), midi=(), payment=(), serial=(), usb=(), " +
		"xr-spatial-tracking=()"

	wantHeaders := map[string]string{
		"Content-Security-Policy":      wantCSP,
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Permissions-Policy":           wantPermissions,
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	}

	// Every kind of response, because the middleware wraps the whole
	// handler: the HTML document, an asset, a health probe, a contract
	// route's error, and the router's own 404.
	paths := []string{"/", fixtureScript, "/healthz", "/api/v1/users/me", "/no-such-page"}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			rec := getFixture(t, http.MethodGet, path)
			for name, want := range wantHeaders {
				if got := rec.Header().Get(name); got != want {
					t.Errorf("GET %s: header %s = %q, want %q", path, name, got, want)
				}
			}
			// Deliberately absent — see securityheaders.go.
			if got := rec.Header().Get("Cross-Origin-Embedder-Policy"); got != "" {
				t.Errorf("GET %s: Cross-Origin-Embedder-Policy = %q, want it unset", path, got)
			}
		})
	}
}

// TestCSPForbidsUnsafeSources reads the shipped policy, not a copy of it, so
// a weakening cannot be made to pass by editing a test expectation. If the
// app ever appears to need one of these, the app is what needs fixing.
func TestCSPForbidsUnsafeSources(t *testing.T) {
	t.Parallel()

	forbidden := []string{"'unsafe-inline'", "'unsafe-eval'", "'unsafe-hashes'", "*"}
	for _, source := range forbidden {
		if strings.Contains(httpserver.ContentSecurityPolicy, source) {
			t.Errorf("CSP contains %s: %q", source, httpserver.ContentSecurityPolicy)
		}
	}
	// data: is legitimate in some directives and catastrophic in script-src.
	for _, directive := range strings.Split(httpserver.ContentSecurityPolicy, "; ") {
		if strings.HasPrefix(directive, "script-src") && strings.Contains(directive, "data:") {
			t.Errorf("CSP allows data: in script-src: %q", directive)
		}
	}
}

func TestWebappRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		path             string
		wantStatus       int
		wantContentType  string
		wantCacheControl string
		wantBodyContain  string
	}{
		{
			name:             "root serves the document",
			path:             "/",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			// The emailed reset link is {base}/reset#token=... — the token
			// is in the fragment, so this bare path is all the server sees,
			// and it has to resolve or the link is dead.
			name:             "emailed reset link path serves the document",
			path:             "/reset",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			name:             "channel route serves the document",
			path:             "/c/01J8ZQ2K3M4N5P6Q7R8S9T0V1W",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			name:             "message permalink serves the document",
			path:             "/c/01J8ZQ2K3M4N5P6Q7R8S9T0V1W/m/01J8ZQ2K3M4N5P6Q7R8S9T0V1X",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			// The invitation link is {base}/invite#token=..., the same
			// fragment trick /reset uses.
			name:             "invitation redemption path serves the document",
			path:             "/invite",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			// The account menu links here with a plain anchor, so this is a
			// hard navigation, not a client-side push. It 404'd for the whole
			// of 1.4: the dashboard shipped and could not be opened.
			name:             "admin dashboard path serves the document",
			path:             "/admin",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			// The panes are bookmarkable and survive a refresh, so the
			// subtree has to answer, not just the bare path. The client
			// routes /admin/* — matching that is the whole job.
			name:             "admin pane path serves the document",
			path:             "/admin/audit",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			// A conference link is the one client route whose token is a path
			// segment, so the subtree has to answer or every link 404s at the
			// document before the app runs.
			name:             "conference link path serves the document",
			path:             "/meet/aaaaaaaaaaaaaaaaaaaaaaaa",
			wantStatus:       http.StatusOK,
			wantContentType:  "text/html; charset=utf-8",
			wantCacheControl: "no-cache",
			wantBodyContain:  `id="root"`,
		},
		{
			name:             "hashed script is cached forever",
			path:             fixtureScript,
			wantStatus:       http.StatusOK,
			wantContentType:  "text/javascript; charset=utf-8",
			wantCacheControl: "public, max-age=31536000, immutable",
			wantBodyContain:  "hamlaneh",
		},
		{
			name:             "hashed stylesheet is cached forever",
			path:             fixtureStyle,
			wantStatus:       http.StatusOK,
			wantContentType:  "text/css; charset=utf-8",
			wantCacheControl: "public, max-age=31536000, immutable",
		},
		{
			name:             "hashed font is cached forever with its real type",
			path:             fixtureFont,
			wantStatus:       http.StatusOK,
			wantContentType:  "font/woff2",
			wantCacheControl: "public, max-age=31536000, immutable",
		},
		{
			// Unhashed: its URL is stable across releases, so pinning it for
			// a year would strand a changed logo in every browser cache.
			name:             "unhashed public file is revalidated",
			path:             fixtureMark,
			wantStatus:       http.StatusOK,
			wantContentType:  "image/svg+xml",
			wantCacheControl: "no-cache",
		},
		{
			name:       "missing asset is 404",
			path:       "/assets/index-Deleted0.js",
			wantStatus: http.StatusNotFound,
			// Cache-Control deliberately empty: an immutable 404 would
			// outlive the deploy that caused it by a year.
			wantCacheControl: "",
		},
		{
			name:             "asset directory is not listed",
			path:             "/assets/",
			wantStatus:       http.StatusNotFound,
			wantCacheControl: "",
		},
		{
			name:             "public directory is not listed",
			path:             "/brand/",
			wantStatus:       http.StatusNotFound,
			wantCacheControl: "",
		},
		{
			name:             "nested public directory is not listed",
			path:             "/brand/flat/",
			wantStatus:       http.StatusNotFound,
			wantCacheControl: "",
		},
		{
			// Client-side routes are enumerated in routeWebapp; a path that
			// is not one of them is honestly a 404, not a 200 page.
			name:       "path that is not a client route is 404",
			path:       "/not-a-client-route",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := getFixture(t, http.MethodGet, tt.path)

			if rec.Code != tt.wantStatus {
				t.Errorf("GET %s: status %d, want %d", tt.path, rec.Code, tt.wantStatus)
			}
			if tt.wantContentType != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.wantContentType {
					t.Errorf("GET %s: Content-Type = %q, want %q", tt.path, got, tt.wantContentType)
				}
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCacheControl {
				t.Errorf("GET %s: Cache-Control = %q, want %q", tt.path, got, tt.wantCacheControl)
			}
			if tt.wantBodyContain != "" && !strings.Contains(rec.Body.String(), tt.wantBodyContain) {
				t.Errorf("GET %s: body does not contain %q", tt.path, tt.wantBodyContain)
			}
		})
	}
}

// TestUnknownAPIPathReturnsJSONError pins that the SPA never swallows the
// API. A JSON client that asks for a path that does not exist must get the
// contract's error envelope, not an HTML document.
func TestUnknownAPIPathReturnsJSONError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "unknown v1 path", method: http.MethodGet, path: "/api/v1/no-such-endpoint"},
		{name: "unknown nested path", method: http.MethodGet, path: "/api/v1/users/me/no-such-thing"},
		{name: "unknown api version", method: http.MethodGet, path: "/api/v2/users/me"},
		{name: "api root", method: http.MethodGet, path: "/api/"},
		{name: "write to an unknown path", method: http.MethodPost, path: "/api/v1/no-such-endpoint"},
		{name: "delete an unknown path", method: http.MethodDelete, path: "/api/v1/no-such-endpoint"},
		// A contract route exists at this path but only for POST. It answers
		// through the same JSON envelope rather than ServeMux's plain-text
		// 405, which is what a JSON client can actually parse.
		{name: "wrong method on a real route", method: http.MethodGet, path: "/api/v1/auth/login"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := getFixture(t, tt.method, tt.path)

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want %d", tt.method, tt.path, rec.Code, http.StatusNotFound)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type = %q, want application/json", tt.method, tt.path, ct)
			}
			if body := rec.Body.String(); strings.Contains(strings.ToLower(body), "<!doctype") {
				t.Errorf("%s %s: answered with an HTML document: %s", tt.method, tt.path, body)
			}

			var got struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s %s: body is not the contract Error shape: %v (%s)",
					tt.method, tt.path, err, rec.Body.String())
			}
			if got.Error.Code != "not_found" {
				t.Errorf("%s %s: error code = %q, want %q", tt.method, tt.path, got.Error.Code, "not_found")
			}
			if got.Error.Message == "" {
				t.Errorf("%s %s: error message is empty", tt.method, tt.path)
			}
		})
	}
}

// TestHealthProbesUnaffectedByWebapp pins that adding the web application
// did not change what an orchestrator sees. Both probes are wired by the
// generated router, and both must keep answering JSON.
func TestHealthProbesUnaffectedByWebapp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path       string
		wantCode   int
		wantStatus string
	}{
		{path: "/healthz", wantCode: http.StatusOK, wantStatus: "ok"},
		// No storage is wired here, so readiness is honestly degraded; the
		// point is that it is still the probe answering, not the SPA.
		{path: "/readyz", wantCode: http.StatusServiceUnavailable, wantStatus: "degraded"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			rec := getFixture(t, http.MethodGet, tc.path)

			if rec.Code != tc.wantCode {
				t.Errorf("GET %s: status %d, want %d", tc.path, rec.Code, tc.wantCode)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("GET %s: Content-Type = %q, want application/json", tc.path, ct)
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("GET %s: body is not JSON: %v (%s)", tc.path, err, rec.Body.String())
			}
			if got["status"] != tc.wantStatus {
				t.Errorf("GET %s: status field = %q, want %q", tc.path, got["status"], tc.wantStatus)
			}
		})
	}
}

// TestWebappRefusesPathTraversal pins the property gosec's G703 taint
// analysis flags on serveFile: the file name reaching the filesystem comes
// from the request. It cannot escape, and this test is what says so rather
// than the comment above the call.
//
// The fixture build carries a file outside every served route, so a request
// that reached it would be reading something the deploy never meant to
// publish — which is exactly the failure G703 describes, expressed as bytes
// rather than as an argument about layers.
func TestWebappRefusesPathTraversal(t *testing.T) {
	t.Parallel()

	const secret = "this file is not published"
	build := fixtureBuild()
	build["secrets/deploy-notes.txt"] = &fstest.MapFile{Data: []byte(secret)}

	// Literal, percent-encoded, doubled and mixed-route forms: ServeMux
	// cleans some of these before a handler sees them, fs.FS rejects the
	// rest, and the assertion below does not care which layer refused.
	hostile := []string{
		"/assets/../secrets/deploy-notes.txt",
		"/assets/..%2fsecrets%2fdeploy-notes.txt",
		"/assets/%2e%2e/secrets/deploy-notes.txt",
		"/assets/....//secrets/deploy-notes.txt",
		"/brand/../secrets/deploy-notes.txt",
		"/brand/../../secrets/deploy-notes.txt",
		"/secrets/deploy-notes.txt",
	}

	for _, target := range hostile {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			httpserver.HandlerWithWebBuild(nil, build).ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("status 200 for a path outside the served routes")
			}
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("the unpublished file's contents were served (status %d)", rec.Code)
			}
		})
	}
}
