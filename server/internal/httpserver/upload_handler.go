package httpserver

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// maxUploadBytes is the per-file upload cap (ADR 003). It is published as
// max_file_size_bytes on GET /api/v1/instance from this same constant, so
// what a client is told and what the server enforces cannot drift.
const maxUploadBytes int64 = 25 << 20

// maxFilenameLen matches the attachments table's own bound. The filename is
// display data: it is stored, shown on the card, and never becomes part of a
// path (internal/blobstore).
const maxFilenameLen = 255

// uploadFormField is the multipart field the contract names.
const uploadFormField = "file"

// defaultUploadType is what an upload with no declared content type is
// labelled. Unlabelled means opaque, which is the safe end of the ADR's rule:
// only a part that declares an image is ever considered for inline serving.
const defaultUploadType = "application/octet-stream"

// UploadFile stores one file into a channel.
//
// The order is the security design. Membership and the authz decision come
// first, before a single byte of the body is read: an upload from somebody
// who is not in the channel must cost the server the request headers, not
// twenty-five megabytes of disk and CPU. Then the cap goes on the body with
// http.MaxBytesReader, so an oversized upload is refused by the reader rather
// than measured after the fact — the limit costs no memory because nothing
// ever holds the whole request.
//
// The declared content type is kept as the card's label but decides only what
// must be proved. A part declaring image/* has to sniff as exactly that type
// (415 otherwise), and only then is it decoded, stripped and thumbnailed.
// Everything else is stored byte for byte as an opaque blob and served as a
// download, which is what makes an uploaded SVG or HTML page a file the
// reader saves rather than a page their browser runs.
func (s *apiServer) UploadFile(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.FileUpload, sc.resource()) {
		sc.deny(w, r)
		return
	}
	if s.blobs == nil {
		internalError(w, r, errors.New("upload reached a server with no blob store"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	part, ok := filePart(w, r)
	if !ok {
		return
	}
	defer func() {
		if err := part.Close(); err != nil {
			slog.Debug("close upload part", "error", err)
		}
	}()

	filename, ok := uploadFilename(w, r, part)
	if !ok {
		return
	}
	declared := declaredType(part)

	// One id for the row and for every blob under it. It is generated here,
	// before anything is written, because the bytes have to land at a path
	// only the server chose — never one derived from what the client sent.
	id := uuid.New()
	na := storage.NewAttachment{
		ID:          id,
		ChannelID:   channelID,
		UploaderID:  sc.prin.user.ID,
		Filename:    filename,
		ContentType: declared,
	}

	if strings.HasPrefix(declared, "image/") {
		ok = s.storeImage(w, r, id, part, &na)
	} else {
		ok = s.storeBlob(w, r, id, part, &na)
	}
	if !ok {
		return
	}

	att, err := sc.store.CreateAttachment(r.Context(), na)
	if err != nil {
		s.deleteBlobs(id)
		if errors.Is(err, storage.ErrNotFound) {
			// The channel went away between the authorization check and the
			// insert; the caller learns exactly what a stranger would.
			writeChannelNotFound(w, r)
			return
		}
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusCreated, api.AttachmentOf(att, s.fileSigner))
}

// storeBlob streams an opaque upload straight to disk, filling in the size
// it turned out to be. Nothing buffers the file: the bytes go from the
// socket to the blob, and the cap is the reader's to enforce.
func (s *apiServer) storeBlob(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, part io.Reader, na *storage.NewAttachment,
) bool {
	f, err := s.blobs.Create(id, blobstore.Original)
	if err != nil {
		internalError(w, r, err)
		return false
	}

	size, copyErr := io.Copy(f, part)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		s.deleteBlobs(id)
		return uploadReadFailed(w, r, err)
	}
	na.SizeBytes = size
	return true
}

// storeImage reads an image upload, proves the bytes are the type the label
// claimed, strips its metadata and writes the stripped original plus any
// thumbnail.
//
// This is the one path that holds a whole file in memory, and it has to:
// dimensions come from the header, but stripping edits segments across the
// file and a thumbnail decodes all of it. The bound is the same
// MaxBytesReader the streaming path uses — an image can never be larger than
// the cap — and the pixel limit is checked from the header before any decode
// allocates.
func (s *apiServer) storeImage(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, part io.Reader, na *storage.NewAttachment,
) bool {
	data, err := io.ReadAll(part)
	if err != nil {
		return uploadReadFailed(w, r, err)
	}

	img, err := ingestImage(na.ContentType, data)
	switch {
	case errors.Is(err, errContentTypeMismatch):
		writeError(w, r, http.StatusUnsupportedMediaType, codeContentTypeMismatch,
			"the file's bytes are not the image type it was uploaded as")
		return false
	case errors.Is(err, errImageTooLarge):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"image is too large to process")
		return false
	case err != nil:
		internalError(w, r, err)
		return false
	}

	if !s.writeBlob(w, r, id, blobstore.Original, img.original) {
		return false
	}
	if img.thumbnail != nil && !s.writeBlob(w, r, id, blobstore.Thumbnail, img.thumbnail) {
		return false
	}

	na.SizeBytes = int64(len(img.original))
	na.Width, na.Height = &img.width, &img.height
	na.HasThumbnail = img.thumbnail != nil
	return true
}

// writeBlob stores one derived blob, cleaning up after itself on failure.
func (s *apiServer) writeBlob(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, variant blobstore.Variant, data []byte,
) bool {
	f, err := s.blobs.Create(id, variant)
	if err != nil {
		s.deleteBlobs(id)
		internalError(w, r, err)
		return false
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		s.deleteBlobs(id)
		internalError(w, r, err)
		return false
	}
	return true
}

// deleteBlobs drops whatever an abandoned upload managed to write. A failure
// here is logged rather than returned: the request already has its answer,
// and what is left behind is disk, which the orphan sweep's counterpart on
// the filesystem is the place to reason about — not this handler.
func (s *apiServer) deleteBlobs(id uuid.UUID) {
	if err := s.blobs.Delete(id); err != nil {
		slog.Error("delete blobs of a failed upload", "attachment_id", id, "error", err)
	}
}

// uploadReadFailed answers a body that could not be read to the end: 413 for
// the cap, 400 for a truncated or malformed multipart stream. It always
// reports false, so callers can return it directly.
func uploadReadFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeFileTooLarge(w, r)
		return false
	}
	writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "malformed upload body")
	return false
}

// writeFileTooLarge is the single source of the 413.
func writeFileTooLarge(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusRequestEntityTooLarge, codeFileTooLarge,
		"the file is larger than this instance accepts")
}

// filePart advances the multipart stream to the part the contract names and
// returns it. Parts before it are skipped rather than refused — a form
// library may send its own — and everything is streamed: ParseMultipartForm
// would spool the whole request to a temporary file first, which is exactly
// the memory and disk the cap exists to avoid spending.
func filePart(w http.ResponseWriter, r *http.Request) (*multipart.Part, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"this endpoint takes a multipart/form-data body")
		return nil, false
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"the request has no "+uploadFormField+" part")
			return nil, false
		}
		if err != nil {
			uploadReadFailed(w, r, err)
			return nil, false
		}
		if part.FormName() == uploadFormField {
			return part, true
		}
		if closeErr := part.Close(); closeErr != nil {
			slog.Debug("close skipped upload part", "error", closeErr)
		}
	}
}

// uploadFilename is the client's name for the file, reduced to something
// storable and answering 400 when it is not.
//
// The name is display data and nothing else: it is what the card shows and
// what a download is offered as. It never reaches the filesystem — the blob's
// path comes from the server's own UUID — so this is validation, not
// defusing. What it must guarantee is that the string can be stored, handed
// back and rendered as one line.
func uploadFilename(w http.ResponseWriter, r *http.Request, part *multipart.Part) (string, bool) {
	// Some clients send a whole path. Keep the last element under either
	// separator, so the card shows "photo.jpg" rather than a directory tree
	// off the uploader's machine.
	name := part.FileName()
	name = strings.TrimSpace(name[strings.LastIndexAny(name, `/\`)+1:])

	if name == "" || name == "." || name == ".." {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"the file part must carry a filename")
		return "", false
	}
	// Stricter than storableText: a filename is one line, so tabs and
	// newlines are refused here even though a message body may hold them.
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(c rune) bool {
		return c < 0x20 || c == 0x7f
	}) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"filename must be text that can be stored and returned unchanged")
		return "", false
	}
	if utf8.RuneCountInString(name) > maxFilenameLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"filename is too long")
		return "", false
	}
	return name, true
}

// WithUploads wires the upload pipeline: the blob store the bytes are
// written to, and the signer that mints the URLs an Attachment is
// serialized with. Omitting it leaves an install with no uploads —
// UploadFile answers 500 — which is what that install is.
//
// A nil signer keeps the unsigned placeholder (search_handler.go), so a
// server can be built with storage alone in a test.
func WithUploads(blobs *blobstore.Store, signer fileURLSigner) Option {
	return func(s *apiServer) {
		s.blobs = blobs
		if signer != nil {
			s.fileSigner = signer
		}
	}
}

// declaredType is the part's content type reduced to type/subtype, or the
// opaque default when it is absent or unparseable. Parameters (charset and
// friends) are dropped: the label is a card caption and a sniff target, and
// keeping the caller's parameters would only widen what a serving header
// might echo.
func declaredType(part *multipart.Part) string {
	declared := part.Header.Get("Content-Type")
	if declared == "" {
		return defaultUploadType
	}
	mediaType, _, err := mime.ParseMediaType(declared)
	if err != nil || mediaType == "" {
		return defaultUploadType
	}
	return strings.ToLower(mediaType)
}
