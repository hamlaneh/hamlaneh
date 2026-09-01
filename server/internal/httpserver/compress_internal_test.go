package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// TestAcceptsGzip covers the spellings real clients send. The q rows are why
// this is a parser rather than a substring search: "gzip;q=0" names gzip and
// refuses it in the same breath.
func TestAcceptsGzip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "absent header is no", header: "", want: false},
		{name: "bare token", header: "gzip", want: true},
		{name: "browser list", header: "gzip, deflate, br, zstd", want: true},
		{name: "not first in the list", header: "br, gzip", want: true},
		{name: "case is not significant", header: "GZIP", want: true},
		{name: "padded with spaces", header: " gzip , deflate ", want: true},
		{name: "q=0 refuses", header: "gzip;q=0", want: false},
		{name: "q=0.0 refuses", header: "gzip;q=0.0", want: false},
		{name: "q=0 among others still refuses", header: "identity, gzip;q=0", want: false},
		{name: "a low q still accepts", header: "gzip;q=0.1", want: true},
		{name: "q=1 accepts", header: "gzip;q=1.0", want: true},
		{name: "uppercase parameter", header: "gzip;Q=0", want: false},
		{name: "another encoding only", header: "br, zstd", want: false},
		{name: "gzip must be a token, not a substring", header: "notgzip", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequest(http.MethodGet, "/assets/index-A1b2C3d4.js", nil)
			if tt.header != "" {
				r.Header.Set("Accept-Encoding", tt.header)
			}
			if got := acceptsGzip(r); got != tt.want {
				t.Errorf("acceptsGzip(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// TestCompressibleLeavesAnEncodedResponseAlone pins the second guard against
// double-compression, the one that does not depend on the install being
// configured correctly: a response that already carries a Content-Encoding is
// never wrapped in another. The first guard is that compression is off unless
// the install asks, and the compose stack — where Caddy is the compressor —
// never asks (compress.go).
func TestCompressibleLeavesAnEncodedResponseAlone(t *testing.T) {
	t.Parallel()

	const (
		name = "assets/index-A1b2C3d4.js"
		size = 64 * 1024
	)
	a := &webapp{files: fstest.MapFS{}, compress: true}

	encoded := httptest.NewRecorder()
	encoded.Header().Set("Content-Encoding", "zstd")
	if a.compressible(encoded, name, size) {
		t.Error("a response that is already encoded was offered for gzip")
	}

	// The control: the same file, same size, same server — only the header
	// differs, so the row above cannot pass for the wrong reason.
	plain := httptest.NewRecorder()
	if !a.compressible(plain, name, size) {
		t.Error("an unencoded script was not offered for gzip")
	}
}
