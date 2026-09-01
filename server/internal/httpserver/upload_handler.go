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

// uploadFormField is the multipart field the contract names, and
// thumbFormField is the optional one that may follow it on an e2ee channel.
const (
	uploadFormField = "file"
	thumbFormField  = "thumb"
)

// defaultUploadType is what an upload with no declared content type is
// labelled. Unlabelled means opaque, which is the safe end of the ADR's rule:
// only a part that declares an image is ever considered for inline serving.
const defaultUploadType = "application/octet-stream"

// encryptedFilename is the literal name an e2ee upload must declare and the
// only name one is ever stored under (ADR 013). It is a constant rather than
// an empty string because migration 0007 requires a filename of at least one
// character, and it is compared byte for byte — none of uploadFilename's
// path-stripping and trimming applies, so nothing can be reduced into it.
const encryptedFilename = "encrypted"

// maxThumbBytes bounds the client-derived thumbnail an e2ee upload may carry
// beside its ciphertext. It widens the request body by exactly this much and
// nothing else: maxUploadBytes is unchanged and still bounds the file part
// itself, so the megabyte allowed here can never be borrowed by the file.
const maxThumbBytes int64 = 1 << 20

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
//
// On an e2ee channel none of that paragraph can apply and none of it runs
// (ADR 013). The bytes are ciphertext, so there is nothing to sniff, nothing
// to strip and nothing to thumbnail; the metadata must therefore arrive as
// the placeholders storeEncrypted insists on, and the row is filled from
// this package's own constants rather than from anything the client sent.
// The two regimes are separate branches rather than a shared path with
// conditionals, because the property that matters is that the image pipeline
// is unreachable from the encrypted one, not that it is skipped.
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

	// The channel's mode is fixed at creation and cannot change under this
	// request, so the regime chosen here is the regime the row is stored
	// under. An e2ee body may carry a second part, and only its own cap
	// widens: maxUploadBytes still bounds the file part below.
	e2ee := sc.channel.E2EE
	bodyCap := maxUploadBytes
	if e2ee {
		bodyCap += maxThumbBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyCap)

	mr, part, ok := filePart(w, r)
	if !ok {
		return
	}

	// One id for the row and for every blob under it. It is generated here,
	// before anything is written, because the bytes have to land at a path
	// only the server chose — never one derived from what the client sent.
	id := uuid.New()
	na := storage.NewAttachment{ID: id, ChannelID: channelID, UploaderID: sc.prin.user.ID}

	if e2ee {
		ok = s.storeEncrypted(w, r, id, part, &na)
	} else {
		ok = s.storePlaintext(w, r, id, part, &na)
	}
	if !ok {
		return
	}
	if !s.takeThumbPart(w, r, id, mr, e2ee, &na) {
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

// storePlaintext is the ADR 003 pipeline: the client's own name and label,
// the sniff-strip-thumbnail path for a declared image, and byte-for-byte
// storage for everything else.
func (s *apiServer) storePlaintext(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, part *multipart.Part, na *storage.NewAttachment,
) bool {
	filename, ok := uploadFilename(w, r, part)
	if !ok {
		return false
	}
	na.Filename = filename
	na.ContentType = declaredType(part)

	if strings.HasPrefix(na.ContentType, "image/") {
		return s.storeImage(w, r, id, part, na)
	}
	return s.storeOriginal(w, r, id, part, na)
}

// storeEncrypted is the e2ee pipeline (ADR 013): prove the part declares
// nothing about the plaintext, then store the ciphertext opaquely.
//
// The row's name and type are this package's constants, not the part's. That
// is what makes the placeholder a property of the code rather than of the
// check above it: even a bug that let a real filename past the refusal could
// not put one in the column.
func (s *apiServer) storeEncrypted(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, part *multipart.Part, na *storage.NewAttachment,
) bool {
	if declaredFilename(part) != encryptedFilename || !opaquelyDeclared(part) {
		writeError(w, r, http.StatusBadRequest, codeE2EEMetadataInClear,
			"on an end-to-end encrypted channel the file part must be named "+
				encryptedFilename+" and declare no content type but "+defaultUploadType)
		return false
	}
	na.Filename = encryptedFilename
	na.ContentType = defaultUploadType
	return s.storeOriginal(w, r, id, part, na)
}

// declaredFilename is the part's filename parameter exactly as the client
// wrote it, or "" when there is none or the header will not parse.
//
// It is deliberately not multipart.Part.FileName, which applies RFC 7578's
// basename reduction. That reduction is right for a plaintext upload — it is
// what keeps a directory tree off the card — but wrong here, where the whole
// claim is that the client said nothing: "/home/amir/q3-layoffs/encrypted"
// reduces to the placeholder and would pass, having named a directory this
// server has no business being told about.
func declaredFilename(part *multipart.Part) string {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	return params["filename"]
}

// opaquelyDeclared reports that the part named a content type of absent or
// application/octet-stream, and nothing else.
//
// An unparseable header is a refusal rather than the opaque default
// declaredType falls back to: on this path the question is not "what shall we
// label this" but "did the client say something about the plaintext", and a
// header nobody can parse is not an answer of no.
func opaquelyDeclared(part *multipart.Part) bool {
	declared := part.Header.Get("Content-Type")
	if declared == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(declared)
	return err == nil && mediaType == defaultUploadType
}

// takeThumbPart handles whatever follows the file part: on an e2ee channel a
// client-encrypted thumbnail, anywhere else a refusal.
//
// A plaintext channel derives its own preview from bytes it can read, so a
// supplied one is refused rather than trusted — the same both-directions
// discipline e2eeBody applies to a message. The scan runs on both regimes so
// that refusal is real; parts that are not the thumb are skipped, as they are
// before the file part.
func (s *apiServer) takeThumbPart(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, mr *multipart.Reader, e2ee bool, na *storage.NewAttachment,
) bool {
	part, ok := thumbPart(w, r, mr)
	if !ok {
		s.deleteBlobs(id)
		return false
	}
	if part == nil {
		return true
	}
	if !e2ee {
		s.deleteBlobs(id)
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"a "+thumbFormField+" part is accepted only on an end-to-end encrypted channel")
		return false
	}

	size, ok := s.storeBlob(w, r, id, blobstore.Thumbnail, part, maxThumbBytes)
	if !ok {
		return false
	}
	if size > maxThumbBytes {
		s.deleteBlobs(id)
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"the "+thumbFormField+" part is larger than this instance accepts")
		return false
	}
	na.HasThumbnail = true
	return true
}

// storeOriginal streams the uploaded bytes into the original blob and
// records the size they turned out to be, refusing anything past the
// published cap.
func (s *apiServer) storeOriginal(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, part io.Reader, na *storage.NewAttachment,
) bool {
	size, ok := s.storeBlob(w, r, id, blobstore.Original, part, maxUploadBytes)
	if !ok {
		return false
	}
	if size > maxUploadBytes {
		s.deleteBlobs(id)
		writeFileTooLarge(w, r)
		return false
	}
	na.SizeBytes = size
	return true
}

// storeBlob streams one part straight to disk and reports how many bytes it
// held. Nothing buffers the file: the bytes go from the socket to the blob.
//
// It reads one byte past limit and hands the count back rather than deciding
// anything, because the two callers refuse an over-limit part differently: a
// file past the published cap is the contract's 413, an oversized thumb is a
// 400. The limit is per PART, which is what the body cap cannot express once
// a body may carry two of them.
func (s *apiServer) storeBlob(w http.ResponseWriter, r *http.Request,
	id uuid.UUID, variant blobstore.Variant, part io.Reader, limit int64,
) (int64, bool) {
	f, err := s.blobs.Create(id, variant)
	if err != nil {
		s.deleteBlobs(id)
		internalError(w, r, err)
		return 0, false
	}

	size, copyErr := io.Copy(f, io.LimitReader(part, limit+1))
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		s.deleteBlobs(id)
		return 0, uploadReadFailed(w, r, err)
	}
	return size, true
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
// returns it, along with the reader it came from so the caller can look for
// what follows. Parts before it are skipped rather than refused — a form
// library may send its own — and everything is streamed: ParseMultipartForm
// would spool the whole request to a temporary file first, which is exactly
// the memory and disk the cap exists to avoid spending.
func filePart(w http.ResponseWriter, r *http.Request) (*multipart.Reader, *multipart.Part, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"this endpoint takes a multipart/form-data body")
		return nil, nil, false
	}

	for {
		part, err := nextPart(w, r, mr)
		if part == nil {
			if err == nil {
				writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
					"the request has no "+uploadFormField+" part")
			}
			return nil, nil, false
		}
		if part.FormName() == uploadFormField {
			return mr, part, true
		}
		closePart(part)
	}
}

// thumbPart advances past the file part to an optional part named thumb.
// A nil part with ok reports that the body simply ended, which is the common
// case: a thumb is optional even on an e2ee channel.
func thumbPart(w http.ResponseWriter, r *http.Request, mr *multipart.Reader) (*multipart.Part, bool) {
	for {
		part, err := nextPart(w, r, mr)
		if part == nil {
			return nil, err == nil
		}
		if part.FormName() == thumbFormField {
			return part, true
		}
		closePart(part)
	}
}

// nextPart reads one part, answering a malformed or oversized body itself. A
// nil part with a nil error is a body that ended; a nil part with an error
// means the caller's answer is already written.
func nextPart(w http.ResponseWriter, r *http.Request, mr *multipart.Reader) (*multipart.Part, error) {
	part, err := mr.NextPart()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		uploadReadFailed(w, r, err)
		return nil, err
	}
	return part, nil
}

// closePart drops a part nothing wanted, discarding whatever is left of it.
func closePart(part *multipart.Part) {
	if err := part.Close(); err != nil {
		slog.Debug("close skipped upload part", "error", err)
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
