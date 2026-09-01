package httpserver_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

// The build these tests serve is the shared fixture's shape with realistic
// sizes: the real bundle is hundreds of kilobytes, and every file in
// fixtureBuild is one line, which is below the size floor compression
// applies. Names carry a content hash like Vite's real output.
const (
	bigScript = "/assets/index-C0mpr3ssM3.js"
	bigWasm   = "/assets/hamlaneh_mls_bg-D4t9x1zQ.wasm"
	bigFont   = "/assets/inter-latin-400-normal-C38fXH4l.woff2"
	tinyMark  = "/brand/flat/symbol-light.svg"
)

// compressFixtureBuild stands in for webapp/dist with files on both sides of
// every decision: compressible and not, above the size floor and below it.
func compressFixtureBuild() fstest.MapFS {
	// Repetitive, so a compressed body is unmistakably shorter than the
	// identity one rather than shorter by luck.
	padding := strings.Repeat("// hamlaneh padding\n", 64)
	return fstest.MapFS{
		"index.html": {Data: []byte(`<!doctype html><html lang="en" dir="ltr"><head>` +
			`<script type="module" crossorigin src="` + bigScript + `"></script>` +
			`</head><body><div id="root"></div><!--` + padding + `--></body></html>`)},
		strings.TrimPrefix(bigScript, "/"): {Data: []byte("export const hamlaneh=1;\n" + padding)},
		// wasm-bindgen output: the module's magic number, then bytes that
		// compress. It is the largest file the real build emits.
		strings.TrimPrefix(bigWasm, "/"): {Data: []byte("\x00asm\x01\x00\x00\x00" + padding)},
		// Already a compressed container, and well over the size floor: the
		// row that proves the floor is not the only thing holding it back.
		strings.TrimPrefix(bigFont, "/"): {Data: []byte("wOF2" + padding)},
		// Compressible by type but far under the floor.
		strings.TrimPrefix(tinyMark, "/"): {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
	}
}

// fixtureBytes is what a request for path must eventually yield, compressed
// or not.
func fixtureBytes(t *testing.T, path string) []byte {
	t.Helper()

	name := strings.TrimPrefix(path, "/")
	if path == "/" {
		name = "index.html"
	}
	file, ok := compressFixtureBuild()[name]
	if !ok {
		t.Fatalf("fixture build has no %s", name)
	}
	return file.Data
}

// TestCompressionOfTheWebBuild covers the whole decision: who asks, what is
// worth compressing, and the default that keeps the compose stack's Caddy
// (`encode zstd gzip`) the only thing compressing there.
func TestCompressionOfTheWebBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		path            string
		accept          string
		enabled         bool
		wantGzip        bool
		wantVary        bool
		wantContentType string
	}{
		{
			name:            "the bundle is gzipped when the client asks",
			path:            bigScript,
			accept:          "gzip, deflate, br, zstd",
			enabled:         true,
			wantGzip:        true,
			wantVary:        true,
			wantContentType: "text/javascript; charset=utf-8",
		},
		{
			name:            "so is the document",
			path:            "/",
			accept:          "gzip",
			enabled:         true,
			wantGzip:        true,
			wantVary:        true,
			wantContentType: "text/html; charset=utf-8",
		},
		{
			// The reason the roadmap bullet is not just about JavaScript.
			name:     "so is the wasm core, the largest file in the build",
			path:     bigWasm,
			accept:   "gzip",
			enabled:  true,
			wantGzip: true,
			wantVary: true,
		},
		{
			name:            "a client that does not ask gets the bytes as they are",
			path:            bigScript,
			accept:          "",
			enabled:         true,
			wantGzip:        false,
			wantVary:        true,
			wantContentType: "text/javascript; charset=utf-8",
		},
		{
			// "gzip;q=0" is the header's way of spelling "not gzip". A
			// substring match for "gzip" gets this row backwards.
			name:     "q=0 is a refusal, not a request",
			path:     bigScript,
			accept:   "gzip;q=0, identity",
			enabled:  true,
			wantGzip: false,
			wantVary: true,
		},
		{
			name:     "a low but non-zero q is still a request",
			path:     bigScript,
			accept:   "gzip;q=0.1",
			enabled:  true,
			wantGzip: true,
			wantVary: true,
		},
		{
			// woff2 is already compressed; gzipping it spends CPU to make it
			// bigger. No second representation exists, so no Vary either.
			name:            "an already-compressed format is left alone",
			path:            bigFont,
			accept:          "gzip",
			enabled:         true,
			wantGzip:        false,
			wantVary:        false,
			wantContentType: "font/woff2",
		},
		{
			name:            "a file below the size floor is left alone",
			path:            tinyMark,
			accept:          "gzip",
			enabled:         true,
			wantGzip:        false,
			wantVary:        false,
			wantContentType: "image/svg+xml",
		},
		{
			// The default, and what every compose install stays on: Caddy is
			// in front doing `encode zstd gzip`, and two compressors racing
			// is worse than one doing it well.
			name:     "off by default, so the proxy stays the only compressor",
			path:     bigScript,
			accept:   "gzip",
			enabled:  false,
			wantGzip: false,
			wantVary: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []httpserver.Option
			if tt.enabled {
				opts = append(opts, httpserver.WithCompression(true))
			}

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.accept != "" {
				req.Header.Set("Accept-Encoding", tt.accept)
			}
			rec := httptest.NewRecorder()
			httpserver.HandlerWithWebBuild(nil, compressFixtureBuild(), opts...).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status %d, want 200", tt.path, rec.Code)
			}

			wantEncoding := ""
			if tt.wantGzip {
				wantEncoding = "gzip"
			}
			if got := rec.Header().Get("Content-Encoding"); got != wantEncoding {
				t.Errorf("GET %s: Content-Encoding = %q, want %q", tt.path, got, wantEncoding)
			}
			// A cache that does not key on Accept-Encoding hands a gzipped
			// body to the next client along, whether or not it can read one.
			hasVary := strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding")
			if hasVary != tt.wantVary {
				t.Errorf("GET %s: Vary = %q, want Accept-Encoding present = %v",
					tt.path, rec.Header().Get("Vary"), tt.wantVary)
			}
			// Compressing must not disturb the type. With nosniff set on
			// every response, a script served as anything else is a script
			// the browser refuses to run.
			if tt.wantContentType != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.wantContentType {
					t.Errorf("GET %s: Content-Type = %q, want %q", tt.path, got, tt.wantContentType)
				}
			}

			want := fixtureBytes(t, tt.path)
			body := rec.Body.Bytes()
			if tt.wantGzip {
				if len(body) >= len(want) {
					t.Errorf("GET %s: compressed body is %d bytes against %d uncompressed — compression bought nothing",
						tt.path, len(body), len(want))
				}
				body = gunzip(t, body)
			}
			if !bytes.Equal(body, want) {
				t.Errorf("GET %s: served %d bytes, want the fixture's %d", tt.path, len(body), len(want))
			}
		})
	}
}

// TestCompressionNeverReachesTheAPI is the BREACH line, and it is a line
// rather than a tuning knob. Compression covers the embedded web build and
// nothing else, whatever the client asks for and whatever the install turned
// on: API responses mix one caller's secrets with another user's chosen text,
// which is the exact shape a compressed length leaks (compress.go carries the
// argument).
//
// If this test is failing because compression was widened to /api, that is a
// decision with a security argument standing against it — read compress.go
// before changing the expectation here.
func TestCompressionNeverReachesTheAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "liveness probe", path: "/healthz"},
		{name: "readiness probe", path: "/readyz"},
		{name: "a real contract route", path: "/api/v1/admin/users?limit=not-a-number"},
		{name: "the contract's own 404", path: "/api/v1/no-such-route"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
			rec := httptest.NewRecorder()
			httpserver.HandlerWithWebBuild(&fakeStore{}, compressFixtureBuild(),
				httpserver.WithCompression(true)).ServeHTTP(rec, req)

			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("GET %s: Content-Encoding = %q, want none", tt.path, got)
			}
			// Nothing here has a second representation, so nothing here
			// varies on the encoding either. This catches a widening that
			// declined to compress one short body but would compress a long
			// one.
			if got := rec.Header().Get("Vary"); strings.Contains(got, "Accept-Encoding") {
				t.Errorf("GET %s: Vary = %q — this response has no encoded variant to advertise", tt.path, got)
			}
		})
	}
}

// gunzip decompresses body, failing the test if it is not a complete gzip
// stream. Close is what checks the trailer, so a truncated body fails here
// rather than passing as a shorter one.
func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading the gzip stream: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("the gzip stream is incomplete: %v", err)
	}
	return decoded
}
