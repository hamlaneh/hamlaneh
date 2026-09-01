package httpserver_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// filesTestKey is 32 bytes of fixed, non-secret material. It never leaves
// this file and gates nothing.
const filesTestKey = "0123456789abcdef0123456789abcdef"

// The stored bytes each variant is given, so a test can tell which one the
// route opened.
const (
	originalBytes  = "original-bytes"
	thumbnailBytes = "thumb"
)

// fakeAttachments answers metadata for the ids it knows and errors for the
// rest, standing in for the single-attachment read on *storage.Store.
type fakeAttachments map[uuid.UUID]storage.Attachment

func (f fakeAttachments) AttachmentByID(_ context.Context, id uuid.UUID) (storage.Attachment, error) {
	att, ok := f[id]
	if !ok {
		return storage.Attachment{}, storage.ErrAttachmentNotFound
	}
	return att, nil
}

// filesFixture is a wired files origin over a real blobstore, plus the
// signer that addresses it.
type filesFixture struct {
	handler http.Handler
	signer  *filesign.Signer
	id      uuid.UUID
}

// newFilesFixture wires an origin holding one attachment, with both
// variants on disk.
func newFilesFixture(t *testing.T, att storage.Attachment) filesFixture {
	t.Helper()

	signer, err := filesign.New([]byte(filesTestKey))
	if err != nil {
		t.Fatalf("filesign.New: %v", err)
	}
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	att.ID = uuid.New()
	writeBlob(t, blobs, att.ID, blobstore.Original, originalBytes)
	// The thumbnail exists exactly when the row says it does, as on disk:
	// ingest derives none for an image already inside the preview bound.
	if att.HasThumbnail {
		writeBlob(t, blobs, att.ID, blobstore.Thumbnail, thumbnailBytes)
	}

	return filesFixture{
		handler: httpserver.Handler(nil,
			httpserver.WithUploads(blobs, nil),
			httpserver.WithFiles(httpserver.Files{
				Signer:      signer,
				Attachments: fakeAttachments{att.ID: att},
			}),
		),
		signer: signer,
		id:     att.ID,
	}
}

func writeBlob(t *testing.T, blobs *blobstore.Store, id uuid.UUID, v blobstore.Variant, body string) {
	t.Helper()
	f, err := blobs.Create(id, v)
	if err != nil {
		t.Fatalf("create blob: %v", err)
	}
	if _, err = io.WriteString(f, body); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close blob: %v", err)
	}
}

func (f filesFixture) get(t *testing.T, target string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Result()
}

// imageAttachment is a file whose bytes really decoded as the type they
// claim — the only kind that is ever served inline.
func imageAttachment(filename, contentType string) storage.Attachment {
	width, height := 1600, 900
	return storage.Attachment{
		Filename:     filename,
		ContentType:  contentType,
		SizeBytes:    int64(len(originalBytes)),
		Width:        &width,
		Height:       &height,
		HasThumbnail: true,
	}
}

// TestFilesOriginServesImagesInline covers the only case that is ever
// rendered rather than downloaded.
func TestFilesOriginServesImagesInline(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif"} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			f := newFilesFixture(t, imageAttachment("holiday.png", contentType))
			resp := f.get(t, f.signer.FileURL(f.id, blobstore.Original))
			defer closeBody(t, resp)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200", resp.StatusCode)
			}
			assertHeader(t, resp, "Content-Type", contentType)
			assertHeader(t, resp, "Content-Disposition", "inline; filename=holiday.png")
			assertHeader(t, resp, "Cache-Control", "private, max-age=3600, immutable")
			// The message list lives on the app origin; the blanket
			// same-origin CORP would block every image it draws.
			assertHeader(t, resp, "Cross-Origin-Resource-Policy", "cross-origin")
			// A relaxed disposition never relaxes these.
			assertHeader(t, resp, "X-Content-Type-Options", "nosniff")
			assertHeader(t, resp, "Content-Security-Policy", "sandbox; default-src 'none'")

			if got := readBody(t, resp); got != originalBytes {
				t.Errorf("body = %q, want the stored original", got)
			}
		})
	}
}

// TestFilesOriginServesEverythingElseOpaque is the other half of the
// matrix, and the half that carries the security. Every entry is something
// a browser would happily execute if it were allowed to.
func TestFilesOriginServesEverythingElseOpaque(t *testing.T) {
	t.Parallel()

	measured := 64
	tests := []struct {
		name string
		att  storage.Attachment
	}{
		{
			name: "html",
			att:  storage.Attachment{Filename: "payload.html", ContentType: "text/html"},
		},
		{
			// SVG is a scriptable document wearing an image's type. It is
			// absent from the inline set for exactly this reason, and having
			// dimensions does not buy it a way in.
			name: "svg with dimensions",
			att: storage.Attachment{
				Filename: "logo.svg", ContentType: "image/svg+xml",
				Width: &measured, Height: &measured,
			},
		},
		{
			name: "pdf",
			att:  storage.Attachment{Filename: "contract.pdf", ContentType: "application/pdf"},
		},
		{
			// An image type on bytes that never decoded: no dimensions were
			// ever measured, so the label is the uploader's word alone.
			name: "image type with no proven dimensions",
			att:  storage.Attachment{Filename: "not-really.png", ContentType: "image/png"},
		},
		{
			name: "image type with only one dimension",
			att:  storage.Attachment{Filename: "half.png", ContentType: "image/png", Width: &measured},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFilesFixture(t, tt.att)
			resp := f.get(t, f.signer.FileURL(f.id, blobstore.Original))
			defer closeBody(t, resp)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200", resp.StatusCode)
			}
			assertHeader(t, resp, "Content-Type", "application/octet-stream")
			assertHeader(t, resp, "X-Content-Type-Options", "nosniff")
			assertHeader(t, resp, "Content-Security-Policy", "sandbox; default-src 'none'")
			if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
				t.Errorf("Content-Disposition = %q, want an attachment", got)
			}
			if got := resp.Header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
				t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin: an opaque blob is never embedded", got)
			}
		})
	}
}

// TestFilesOriginServesThumbnail pins the variant routing and the one thing
// a thumbnail does not inherit from its original: its encoding. images.go
// re-encodes previews as JPEG or PNG, so labelling a PNG thumbnail
// image/webp would be a lie nosniff stops the browser from correcting.
func TestFilesOriginServesThumbnail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		originalType string
		wantType     string
	}{
		{originalType: "image/jpeg", wantType: "image/jpeg"},
		{originalType: "image/png", wantType: "image/png"},
		{originalType: "image/webp", wantType: "image/png"},
		{originalType: "image/gif", wantType: "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.originalType, func(t *testing.T) {
			t.Parallel()

			f := newFilesFixture(t, imageAttachment("holiday.jpg", tt.originalType))
			resp := f.get(t, f.signer.FileURL(f.id, blobstore.Thumbnail))
			defer closeBody(t, resp)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200", resp.StatusCode)
			}
			assertHeader(t, resp, "Content-Type", tt.wantType)
			if got := readBody(t, resp); got != thumbnailBytes {
				t.Errorf("body = %q, want the thumbnail variant", got)
			}
		})
	}
}

// TestFilesOriginAnswers404 is the refusal surface. Every case is a bare
// 404: a distinguishable answer would say which attachment ids exist.
func TestFilesOriginAnswers404(t *testing.T) {
	t.Parallel()

	base := newFilesFixture(t, imageAttachment("a.png", "image/png"))
	live := base.signer.FileURL(base.id, blobstore.Original)
	liveThumb := base.signer.FileURL(base.id, blobstore.Thumbnail)
	otherKey, err := filesign.New([]byte(strings.Repeat("z", 32)))
	if err != nil {
		t.Fatalf("filesign.New: %v", err)
	}

	tests := []struct {
		name   string
		target string
	}{
		{name: "no signature at all", target: filesign.Path(base.id, blobstore.Original)},
		{name: "tampered signature", target: tamper(live)},
		{
			name:   "expired signature",
			target: base.signer.FileURLAt(base.id, blobstore.Original, time.Now().Add(-2*filesign.TTL)),
		},
		{
			// The thumbnail's query on the original's path: the variant is
			// inside the MAC, so a preview link cannot be traded up to the
			// full file.
			name:   "thumbnail signature replayed for the original",
			target: filesign.Path(base.id, blobstore.Original) + queryOf(liveThumb),
		},
		{
			name:   "original signature replayed for the thumbnail",
			target: filesign.Path(base.id, blobstore.Thumbnail) + queryOf(live),
		},
		{
			// Another attachment's live signature pointed at this id.
			name:   "id swapped",
			target: filesign.Path(uuid.New(), blobstore.Original) + queryOf(live),
		},
		{
			name:   "signed by another instance's key",
			target: otherKey.FileURL(base.id, blobstore.Original),
		},
		{name: "id is not a uuid", target: "/files/not-a-uuid?exp=99999999999&sig=abc"},
		{
			// Correctly signed, but no such attachment: an unknown id has to
			// look exactly like a bad signature.
			name:   "signed id that does not exist",
			target: base.signer.FileURL(uuid.New(), blobstore.Original),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := base.get(t, tt.target)
			defer closeBody(t, resp)

			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status %d, want 404", resp.StatusCode)
			}
			// The refusal still carries the posture; deploy/verify-defaults.sh
			// probes exactly this on an install with nothing uploaded.
			assertHeader(t, resp, "X-Content-Type-Options", "nosniff")
			assertHeader(t, resp, "Content-Security-Policy", "sandbox; default-src 'none'")
			assertHeader(t, resp, "Content-Disposition", "attachment")
			if got := resp.Header.Get("Cache-Control"); strings.Contains(got, "max-age") {
				t.Errorf("Cache-Control = %q: a 404 must not be cached for the life of a signature", got)
			}
			// No reason, ever — not "expired", not "forbidden", and never
			// the id, which would confirm it was read.
			body := strings.ToLower(readBody(t, resp))
			for _, leak := range []string{"expired", "signature", "forbidden", "denied", base.id.String()} {
				if strings.Contains(body, leak) {
					t.Errorf("404 body %q leaks %q", body, leak)
				}
			}
		})
	}
}

// TestFilesOriginMissingThumbnailIs404 covers the image small enough that
// ingest derived no preview: the row says so, and the bytes are not there.
func TestFilesOriginMissingThumbnailIs404(t *testing.T) {
	t.Parallel()

	att := imageAttachment("small.png", "image/png")
	att.HasThumbnail = false
	f := newFilesFixture(t, att)

	// Correctly signed, and the row is real — only the bytes are absent.
	resp := f.get(t, f.signer.FileURL(f.id, blobstore.Thumbnail))
	defer closeBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

// TestFilesOriginWithoutPipelineStillCarriesHeaders pins the state the
// deploy check runs against: a stack whose upload pipeline is not wired
// must still answer these routes itself, with the headers, rather than
// falling through to a bare mux 404 that has none.
func TestFilesOriginWithoutPipelineStillCarriesHeaders(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpserver.Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/"+uuid.Nil.String(), nil))
	resp := rec.Result()
	defer closeBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
	assertHeader(t, resp, "X-Content-Type-Options", "nosniff")
	assertHeader(t, resp, "Content-Security-Policy", "sandbox; default-src 'none'")
	assertHeader(t, resp, "Content-Disposition", "attachment")
	// deploy/verify-defaults.sh probes this on an install with nothing
	// uploaded yet, so it has to be on the 404 too.
	assertHeader(t, resp, "Access-Control-Allow-Origin", "*")
}

// TestFilesOriginAllowsCrossOriginReads is ADR 013's one serving change,
// and the assertion below is really two.
//
// The first is that the header is there at all: an encrypted attachment is
// decrypted by the app's own JavaScript, which has to fetch() the ciphertext
// off this origin to do it, and without a CORS header the app can navigate
// to a blob but never read one.
//
// The second is what keeps `*` honest: Access-Control-Allow-Credentials is
// absent. This origin is deliberately cookie-less, so there is no ambient
// authority for CORS to launder — the signed URL is the whole credential,
// and a script that has one could already fetch what it names. Setting the
// credentials header would be the change that turned that reasoning false,
// so it is asserted absent rather than merely left unwritten.
func TestFilesOriginAllowsCrossOriginReads(t *testing.T) {
	t.Parallel()

	f := newFilesFixture(t, imageAttachment("holiday.png", "image/png"))
	targets := map[string]string{
		"an image served inline":    f.signer.FileURL(f.id, blobstore.Original),
		"a thumbnail":               f.signer.FileURL(f.id, blobstore.Thumbnail),
		"an unsigned request's 404": "/files/" + uuid.New().String(),
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp := f.get(t, target)
			defer closeBody(t, resp)

			assertHeader(t, resp, "Access-Control-Allow-Origin", "*")
			if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("Access-Control-Allow-Credentials = %q; this origin has no credentialed state to share", got)
			}
		})
	}
}

// TestFilesOriginOpaqueBlobIsReadableButNotEmbeddable is the pair of headers
// an encrypted attachment rides out on, asserted together because each one
// is wrong without the other.
//
// The blob may be READ cross-origin — that is the fetch() the decryption
// needs — and it may still never be EMBEDDED: same-origin CORP keeps opaque
// bytes out of any page as a subresource, which is what stops a stored HTML
// or SVG payload from ever being loaded into a document.
func TestFilesOriginOpaqueBlobIsReadableButNotEmbeddable(t *testing.T) {
	t.Parallel()

	// The shape an encrypted upload produces: the placeholder name, the
	// opaque type, nothing measured.
	f := newFilesFixture(t, storage.Attachment{
		Filename: "encrypted", ContentType: "application/octet-stream",
	})
	resp := f.get(t, f.signer.FileURL(f.id, blobstore.Original))
	defer closeBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	assertHeader(t, resp, "Access-Control-Allow-Origin", "*")
	assertHeader(t, resp, "Cross-Origin-Resource-Policy", "same-origin")
	assertHeader(t, resp, "Content-Type", "application/octet-stream")
	assertHeader(t, resp, "X-Content-Type-Options", "nosniff")
	assertHeader(t, resp, "Content-Security-Policy", "sandbox; default-src 'none'")
}

// TestFilesOriginRefusesWrites keeps the origin read-only: it is reachable
// with no session at all, so anything but a read would be unauthenticated.
func TestFilesOriginRefusesWrites(t *testing.T) {
	t.Parallel()

	f := newFilesFixture(t, storage.Attachment{Filename: "c.pdf", ContentType: "application/pdf"})
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, f.signer.FileURL(f.id, blobstore.Original), nil))
	resp := rec.Result()
	defer closeBody(t, resp)

	if resp.StatusCode == http.StatusOK {
		t.Error("DELETE on the files origin was served")
	}
}

// TestFilesOriginNamesNonASCIIDownloads keeps a Persian filename usable:
// the header has to encode it, not drop it.
func TestFilesOriginNamesNonASCIIDownloads(t *testing.T) {
	t.Parallel()

	f := newFilesFixture(t, storage.Attachment{Filename: "گزارش.pdf", ContentType: "application/pdf"})
	resp := f.get(t, f.signer.FileURL(f.id, blobstore.Original))
	defer closeBody(t, resp)

	got := resp.Header.Get("Content-Disposition")
	if !strings.Contains(got, "filename*=utf-8''") {
		t.Errorf("Content-Disposition = %q, want an RFC 2231 encoded filename", got)
	}
}

func assertHeader(t *testing.T, resp *http.Response, name, want string) {
	t.Helper()
	if got := resp.Header.Get(name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

// queryOf returns the "?..." part of a signed URL.
func queryOf(signed string) string {
	if i := strings.IndexByte(signed, '?'); i >= 0 {
		return signed[i:]
	}
	return ""
}

// tamper flips one character of a signed URL's signature.
func tamper(signed string) string {
	i := strings.Index(signed, "sig=")
	if i < 0 {
		return signed
	}
	i += len("sig=")
	replacement := byte('a')
	if signed[i] == 'a' {
		replacement = 'b'
	}
	return signed[:i] + string(replacement) + signed[i+1:]
}
