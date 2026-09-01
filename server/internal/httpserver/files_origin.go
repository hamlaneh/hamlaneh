package httpserver

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The files origin — GET /files/{id} and GET /files/{id}/thumb — is the one
// surface in this server that answers with no session principal at all.
// docs/adr/003-file-serving-and-egress.md fixes why: files.<domain> is a
// separate, deliberately cookie-less origin, so uploaded content can never
// script against the app and a request for it carries no ambient authority.
// Authorization happened when the URL was minted for a reader entitled to
// the channel. The signature IS the credential.
//
// Consequences that must not be "tidied" away:
//
//   - These two routes are outside docs/api/openapi.yaml on purpose. The
//     authz-matrix completeness gate demands session-principal rows for
//     every contract endpoint, and there is no principal here to write a
//     row about. Their tests live in files_origin_test.go and in the e2e
//     suite instead.
//   - They are registered on the base mux, NOT through the generated
//     contract router, so securityMiddleware (CSRF, sessions, the
//     must-change gate) never runs on them. That is the design, not an
//     oversight: there is no cookie to check.
//   - Every refusal is 404. Never 403, never a reason. A distinguishable
//     answer would turn this origin into an oracle for which attachment ids
//     exist, which is the one fact a stranger holding no signature could
//     otherwise learn.
//   - The response headers below are set BEFORE anything can fail, so the
//     404s carry them too. That is what deploy/verify-defaults.sh probes on
//     an install with nothing uploaded yet, and it is also the honest
//     posture: an opaque download is the default, and inline rendering is
//     the exception that has to earn itself.

// inlineImageTypes are the only content types ever served inline, and only
// when the stored dimensions prove the bytes really decoded as that type at
// ingest. Everything else in the world — SVG, HTML, XML, PDF — is an opaque
// blob. SVG is absent deliberately: it is a document that can carry script,
// not a picture, and inlining one would hand an uploader this origin.
//
// It is the same four types images.go will ingest (ingestibleImageTypes),
// and deliberately a separate list: that one says what may be decoded and
// stripped, this one says what a browser may be told to render. They agree
// today; if they ever have to diverge, this is the one that must not widen.
var inlineImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

const (
	// blobCacheControl is honest about what a signed URL is worth: private
	// to the reader who was handed it, immutable because the bytes behind an
	// attachment id never change, and an hour because that is exactly when
	// the signature stops working (filesign.TTL).
	blobCacheControl = "private, max-age=3600, immutable"

	// blobCSP applies to every file response, on every serving mode. The
	// separate origin is defense in depth; THIS is the defense. A bare-IP or
	// home-mode install has no files.<domain> to serve from and gets these
	// identical headers from the app origin.
	blobCSP = "sandbox; default-src 'none'"

	// opaqueContentType is what anything that is not a proven image is
	// labelled. Handing back a stored "text/html" label, even with
	// attachment and nosniff, is a bet on browser behaviour; refusing to
	// name the type at all is not a bet.
	opaqueContentType = "application/octet-stream"
)

// Files wires the files origin. Every member is required before the routes
// serve any bytes: an install whose upload pipeline is not configured
// leaves them answering 404, which is the truth about that install.
type Files struct {
	// Signer is both halves of the credential: it mints the URLs an
	// Attachment carries — its FileURL is what the minting side calls —
	// and it is what verifies the ones presented here.
	Signer *filesign.Signer
	// Attachments reads the metadata that decides the response headers.
	Attachments attachmentReader
	// Previews is the fallback lookup for link-preview images, which have no
	// attachments row on purpose (ADR 003).
	Previews previewImageReader
}

// The bytes themselves come from s.blobs, the one blob store the upload half
// already wires (WithUploads). Reading and writing the same files through
// two separately configured handles is how one of them ends up pointed
// somewhere else.

// URLSigner mints the URLs an Attachment or a link-preview card carries.
// It is declared here, next to the routes that verify what it produces, and
// it is an interface for the same reason Realtime is: serialization code
// calls it from all over this package and must not depend on how a
// signature is computed.
//
// The URLs are the credential (ADR 003). The files origin is cookie-less, so
// authorization happens when a URL is minted rather than when it is served —
// which is why every serialization calls this again instead of storing what
// it returned. An implementation signs and dates each URL it hands back; a
// caller must never cache one.
type URLSigner interface {
	// FileURL returns a fresh, expiring URL for one variant of one blob.
	FileURL(id uuid.UUID, variant blobstore.Variant) string
}

// The signer this package wires is the one that verifies, too.
var _ URLSigner = (*filesign.Signer)(nil)

// attachmentReader is the metadata half of what the files origin needs. It
// is deliberately narrow — serving must not be able to reach anything else
// in storage — and deliberately says nothing about channels: entitlement was
// settled when the URL was signed, and re-deciding it here with no principal
// to decide it for is not possible.
//
// *storage.Store satisfies this once it carries the single-attachment read.
type attachmentReader interface {
	AttachmentByID(ctx context.Context, id uuid.UUID) (storage.Attachment, error)
}

// previewImageReader answers whether a blob id belongs to a link-preview
// card. *storage.Store implements it.
type previewImageReader interface {
	LinkPreviewImageExists(ctx context.Context, blobID uuid.UUID) (bool, error)
}

// routeFilesOrigin registers the two file routes on mux.
//
// The patterns come from filesign.RoutePattern rather than being written out
// here, because filesign.Path is what every minted URL is built from: a path
// spelled independently at the two ends is one edit away from serving 404 to
// URLs this instance signed itself.
//
// They are registered unconditionally, even on a server with no Files
// wired. A missing route would fall through to the bare 404 of the default
// mux, which carries none of the headers above — and those headers are the
// invariant the deploy check verifies on exactly such an install.
func routeFilesOrigin(mux *http.ServeMux, s *apiServer) {
	mux.HandleFunc("GET "+filesign.RoutePattern(blobstore.Original), func(w http.ResponseWriter, r *http.Request) {
		s.serveAttachment(w, r, blobstore.Original)
	})
	mux.HandleFunc("GET "+filesign.RoutePattern(blobstore.Thumbnail), func(w http.ResponseWriter, r *http.Request) {
		s.serveAttachment(w, r, blobstore.Thumbnail)
	})
}

// serveAttachment verifies the signature on the request, then streams the
// bytes it authorizes. Anything that does not line up is a 404.
func (s *apiServer) serveAttachment(w http.ResponseWriter, r *http.Request, variant blobstore.Variant) {
	setOpaqueBlobHeaders(w)

	f := s.files
	if f.Signer == nil || f.Attachments == nil || s.blobs == nil {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !f.Signer.Valid(id, variant, r.URL.Query()) {
		http.NotFound(w, r)
		return
	}
	att, err := f.Attachments.AttachmentByID(r.Context(), id)
	if err != nil {
		// Not an attachment. One more lookup before the 404: a link-preview
		// image, which deliberately has no attachments row (ADR 003). Only
		// the original variant exists for those — the derivative IS the
		// bounded image; there is no second derivative of it.
		if variant == blobstore.Original && f.Previews != nil {
			if ok, previewErr := f.Previews.LinkPreviewImageExists(r.Context(), id); previewErr == nil && ok {
				servePreviewImage(w, r, s, id)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	// A missing blob — including the thumbnail of an image small enough that
	// ingest never derived one — is the same 404 as everything else.
	blob, err := s.blobs.Open(id, variant)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer closeBlob(blob, r)

	h := w.Header()
	if contentType, ok := inlineType(att, variant); ok {
		h.Set("Content-Type", contentType)
		h.Set("Content-Disposition", disposition("inline", att.Filename))
		// The app document lives on a different origin, and embedding these
		// pictures in it is the entire purpose of this one. The blanket
		// Cross-Origin-Resource-Policy: same-origin from securityHeaders
		// would block every <img> the message list draws. Relaxed only for
		// the proven image types: an opaque blob keeps same-origin, so it
		// can be downloaded by navigation but never pulled into a page.
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	} else {
		h.Set("Content-Disposition", disposition("attachment", att.Filename))
	}
	// Set only now that the bytes exist: caching a 404 for the life of a
	// signature would outlive whatever caused it.
	h.Set("Cache-Control", blobCacheControl)

	// Empty name and zero modtime: the Content-Type above is already
	// decided, and ServeContent must never sniff one or guess it from a
	// client-supplied filename. It is here for Content-Length and Range.
	http.ServeContent(w, r, "", time.Time{}, blob)
}

// inlineType reports the Content-Type to render this variant inline with,
// and whether it may be rendered inline at all.
//
// Inline is earned by two facts together: the stored type is one of the four
// images ever served inline (inlineImageTypes above), and the ingest
// actually measured its pixels. An "image/png" label on bytes nobody could
// decode has no dimensions, and is exactly the smuggling attempt this
// refuses to render.
func inlineType(att storage.Attachment, variant blobstore.Variant) (string, bool) {
	if !inlineImageTypes[att.ContentType] || att.Width == nil || att.Height == nil {
		return "", false
	}
	if variant != blobstore.Thumbnail {
		return att.ContentType, true
	}
	// A thumbnail is a NEW encoding, not a slice of the original: images.go
	// re-encodes a photograph's preview as JPEG and everything else as PNG,
	// so that alpha survives. Labelling a PNG thumbnail with the original's
	// image/webp would be a lie the browser believes — nosniff means it will
	// not second-guess us, and the reader sees a broken image.
	if att.ContentType == "image/jpeg" {
		return "image/jpeg", true
	}
	return "image/png", true
}

// setOpaqueBlobHeaders writes the posture every file response starts from:
// an unnamed download of unnamed bytes, in a sandbox, with sniffing off,
// readable by a script that holds the URL. Only a proven image relaxes any
// of it, and only after its metadata has been read.
func setOpaqueBlobHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", opaqueContentType)
	h.Set("Content-Disposition", "attachment")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", blobCSP)
	// ADR 013. An encrypted attachment is decrypted by the app's own
	// JavaScript, which must fetch() these bytes to do it — and without a
	// CORS header the app origin can navigate to a blob but never read one.
	//
	// `*` gives away nothing, for the same reason this origin exists: it is
	// deliberately cookie-less, registered outside securityMiddleware, so no
	// request here carries ambient authority for CORS to launder. The signed
	// URL is the entire credential, and a script holding one could already
	// fetch what it names; what changes is only that it may now read the
	// answer. Two things must therefore stay true, or the reasoning stops
	// holding:
	//
	//   - Access-Control-Allow-Credentials is never set. `*` and credentials
	//     are incompatible by specification anyway, but the header is also
	//     the thing that would make a cookie meaningful here.
	//   - No response on this origin ever varies by anything but the signed
	//     URL. The moment one did, `*` would be caching somebody's answer
	//     for everybody.
	//
	// A configured app origin instead of `*` was refused deliberately: there
	// is no credentialed state to scope it to, and it would be one more
	// deploy-time string to get wrong in bare-IP and home-mode installs.
	h.Set("Access-Control-Allow-Origin", "*")
}

// disposition formats a Content-Disposition value, encoding a non-ASCII
// filename per RFC 2231 rather than mangling it — plenty of the files this
// instance carries will be named in Persian. A name that cannot be encoded
// at all degrades to the bare disposition: the download is then named after
// the URL, which is a worse name but never a broken header.
func disposition(kind, filename string) string {
	if filename == "" {
		return kind
	}
	v := mime.FormatMediaType(kind, map[string]string{"filename": filename})
	if v == "" {
		return kind
	}
	return v
}

// closeBlob releases the open blob, logging a failure. Once the body has
// been written there is nothing else left to do about one.
func closeBlob(c io.Closer, r *http.Request) {
	if err := c.Close(); err != nil {
		slog.Error("close attachment blob", "path", r.URL.Path, "error", err)
	}
}

// servePreviewImage streams a link-preview derivative inline.
//
// It is proven by construction rather than by sniffing a stranger's bytes:
// the blob was decoded, bounded and re-encoded by our own ingest, so it is
// exactly a PNG or a JPEG our encoder produced. DetectContentType over the
// first bytes recovers which of the two — deciding it from our own output,
// not trusting a remote site's label.
func servePreviewImage(w http.ResponseWriter, r *http.Request, s *apiServer, id uuid.UUID) {
	blob, err := s.blobs.Open(id, blobstore.Original)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer closeBlob(blob, r)

	head := make([]byte, 512)
	n, _ := io.ReadFull(blob, head)
	contentType := http.DetectContentType(head[:n])
	if contentType != "image/png" && contentType != "image/jpeg" {
		// Not something our encoder writes. Refuse rather than guess.
		http.NotFound(w, r)
		return
	}
	if _, err := blob.Seek(0, io.SeekStart); err != nil {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", `inline; filename="preview"`)
	// Same reasoning as the attachment path: embedding these in the app
	// document is this origin's entire purpose.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Cache-Control", blobCacheControl)
	http.ServeContent(w, r, "", time.Time{}, blob)
}
