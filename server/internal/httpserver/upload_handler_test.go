package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// uploadPath is the contract's upload target for the fixture channel.
func uploadPath() string { return channelPath("/files") }

// multipartBody builds a multipart/form-data body and returns it with the
// Content-Type header that describes it.
func multipartBody(t *testing.T, field, filename, contentType string, data []byte) (string, string) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(header)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf.String(), mw.FormDataContentType()
}

// testPNG encodes a w×h PNG.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	return buf.Bytes()
}

// uploadServer is a member's server with a real blob store behind it, plus
// the attachment the store recorded.
type uploadServer struct {
	store   *fakeStore
	blobs   *blobstore.Store
	handler http.Handler
	// recorded is what CreateAttachment was called with.
	recorded storage.NewAttachment
}

func newUploadServer(t *testing.T) *uploadServer {
	t.Helper()

	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	up := &uploadServer{store: memberStore(), blobs: blobs}
	up.store.createAttachment = func(_ context.Context, na storage.NewAttachment) (storage.Attachment, error) {
		up.recorded = na
		return storage.Attachment{
			ID:           na.ID,
			ChannelID:    na.ChannelID,
			UploaderID:   na.UploaderID,
			Filename:     na.Filename,
			ContentType:  na.ContentType,
			SizeBytes:    na.SizeBytes,
			Width:        na.Width,
			Height:       na.Height,
			HasThumbnail: na.HasThumbnail,
		}, nil
	}
	up.handler = httpserver.Handler(up.store, httpserver.WithUploads(blobs, nil))
	return up
}

// upload posts one file part and returns the recorder.
func (u *uploadServer) upload(t *testing.T, filename, contentType string, data []byte) *httptest.ResponseRecorder {
	t.Helper()

	body, formType := multipartBody(t, "file", filename, contentType, data)
	req := request(http.MethodPost, uploadPath(), body, withSessionCookie("tok"), withCSRF())
	req.Header.Set("Content-Type", formType)

	rec := httptest.NewRecorder()
	u.handler.ServeHTTP(rec, req)
	return rec
}

// storedBytes reads back what the blob store holds for one variant.
func (u *uploadServer) storedBytes(t *testing.T, id uuid.UUID, variant blobstore.Variant) []byte {
	t.Helper()

	f, err := u.blobs.Open(id, variant)
	if err != nil {
		t.Fatalf("open stored blob: %v", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("close stored blob: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	return data
}

func TestUploadFileStoresAnImageWithItsDimensions(t *testing.T) {
	t.Parallel()

	up := newUploadServer(t)
	source := testPNG(t, 800, 400)
	rec := up.upload(t, "holiday.png", "image/png", source)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var att api.Attachment
	if err := json.Unmarshal(rec.Body.Bytes(), &att); err != nil {
		t.Fatalf("body is not an Attachment: %v", err)
	}
	if att.Filename != "holiday.png" || att.ContentType != "image/png" {
		t.Errorf("attachment = %+v, want the declared name and label", att)
	}
	if att.Width == nil || *att.Width != 800 || att.Height == nil || *att.Height != 400 {
		t.Errorf("dimensions = %v x %v, want 800 x 400", att.Width, att.Height)
	}
	if att.ThumbnailUrl == nil {
		t.Error("an 800px image was serialized without a thumbnail URL")
	}
	// The URL is the credential and is minted per serialization; all this
	// asserts is that one was minted and that it names the attachment.
	if !strings.Contains(att.Url, att.Id.String()) {
		t.Errorf("url %q does not name the attachment", att.Url)
	}

	// The bytes really landed, under the server's own id.
	stored := up.storedBytes(t, att.Id, blobstore.Original)
	if int64(len(stored)) != att.SizeBytes {
		t.Errorf("stored %d bytes, reported %d", len(stored), att.SizeBytes)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(stored)); err != nil {
		t.Errorf("the stored original does not decode: %v", err)
	} else if cfg.Width != 800 || cfg.Height != 400 {
		t.Errorf("the stored original is %dx%d", cfg.Width, cfg.Height)
	}
	if thumb := up.storedBytes(t, att.Id, blobstore.Thumbnail); len(thumb) == 0 {
		t.Error("has_thumbnail was set but no thumbnail was stored")
	}
	if !up.recorded.HasThumbnail {
		t.Error("the recorded row does not say it has a thumbnail")
	}
	if up.recorded.ID != att.Id {
		t.Errorf("the row was recorded under %s but served as %s", up.recorded.ID, att.Id)
	}
}

func TestUploadFileStoresAnythingElseAsAnOpaqueBlob(t *testing.T) {
	t.Parallel()

	up := newUploadServer(t)
	// An SVG is a document that can carry script. It is storable — the
	// serving side is what refuses to render it — but nothing about it is
	// treated as an image here.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	rec := up.upload(t, "drawing.svg", "text/plain", svg)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var att api.Attachment
	if err := json.Unmarshal(rec.Body.Bytes(), &att); err != nil {
		t.Fatalf("body is not an Attachment: %v", err)
	}
	if att.Width != nil || att.Height != nil || att.ThumbnailUrl != nil {
		t.Errorf("an opaque blob was given image fields: %+v", att)
	}
	if got := up.storedBytes(t, att.Id, blobstore.Original); !bytes.Equal(got, svg) {
		t.Error("an opaque blob was rewritten on the way in; it must be stored byte for byte")
	}
	if _, err := up.blobs.Open(att.Id, blobstore.Thumbnail); err == nil {
		t.Error("an opaque blob got a thumbnail")
	}
}

func TestUploadFileRefusesBytesThatAreNotTheDeclaredImage(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		filename    string
		contentType string
		data        []byte
	}{
		"an SVG declaring itself an image": {
			"payload.svg", "image/svg+xml",
			[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		},
		"HTML wearing a PNG label": {
			"page.png", "image/png",
			[]byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>"),
		},
		"a PNG declared as a JPEG": {"photo.jpg", "image/jpeg", nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUploadServer(t)
			data := tc.data
			if data == nil {
				data = testPNG(t, 8, 8)
			}

			rec := up.upload(t, tc.filename, tc.contentType, data)
			wantError(t, rec, http.StatusUnsupportedMediaType, "content_type_mismatch")
			if up.recorded.ID != uuid.Nil {
				t.Error("a refused upload still recorded a row")
			}
		})
	}
}

func TestUploadFileRefusesAFileOverTheCap(t *testing.T) {
	t.Parallel()

	up := newUploadServer(t)

	// One byte past the published cap. The reader refuses it, so the server
	// never holds the whole thing.
	var info api.InstanceInfo
	instance := do(t, up.store, request(http.MethodGet, "/api/v1/instance", ""))
	if err := json.Unmarshal(instance.Body.Bytes(), &info); err != nil {
		t.Fatalf("instance body: %v", err)
	}
	if info.MaxFileSizeBytes <= 0 {
		t.Fatalf("the instance document publishes max_file_size_bytes = %d", info.MaxFileSizeBytes)
	}

	oversized := bytes.Repeat([]byte("A"), int(info.MaxFileSizeBytes)+1)
	rec := up.upload(t, "huge.bin", "application/octet-stream", oversized)
	wantError(t, rec, http.StatusRequestEntityTooLarge, "file_too_large")
	if up.recorded.ID != uuid.Nil {
		t.Error("an oversized upload still recorded a row")
	}
}

func TestUploadFileRefusesAMalformedRequest(t *testing.T) {
	t.Parallel()

	t.Run("a body that is not multipart", func(t *testing.T) {
		t.Parallel()
		up := newUploadServer(t)
		req := request(http.MethodPost, uploadPath(), `{"file":"nope"}`,
			withSessionCookie("tok"), withCSRF())
		rec := httptest.NewRecorder()
		up.handler.ServeHTTP(rec, req)
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a multipart body with no file part", func(t *testing.T) {
		t.Parallel()
		up := newUploadServer(t)
		body, formType := multipartBody(t, "notfile", "x.png", "image/png", testPNG(t, 4, 4))
		req := request(http.MethodPost, uploadPath(), body, withSessionCookie("tok"), withCSRF())
		req.Header.Set("Content-Type", formType)

		rec := httptest.NewRecorder()
		up.handler.ServeHTTP(rec, req)
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a file part with no filename", func(t *testing.T) {
		t.Parallel()
		up := newUploadServer(t)
		rec := up.upload(t, "", "application/octet-stream", []byte("bytes"))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a filename that is a path", func(t *testing.T) {
		t.Parallel()
		up := newUploadServer(t)
		// The name is display data and never reaches the filesystem, so the
		// directory part is simply dropped rather than refused.
		rec := up.upload(t, `../../etc/passwd`, "application/octet-stream", []byte("bytes"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if up.recorded.Filename != "passwd" {
			t.Errorf("stored filename %q, want the last path element", up.recorded.Filename)
		}
	})
}

func TestUploadFileIsRefusedToAStranger(t *testing.T) {
	t.Parallel()

	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	// A non-member gets the channel's own 404, and nothing is read from
	// their body on the way to it.
	store := channelStore(fixtureUser(), fixtureChannel(), false)
	store.createAttachment = func(context.Context, storage.NewAttachment) (storage.Attachment, error) {
		t.Error("a stranger's upload reached storage")
		return storage.Attachment{}, errors.New("must not be called")
	}

	body, formType := multipartBody(t, "file", "x.png", "image/png", testPNG(t, 4, 4))
	req := request(http.MethodPost, uploadPath(), body, withSessionCookie("tok"), withCSRF())
	req.Header.Set("Content-Type", formType)

	rec := httptest.NewRecorder()
	httpserver.Handler(store, httpserver.WithUploads(blobs, nil)).ServeHTTP(rec, req)
	wantError(t, rec, http.StatusNotFound, "channel_not_found")
}

func TestUploadFileWithoutABlobStoreIsAnInternalError(t *testing.T) {
	t.Parallel()

	// A server wired without the upload pipeline is a test fixture, never
	// production. It must not answer as though it stored anything.
	store := memberStore()
	body, formType := multipartBody(t, "file", "x.png", "image/png", testPNG(t, 4, 4))
	req := request(http.MethodPost, uploadPath(), body, withSessionCookie("tok"), withCSRF())
	req.Header.Set("Content-Type", formType)

	wantError(t, do(t, store, req), http.StatusInternalServerError, "internal_error")
}
