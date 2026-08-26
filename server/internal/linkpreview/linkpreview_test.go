package linkpreview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// --- fakes ------------------------------------------------------------

type fetchResult struct {
	body        []byte
	contentType string
	err         error
}

type fakeFetcher struct {
	mu      sync.Mutex
	results map[string]fetchResult
	got     []string
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string, limit int64) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.got = append(f.got, rawURL)
	result, ok := f.results[rawURL]
	if !ok {
		return nil, "", errors.New("fakeFetcher: nothing configured for " + rawURL)
	}
	if result.err != nil {
		return nil, "", result.err
	}
	body := result.body
	if int64(len(body)) > limit {
		body = body[:limit]
	}
	return body, result.contentType, nil
}

func (f *fakeFetcher) fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

type fakeStore struct {
	mu      sync.Mutex
	saved   []storage.LinkPreview
	held    map[uuid.UUID]bool
	saveErr error
}

func newFakeStore() *fakeStore { return &fakeStore{held: map[uuid.UUID]bool{}} }

func (s *fakeStore) SaveLinkPreview(_ context.Context, preview storage.LinkPreview) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = append(s.saved, preview)
	s.held[preview.MessageID] = true
	return nil
}

func (s *fakeStore) DeleteLinkPreview(_ context.Context, messageID uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	had := s.held[messageID]
	delete(s.held, messageID)
	return had, nil
}

func (s *fakeStore) previews() []storage.LinkPreview {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.LinkPreview(nil), s.saved...)
}

type fakeBlobs struct {
	mu      sync.Mutex
	written map[uuid.UUID][]byte
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{written: map[uuid.UUID][]byte{}} }

func (b *fakeBlobs) WriteBlob(_ context.Context, id uuid.UUID, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.written[id] = data
	return nil
}

func (b *fakeBlobs) blob(id uuid.UUID) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written[id]
}

type announcement struct {
	channelID uuid.UUID
	message   storage.Message
}

type fakeRealtime struct {
	events chan announcement
}

func newFakeRealtime() *fakeRealtime {
	return &fakeRealtime{events: make(chan announcement, 8)}
}

func (r *fakeRealtime) MessageUpdated(channelID uuid.UUID, message storage.Message) {
	r.events <- announcement{channelID: channelID, message: message}
}

// announced returns the next event, or reports that none arrived.
func (r *fakeRealtime) announced(t *testing.T) (announcement, bool) {
	t.Helper()

	select {
	case event := <-r.events:
		return event, true
	case <-time.After(2 * time.Second):
		return announcement{}, false
	}
}

func (r *fakeRealtime) silent(t *testing.T) {
	t.Helper()

	select {
	case event := <-r.events:
		t.Fatalf("unexpected message_updated for %s", event.message.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

// testEnricher builds an enricher without starting its worker, so a test
// can drive one job and assert on the result with no goroutine in the way.
func testEnricher(fetcher Fetcher, store Store, blobs BlobWriter, realtime Announcer) *Enricher {
	return &Enricher{fetcher: fetcher, store: store, blobs: blobs, realtime: realtime}
}

const samplePage = `<!doctype html><html><head>
	<title>Tab title</title>
	<meta name="description" content="the plain one">
	<meta property="og:title" content="Shared title">
	<meta property="og:description" content="Shared description">
	<meta property="og:image" content="/card.png">
	</head><body><title>ignored</title></body></html>`

func testMessage() storage.Message {
	return storage.Message{
		ID:          uuid.New(),
		ChannelID:   uuid.New(),
		ClientMsgID: uuid.New(),
		Content:     "look at https://example.test/post please",
		CreatedAt:   time.Now().UTC(),
	}
}

// --- the enricher -----------------------------------------------------

// TestProcessStoresTheCardAndAnnouncesIt is the happy path end to end, and
// the place ws-protocol §4's rule is pinned: the announced message is the
// one that was sent, edited_at still nil. A card the server added must never
// mark somebody's message "(edited)".
func TestProcessStoresTheCardAndAnnouncesIt(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{results: map[string]fetchResult{
		"https://example.test/post":     {body: []byte(samplePage), contentType: "text/html; charset=utf-8"},
		"https://example.test/card.png": {body: pngFixture(t, 800, 400), contentType: "image/png"},
	}}
	store := newFakeStore()
	blobs := newFakeBlobs()
	realtime := newFakeRealtime()
	message := testMessage()

	testEnricher(fetcher, store, blobs, realtime).process(t.Context(), job{channelID: message.ChannelID, message: message})

	saved := store.previews()
	if len(saved) != 1 {
		t.Fatalf("stored %d previews, want 1", len(saved))
	}
	preview := saved[0]
	if preview.MessageID != message.ID {
		t.Errorf("preview.MessageID = %s, want %s", preview.MessageID, message.ID)
	}
	if preview.URL != "https://example.test/post" {
		t.Errorf("preview.URL = %q", preview.URL)
	}
	if preview.Title != "Shared title" {
		t.Errorf("preview.Title = %q, want the og:title", preview.Title)
	}
	if preview.Description != "Shared description" {
		t.Errorf("preview.Description = %q, want the og:description", preview.Description)
	}
	if preview.ImageBlobID == uuid.Nil {
		t.Fatal("preview carries no image blob")
	}

	// The image was re-hosted: bytes in the blob store, bounded, decodable.
	derivative := blobs.blob(preview.ImageBlobID)
	config, _, err := image.DecodeConfig(bytes.NewReader(derivative))
	if err != nil {
		t.Fatalf("stored derivative does not decode: %v", err)
	}
	if config.Width > maxImageEdge || config.Height > maxImageEdge {
		t.Errorf("derivative is %dx%d, over the %d cap", config.Width, config.Height, maxImageEdge)
	}

	// The relative og:image resolved against the page it came from.
	if got := fetcher.fetched(); len(got) != 2 || got[1] != "https://example.test/card.png" {
		t.Errorf("fetches = %v, want the page then the resolved image", got)
	}

	event, ok := realtime.announced(t)
	if !ok {
		t.Fatal("no message_updated announced")
	}
	if event.channelID != message.ChannelID {
		t.Errorf("announced to channel %s, want %s", event.channelID, message.ChannelID)
	}
	if event.message.EditedAt != nil {
		t.Error("enrichment stamped edited_at; ws-protocol §4 forbids it")
	}
	if event.message.Content != message.Content {
		t.Errorf("announced content = %q, want it untouched", event.message.Content)
	}
}

// TestProcessRemovesTheCardWhenAnEditDropsTheURL: an edit is also how a card
// is taken away, and the removal is announced so the client stops drawing it.
func TestProcessRemovesTheCardWhenAnEditDropsTheURL(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	message := testMessage()
	store.held[message.ID] = true
	realtime := newFakeRealtime()
	enricher := testEnricher(&fakeFetcher{}, store, nil, realtime)

	message.Content = "never mind"
	enricher.process(t.Context(), job{channelID: message.ChannelID, message: message})

	if store.held[message.ID] {
		t.Error("the card survived an edit that removed the link")
	}
	if _, ok := realtime.announced(t); !ok {
		t.Error("removing a card was not announced")
	}
}

// TestProcessIsQuietWhenThereWasNoCardToRemove keeps every ordinary message
// off the socket: a send with no URL must not produce a message_updated.
func TestProcessIsQuietWhenThereWasNoCardToRemove(t *testing.T) {
	t.Parallel()

	realtime := newFakeRealtime()
	message := testMessage()
	message.Content = "no links here"

	testEnricher(&fakeFetcher{}, newFakeStore(), nil, realtime).
		process(t.Context(), job{channelID: message.ChannelID, message: message})

	realtime.silent(t)
}

// TestProcessDoesNotAnnounceADeletedMessage: the placeholder was already
// announced as message_deleted, and a second event about a deleted message
// would ask the client to redraw one.
func TestProcessDoesNotAnnounceADeletedMessage(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	message := testMessage()
	store.held[message.ID] = true
	deletedAt := time.Now().UTC()
	message.DeletedAt = &deletedAt
	message.Content = ""
	realtime := newFakeRealtime()

	testEnricher(&fakeFetcher{}, store, nil, realtime).
		process(t.Context(), job{channelID: message.ChannelID, message: message})

	if store.held[message.ID] {
		t.Error("a deleted message kept its card")
	}
	realtime.silent(t)
}

// TestProcessIsSilentOnEveryFetchFailure is the ADR's rule that a preview is
// a nicety: nothing is stored, nothing is announced, nobody is told.
func TestProcessIsSilentOnEveryFetchFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result fetchResult
	}{
		{"blocked address", fetchResult{err: errors.New("egress: address is not permitted")}},
		{"not html", fetchResult{body: []byte("%PDF-1.7"), contentType: "application/pdf"}},
		{"no metadata", fetchResult{body: []byte("<html><body>hi</body></html>"), contentType: "text/html"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore()
			realtime := newFakeRealtime()
			message := testMessage()
			fetcher := &fakeFetcher{results: map[string]fetchResult{
				"https://example.test/post": tt.result,
			}}

			testEnricher(fetcher, store, nil, realtime).
				process(t.Context(), job{channelID: message.ChannelID, message: message})

			if got := store.previews(); len(got) != 0 {
				t.Errorf("stored %d previews, want none", len(got))
			}
			realtime.silent(t)
		})
	}
}

// TestProcessStoresTheCardWhenOnlyTheImageFails: a dead image costs the card
// its picture, not the card.
func TestProcessStoresTheCardWhenOnlyTheImageFails(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{results: map[string]fetchResult{
		"https://example.test/post":     {body: []byte(samplePage), contentType: "text/html"},
		"https://example.test/card.png": {err: errors.New("egress: address is not permitted")},
	}}
	store := newFakeStore()
	realtime := newFakeRealtime()
	message := testMessage()

	testEnricher(fetcher, store, newFakeBlobs(), realtime).
		process(t.Context(), job{channelID: message.ChannelID, message: message})

	saved := store.previews()
	if len(saved) != 1 {
		t.Fatalf("stored %d previews, want 1", len(saved))
	}
	if saved[0].ImageBlobID != uuid.Nil {
		t.Error("a failed image fetch still produced a blob id")
	}
	if _, ok := realtime.announced(t); !ok {
		t.Error("the card was not announced")
	}
}

// TestProcessWithoutABlobStoreStillMakesACard is the not-yet-wired install:
// text card, no picture, no crash.
func TestProcessWithoutABlobStoreStillMakesACard(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{results: map[string]fetchResult{
		"https://example.test/post": {body: []byte(samplePage), contentType: "text/html"},
	}}
	store := newFakeStore()
	message := testMessage()

	testEnricher(fetcher, store, nil, newFakeRealtime()).
		process(t.Context(), job{channelID: message.ChannelID, message: message})

	saved := store.previews()
	if len(saved) != 1 || saved[0].ImageBlobID != uuid.Nil {
		t.Fatalf("previews = %+v, want one card with no image", saved)
	}
}

// TestEnqueueEnrichesThroughTheWorker exercises the goroutine, the queue and
// Close — the plumbing the other tests bypass by calling process directly.
func TestEnqueueEnrichesThroughTheWorker(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{results: map[string]fetchResult{
		"https://example.test/post": {body: []byte(samplePage), contentType: "text/html"},
	}}
	realtime := newFakeRealtime()
	enricher := New(fetcher, newFakeStore(), nil, realtime)
	defer enricher.Close()

	message := testMessage()
	enricher.Enqueue(message.ChannelID, message)

	event, ok := realtime.announced(t)
	if !ok {
		t.Fatal("the worker announced nothing")
	}
	if event.message.ID != message.ID {
		t.Errorf("announced %s, want %s", event.message.ID, message.ID)
	}
}

// TestCloseIsIdempotent: shutdown paths call it more than once.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	enricher := New(&fakeFetcher{}, newFakeStore(), nil, newFakeRealtime())
	enricher.Close()
	enricher.Close()
}

// TestEnqueueAfterCloseDoesNotPanic: an in-flight request racing shutdown
// drops its preview, which is the whole point of never closing the queue.
func TestEnqueueAfterCloseDoesNotPanic(t *testing.T) {
	t.Parallel()

	enricher := New(&fakeFetcher{}, newFakeStore(), nil, newFakeRealtime())
	enricher.Close()

	message := testMessage()
	enricher.Enqueue(message.ChannelID, message)
}

// --- extraction and parsing -------------------------------------------

func TestFirstURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"plain", "see https://go.dev/doc for it", "https://go.dev/doc"},
		{"http too", "http://example.test/x", "http://example.test/x"},
		{"trailing sentence dot", "read https://go.dev/doc.", "https://go.dev/doc"},
		{"markdown link", "[the docs](https://go.dev/doc)", "https://go.dev/doc"},
		{"first of several", "https://a.test/1 and https://b.test/2", "https://a.test/1"},
		{"persian text around it", "این را ببین https://go.dev/doc خوب است", "https://go.dev/doc"},
		{"no url", "nothing to see", ""},
		{"other scheme", "ftp://files.test/x", ""},
		{"scheme only", "https://", ""},
		{"over the length cap", "https://a.test/" + strings.Repeat("x", maxURLLen), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := firstURL(tt.content); got != tt.want {
				t.Errorf("firstURL(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestParseMetaPrefersOpenGraph(t *testing.T) {
	t.Parallel()

	meta := parseMeta(strings.NewReader(samplePage))
	if got := meta.title(); got != "Shared title" {
		t.Errorf("title = %q, want the og:title", got)
	}
	if got := meta.description(); got != "Shared description" {
		t.Errorf("description = %q, want the og:description", got)
	}
	if meta.image != "/card.png" {
		t.Errorf("image = %q", meta.image)
	}
}

func TestParseMetaFallsBackToTitleTag(t *testing.T) {
	t.Parallel()

	meta := parseMeta(strings.NewReader(
		`<html><head><title>  Just a tab  </title><meta name="description" content="plain"></head></html>`))
	if got := meta.title(); got != "Just a tab" {
		t.Errorf("title = %q, want the <title> trimmed", got)
	}
	if got := meta.description(); got != "plain" {
		t.Errorf("description = %q, want the plain meta", got)
	}
}

// TestParseMetaStopsAtBody: <body> ends the interesting part, and a page
// that repeats og:title in its body cannot overwrite what <head> declared.
func TestParseMetaStopsAtBody(t *testing.T) {
	t.Parallel()

	meta := parseMeta(strings.NewReader(
		`<html><head><title>head</title></head><body><meta property="og:title" content="body"></body></html>`))
	if got := meta.title(); got != "head" {
		t.Errorf("title = %q, want the head's", got)
	}
}

// TestParseMetaSurvivesGarbage: a truncated or nonsense document yields an
// empty card, never a panic — the 512 KiB cap guarantees truncated input.
func TestParseMetaSurvivesGarbage(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"", "<<<<", "<html><head><meta property=", "\x00\x01\x02", samplePage[:80],
	} {
		if got := parseMeta(strings.NewReader(input)).title(); strings.Contains(got, "\x00") {
			t.Errorf("parseMeta(%q) title = %q", input, got)
		}
	}
}

func TestClipCountsRunesAndCollapsesWhitespace(t *testing.T) {
	t.Parallel()

	if got := clip("  a\n\tb  ", 10); got != "a b" {
		t.Errorf("clip = %q, want %q", got, "a b")
	}
	long := strings.Repeat("ن", 300)
	if got := []rune(clip(long, maxTitleRunes)); len(got) != maxTitleRunes {
		t.Errorf("clip kept %d runes, want %d", len(got), maxTitleRunes)
	}
}

func TestResolveImageURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, page, ref, want string
	}{
		{"relative", "https://a.test/blog/post", "/card.png", "https://a.test/card.png"},
		{"protocol relative", "https://a.test/p", "//b.test/c.png", "https://b.test/c.png"},
		{"absolute", "https://a.test/p", "https://cdn.test/c.png", "https://cdn.test/c.png"},
		{"data uri refused", "https://a.test/p", "data:image/png;base64,AAAA", ""},
		{"javascript refused", "https://a.test/p", "javascript:alert(1)", ""},
		{"empty", "https://a.test/p", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := resolveImageURL(tt.page, tt.ref); got != tt.want {
				t.Errorf("resolveImageURL(%q, %q) = %q, want %q", tt.page, tt.ref, got, tt.want)
			}
		})
	}
}

// --- the image derivative ---------------------------------------------

func TestBoundedImageShrinksToTheCap(t *testing.T) {
	t.Parallel()

	derivative, err := boundedImage(pngFixture(t, 2000, 1000))
	if err != nil {
		t.Fatalf("boundedImage: %v", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(derivative))
	if err != nil {
		t.Fatalf("decode derivative: %v", err)
	}
	if config.Width != maxImageEdge || config.Height != maxImageEdge/2 {
		t.Errorf("derivative is %dx%d, want %dx%d", config.Width, config.Height, maxImageEdge, maxImageEdge/2)
	}
}

// TestBoundedImageRefusesABombBeforeDecoding is ADR 003's before-decode
// rule. The fixture is a PNG whose header claims 40001x1000 pixels and whose
// pixel data is one tiny image — decoding it would be the mistake, so the
// header check has to answer first.
func TestBoundedImageRefusesABombBeforeDecoding(t *testing.T) {
	t.Parallel()

	bomb := pngWithDeclaredSize(t, 40001, 1000)
	if _, err := boundedImage(bomb); err == nil {
		t.Fatal("boundedImage accepted an image over the pixel cap")
	}
}

// TestBoundedImageStripsMetadata: the derivative is re-encoded from pixels,
// so an EXIF segment carrying somebody's GPS coordinates cannot survive into
// something a reader downloads.
func TestBoundedImageStripsMetadata(t *testing.T) {
	t.Parallel()

	original := jpegWithExif(t, 100, 100)
	if !bytes.Contains(original, []byte("Exif")) {
		t.Fatal("fixture has no EXIF segment to strip")
	}

	derivative, err := boundedImage(original)
	if err != nil {
		t.Fatalf("boundedImage: %v", err)
	}
	if bytes.Contains(derivative, []byte("Exif")) {
		t.Error("the derivative still carries an EXIF segment")
	}
	if bytes.Contains(derivative, []byte("51.5074")) {
		t.Error("the derivative still carries the original's GPS payload")
	}
}

func TestBoundedImageRefusesNonImages(t *testing.T) {
	t.Parallel()

	for _, data := range [][]byte{nil, []byte("<html>"), []byte("\x89PNG\r\n\x1a\n truncated")} {
		if _, err := boundedImage(data); err == nil {
			t.Errorf("boundedImage(%q) accepted a non-image", data)
		}
	}
}

// --- fixtures ---------------------------------------------------------

func pngFixture(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		img.Set(x, height/2, color.RGBA{R: uint8(x % 256), G: 0x20, B: 0x40, A: 0xff})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png fixture: %v", err)
	}
	return buf.Bytes()
}

// pngWithDeclaredSize rewrites a small PNG's IHDR so its header claims a
// size its pixel data does not have — an image bomb as cheap to build as it
// is to serve.
func pngWithDeclaredSize(t *testing.T, width, height uint32) []byte {
	t.Helper()

	data := pngFixture(t, 4, 4)
	// Layout: 8-byte signature, then the IHDR chunk — 4 length, 4 type, then
	// width and height as big-endian uint32s, then the rest and the CRC over
	// type+data.
	const ihdrType, ihdrData, ihdrLen = 12, 16, 13
	binary.BigEndian.PutUint32(data[ihdrData:], width)
	binary.BigEndian.PutUint32(data[ihdrData+4:], height)
	crc := crc32.ChecksumIEEE(data[ihdrType : ihdrType+4+ihdrLen])
	binary.BigEndian.PutUint32(data[ihdrType+4+ihdrLen:], crc)
	return data
}

// jpegWithExif splices an APP1 EXIF segment carrying a fake coordinate
// straight after the SOI marker, the way a phone camera does.
func jpegWithExif(t *testing.T, width, height int) []byte {
	t.Helper()

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg fixture: %v", err)
	}
	encoded := buf.Bytes()

	payload := append([]byte("Exif\x00\x00"), []byte("GPS 51.5074,-0.1278")...)
	segment := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}

	out := make([]byte, 0, len(encoded)+len(segment)+len(payload))
	out = append(out, encoded[:2]...) // SOI
	out = append(out, segment...)
	out = append(out, payload...)
	return append(out, encoded[2:]...)
}
