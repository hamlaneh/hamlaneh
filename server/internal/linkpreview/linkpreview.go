// Package linkpreview turns the first http(s) URL in a message into the card
// the client draws under it: title, description, and an image re-hosted on
// this instance.
//
// Two properties shape everything here. The fetch is somebody else's URL, so
// it goes through internal/egress and nothing in this package is allowed to
// reach the network any other way. And the fetch is a nicety, so it happens
// off the request path: a send returns as soon as the message is stored, the
// preview arrives later as a message_updated event, and every way it can
// fail — blocked address, timeout, a page with no metadata, a dead image —
// is silent to the author and logged for the operator (ADR 003). There are
// no retries in this slice.
//
// Enrichment never touches the messages row. The card lives in its own table
// (migration 0008), so a preview the server added can never stamp somebody's
// message "(edited)" — ws-protocol.md §4 is explicit that edited_at belongs
// to real edits.
package linkpreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

const (
	// The size caps of ADR 003.
	maxHTMLBytes  = 512 << 10
	maxImageBytes = 5 << 20

	// queueDepth bounds the backlog. A full queue drops the enrichment
	// rather than blocking a handler: the message is already stored and
	// delivered, and a card is not worth making somebody wait for.
	queueDepth = 128

	// jobTimeout bounds one whole enrichment — a page fetch, an image fetch,
	// a decode and two statements. Each fetch carries the egress client's own
	// 5-second budget; this is the ceiling on all of it together.
	jobTimeout = 20 * time.Second
)

// errNothingToShow is a page that fetched fine and offered nothing worth
// drawing. It is an ordinary outcome, not a failure to report.
var errNothingToShow = errors.New("linkpreview: page has no title or description")

// Fetcher is the guarded HTTP client this package fetches through:
// *egress.Client in production, a fake in tests. Nothing else in this
// package opens a connection, which is what keeps the SSRF surface to one
// audited implementation.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, limit int64) ([]byte, string, error)
}

// Store is the preview persistence. *storage.Store implements it.
type Store interface {
	SaveLinkPreview(ctx context.Context, preview storage.LinkPreview) error
	DeleteLinkPreview(ctx context.Context, messageID uuid.UUID) (bool, error)
}

// BlobWriter is the narrow slice of the blob store this package needs: put
// these bytes under this id. Declared here rather than imported so the
// preview pipeline and the upload pipeline can be built and tested
// independently; the wiring picks the one implementation.
//
// A nil BlobWriter is legal and means previews carry no image — which is the
// honest behaviour of an install whose blob store is not wired yet.
type BlobWriter interface {
	WriteBlob(ctx context.Context, id uuid.UUID, data []byte) error
}

// Announcer is the message_updated half of httpserver.Realtime, named here
// so this package does not depend on the HTTP layer to announce.
type Announcer interface {
	MessageUpdated(channelID uuid.UUID, message storage.Message)
}

// job is one message waiting to be enriched.
type job struct {
	channelID uuid.UUID
	message   storage.Message
}

// Enricher runs preview fetches off the request path. Use New; the zero
// value is not usable.
//
// One worker, deliberately. Enrichment is a background courtesy and the
// thing it must never do is let a burst of messages carrying links turn into
// a burst of outbound connections from the instance — that is a
// denial-of-service amplifier aimed through this server at somebody else.
// Serial is also fast enough: each job is bounded at jobTimeout and the
// queue absorbs bursts.
type Enricher struct {
	fetcher  Fetcher
	store    Store
	blobs    BlobWriter
	realtime Announcer

	jobs   chan job
	cancel context.CancelFunc
	done   chan struct{}
}

// New starts the enricher's worker. Close stops it.
//
// blobs may be nil (previews then carry no image); the other three are
// required, because an enricher that cannot fetch, cannot store, or cannot
// announce has nothing to do.
func New(fetcher Fetcher, store Store, blobs BlobWriter, realtime Announcer) *Enricher {
	ctx, cancel := context.WithCancel(context.Background())
	enricher := &Enricher{
		fetcher:  fetcher,
		store:    store,
		blobs:    blobs,
		realtime: realtime,
		jobs:     make(chan job, queueDepth),
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go enricher.run(ctx)
	return enricher
}

// Enqueue asks for a message to be enriched. It never blocks and never
// fails: a full queue logs and drops.
//
// Call it after a send and after an edit. An edit is not just a re-fetch —
// it is also how a card gets removed, because content that no longer holds a
// URL must not keep a preview of one.
func (e *Enricher) Enqueue(channelID uuid.UUID, message storage.Message) {
	select {
	case e.jobs <- job{channelID: channelID, message: message}:
	default:
		slog.Warn("link preview queue full, dropping enrichment", "message_id", message.ID)
	}
}

// Close stops the worker and waits for the job in flight. It is safe to call
// more than once. The queue is deliberately never closed, so an Enqueue that
// races a shutdown drops a preview instead of panicking on a closed channel.
func (e *Enricher) Close() {
	e.cancel()
	<-e.done
}

func (e *Enricher) run(ctx context.Context) {
	defer close(e.done)

	for {
		select {
		case <-ctx.Done():
			return
		case next := <-e.jobs:
			jobCtx, cancel := context.WithTimeout(ctx, jobTimeout)
			e.process(jobCtx, next)
			cancel()
		}
	}
}

// process enriches one message. Every failure ends here: the author is never
// told, because there is nothing they could do about a stranger's server.
func (e *Enricher) process(ctx context.Context, next job) {
	target := firstURL(next.message.Content)
	if target == "" || next.message.DeletedAt != nil {
		e.removePreview(ctx, next)
		return
	}

	preview, err := e.build(ctx, next.message.ID, target)
	if err != nil {
		slog.Debug("no link preview", "message_id", next.message.ID, "error", err)
		return
	}
	if err = e.store.SaveLinkPreview(ctx, preview); err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			slog.Warn("store link preview", "message_id", next.message.ID, "error", err)
		}
		return
	}
	e.realtime.MessageUpdated(next.channelID, next.message)
}

// removePreview drops a card whose message no longer has a link, announcing
// only when there was one to drop — and never for a deleted message, whose
// disappearance was already announced as message_deleted.
func (e *Enricher) removePreview(ctx context.Context, next job) {
	removed, err := e.store.DeleteLinkPreview(ctx, next.message.ID)
	if err != nil {
		slog.Warn("remove link preview", "message_id", next.message.ID, "error", err)
		return
	}
	if removed && next.message.DeletedAt == nil {
		e.realtime.MessageUpdated(next.channelID, next.message)
	}
}

// build fetches a page and assembles the card. A page with neither a title
// nor a description is not a card, so it is refused rather than stored as an
// empty box under the message.
func (e *Enricher) build(ctx context.Context, messageID uuid.UUID, target string) (storage.LinkPreview, error) {
	page, contentType, err := e.fetcher.Fetch(ctx, target, maxHTMLBytes)
	if err != nil {
		return storage.LinkPreview{}, fmt.Errorf("fetch page: %w", err)
	}
	if !isHTML(contentType) {
		return storage.LinkPreview{}, fmt.Errorf("linkpreview: %s is %q, not a web page", target, contentType)
	}

	meta := parseMeta(bytes.NewReader(page))
	preview := storage.LinkPreview{
		MessageID:   messageID,
		URL:         target,
		Title:       meta.title(),
		Description: meta.description(),
	}
	if preview.Title == "" && preview.Description == "" {
		return storage.LinkPreview{}, errNothingToShow
	}

	// The image is optional in every sense: a page without one, an image
	// that will not decode, and a blob store that is not wired all produce
	// the same card without a picture.
	//
	// ponytail: a relative og:image is resolved against the URL as typed
	// rather than the URL finally landed on, so a page that cross-host
	// redirects and then offers a relative image loses its picture. Have
	// Fetcher report the final URL if that ever shows up in practice.
	if ref := resolveImageURL(target, meta.image); ref != "" {
		blobID, imageErr := e.storeImage(ctx, ref)
		if imageErr != nil {
			slog.Debug("no link preview image", "message_id", messageID, "error", imageErr)
		} else {
			preview.ImageBlobID = blobID
		}
	}
	return preview, nil
}

// storeImage fetches the page's image through the same guard, bounds it, and
// writes the derivative to the blob store under a fresh server-generated id.
//
// Re-hosting is the point: the contract's image_url is always this
// instance's own files origin, so a reader's browser is never made to fetch
// a stranger's server — and the app's img-src 'self' CSP would refuse it if
// it were.
func (e *Enricher) storeImage(ctx context.Context, imageURL string) (uuid.UUID, error) {
	if e.blobs == nil {
		return uuid.Nil, errors.New("linkpreview: no blob store wired")
	}

	data, _, err := e.fetcher.Fetch(ctx, imageURL, maxImageBytes)
	if err != nil {
		return uuid.Nil, fmt.Errorf("fetch image: %w", err)
	}
	derivative, err := boundedImage(data)
	if err != nil {
		return uuid.Nil, err
	}

	blobID := uuid.New()
	if err = e.blobs.WriteBlob(ctx, blobID, derivative); err != nil {
		return uuid.Nil, fmt.Errorf("write preview image: %w", err)
	}
	return blobID, nil
}

// isHTML reports whether a Content-Type is a web page. A missing type is
// accepted — plenty of servers omit it, and the parser's answer for bytes
// that are not markup is an empty card, which is already handled.
func isHTML(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}
