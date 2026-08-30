package httpserver

import (
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// Response compression, for HOME MODE.
//
// # What is compressed, and why only that
//
// Only the embedded web build — the document, /assets/*, /brand/* — is ever
// gzipped. API responses are not, and that is a security decision rather than
// an omission.
//
// Compressing a body that mixes a secret with attacker-chosen bytes leaks the
// secret: the compressed length reports how much of the guess matched, one
// byte at a time (BREACH, CVE-2013-3587). Both halves of that are true of
// this application's API, and neither is true of its bundle.
//
//   - The bundle is a compile-time artifact (internal/webassets). Every
//     install serves the same bytes to every caller, nothing from a request
//     reaches the body, and there is no secret in it. A length measures
//     nothing an attacker does not already have.
//   - API responses carry both halves. Anyone who can post chooses bytes that
//     come back inside somebody else's response — message bodies, channel
//     names, display names, search terms — and those same responses carry
//     per-account secrets: MLS key packages and encrypted backup blobs
//     (mls_handlers.go), invitation tokens (invite_handlers.go), two-step
//     enrolment secrets (totp_handlers.go). Compressing them would assemble
//     the oracle by hand.
//
// The CSRF token is the one secret NOT at risk, and it is worth naming
// because it is the textbook BREACH target: it rides in a cookie and the
// X-Hamlaneh-CSRF header (middleware.go checkCSRF), never in a body, and
// headers are not part of the compressed entity. Its safety is not what makes
// the API safe to compress — the secrets listed above are why the API is not.
//
// Compressing API JSON safely means length randomisation, or masking every
// secret per response; machinery with no business being built to make a
// 560 KB bundle smaller. Widening this is an ADR, not an edit here.
// TestCompressionNeverReachesTheAPI holds the line.
//
// # Why it cannot double-compress behind Caddy
//
// It is off unless the install sets HAMLANEH_COMPRESS_RESPONSES=1, and the
// compose stack never does: deploy/Caddyfile already runs `encode zstd gzip`
// on both site blocks. Those installs keep zstd — a better ratio than gzip on
// this bundle — and this process spends no CPU racing it. Home mode is a
// single binary with nothing in front of it (CLAUDE.md, "Packaging"), and is
// the install this exists for.
//
// Setting it behind a proxy anyway is a misconfiguration rather than a
// hazard, and the reason belongs on the record: Caddy's encode handler skips
// any upstream response that already carries a Content-Encoding, so the
// client would receive one gzip layer, never gzip inside zstd. This side
// refuses symmetrically — see compressible, which leaves an already-encoded
// response alone.

// EnvCompressResponses turns response compression on when set to "1". Home
// mode sets it; anything with a compressing proxy in front leaves it unset.
const EnvCompressResponses = "HAMLANEH_COMPRESS_RESPONSES"

// minCompressSize is the smallest body worth compressing. Below it, gzip's
// header and trailer plus a round of CPU buy nothing, and a short file can
// come out larger than it went in.
const minCompressSize = 512

// compressibleExts are the extensions in the web build whose bytes actually
// shrink. The absentees are the point: .png, .webp, .woff and .woff2 are
// already-compressed container formats, and gzipping one spends CPU to make
// it very slightly bigger.
//
// .wasm is the largest single file the build emits — the MLS core, around
// 489 KB (ADR 006) — and it compresses like the code it is. The extension is
// what decides compressibility here, deliberately independent of the
// Content-Type allowlist in webapp.go.
var compressibleExts = map[string]bool{
	".css":  true,
	".html": true,
	".js":   true,
	".json": true,
	".md":   true,
	".svg":  true,
	".txt":  true,
	".wasm": true,
}

// compressible reports whether name's bytes are worth gzipping at all. That
// is a different question from whether this particular client wants them
// gzipped: a yes here means the URL has two representations, so the response
// varies on Accept-Encoding even when the answer served is the plain one.
func (a *webapp) compressible(w http.ResponseWriter, name string, size int64) bool {
	if !a.compress || size < minCompressSize {
		return false
	}
	// Never encode twice. Nothing in this package sets Content-Encoding
	// today; a pre-compressed asset shipped later would, and this is what
	// keeps it from going out as gzip wrapped around gzip.
	if w.Header().Get("Content-Encoding") != "" {
		return false
	}
	return compressibleExts[strings.ToLower(path.Ext(name))]
}

// serveGzipped writes one file from the build, gzipped.
//
// It deliberately does not go through http.ServeFileFS. What that helper adds
// over a copy is Content-Length, byte ranges and conditional requests, and
// compressing on the fly invalidates the first two: the length it would send
// is the uncompressed one, and a byte range names offsets into bytes the
// client is no longer receiving. The third is already inert — an embedded
// file has a zero modification time, so ServeContent sends no Last-Modified
// to revalidate against. Copying the bytes here is smaller and plainer than a
// ResponseWriter wrapper that has to undo all three.
func (a *webapp) serveGzipped(w http.ResponseWriter, r *http.Request, name string) {
	f, err := a.files.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			slog.Error("close web asset", "name", name, "error", closeErr)
		}
	}()

	w.Header().Set("Content-Encoding", "gzip")

	gz := gzip.NewWriter(w)
	_, copyErr := io.Copy(gz, f)
	// Close flushes the deflate stream and writes gzip's trailer, so it runs
	// even after a failed copy: skipping it would leave a body that no client
	// can finish decompressing on a connection that may yet recover.
	closeErr := gz.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		// The status line went out with the first flush, so logging is all
		// that is left — the same bargain writeBody makes.
		slog.Error("write compressed web asset", "name", name, "error", err)
	}
}

// acceptsGzip reports whether the client asked for gzip.
//
// A substring search for "gzip" would get one real case backwards: "gzip;q=0"
// is how the header spells "not gzip", so the quality value is read rather
// than assumed. An absent header means no, which is what keeps curl and every
// other plain client on identity bytes.
func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, hasParams := strings.Cut(enc, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if !hasParams {
			return true
		}
		// Anything that is not a readable q-value is treated as acceptance:
		// the client named gzip, and a parameter nobody defined is not a
		// refusal.
		q, err := strconv.ParseFloat(
			strings.TrimPrefix(strings.ToLower(strings.TrimSpace(params)), "q="), 64)
		return err != nil || q > 0
	}
	return false
}
