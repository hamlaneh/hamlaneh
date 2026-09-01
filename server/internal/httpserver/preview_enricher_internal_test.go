package httpserver

import (
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

type recordingEnricher struct {
	channelID uuid.UUID
	message   storage.Message
	calls     int
}

func (r *recordingEnricher) Enqueue(channelID uuid.UUID, message storage.Message) {
	r.channelID = channelID
	r.message = message
	r.calls++
}

// TestEnrichPreviewIsSafeWithoutAnEnricher: an install with no egress
// pipeline must serve messages, not panic on every send.
func TestEnrichPreviewIsSafeWithoutAnEnricher(t *testing.T) {
	t.Parallel()

	newAPIServer(nil).enrichPreview(uuid.New(), storage.Message{ID: uuid.New()})
}

func TestEnrichPreviewForwardsToTheEnricher(t *testing.T) {
	t.Parallel()

	enricher := &recordingEnricher{}
	server := newAPIServer(nil, WithLinkPreviews(enricher))
	channelID := uuid.New()
	message := storage.Message{ID: uuid.New(), ChannelID: channelID}

	server.enrichPreview(channelID, message)

	if enricher.calls != 1 {
		t.Fatalf("Enqueue called %d times, want 1", enricher.calls)
	}
	if enricher.channelID != channelID || enricher.message.ID != message.ID {
		t.Errorf("enqueued (%s, %s), want (%s, %s)",
			enricher.channelID, enricher.message.ID, channelID, message.ID)
	}
}

// TestWithLinkPreviewsIgnoresNil keeps the option total: passing a nil
// enricher leaves the server in the same state as not passing one.
func TestWithLinkPreviewsIgnoresNil(t *testing.T) {
	t.Parallel()

	if server := newAPIServer(nil, WithLinkPreviews(nil)); server.previews != nil {
		t.Error("a nil enricher was installed")
	}
}

func TestAPILinkPreviewMapsTheCard(t *testing.T) {
	t.Parallel()

	blobID := uuid.New()
	preview := storage.LinkPreview{
		MessageID:   uuid.New(),
		URL:         "https://example.test/post",
		Title:       "A title",
		Description: "A description",
		ImageBlobID: blobID,
	}

	signer := unsignedFileURLs{}
	card := apiLinkPreview(preview, signer)

	if card.Url != preview.URL {
		t.Errorf("url = %q, want %q", card.Url, preview.URL)
	}
	if card.Title == nil || *card.Title != preview.Title {
		t.Errorf("title = %v, want %q", card.Title, preview.Title)
	}
	if card.Description == nil || *card.Description != preview.Description {
		t.Errorf("description = %v, want %q", card.Description, preview.Description)
	}
	// The image URL is this instance's own, minted from the blob id — never
	// the remote site's.
	if card.ImageUrl == nil {
		t.Fatal("image_url is absent")
	}
	want, _ := signer.AttachmentURLs(blobID, false)
	if *card.ImageUrl != want {
		t.Errorf("image_url = %q, want %q", *card.ImageUrl, want)
	}
}

// TestAPILinkPreviewOmitsWhatThePageDidNotOffer: a card with no image, no
// title and no description carries absent fields rather than empty strings,
// which is what the contract's nullable properties mean.
func TestAPILinkPreviewOmitsWhatThePageDidNotOffer(t *testing.T) {
	t.Parallel()

	card := apiLinkPreview(storage.LinkPreview{URL: "https://example.test/"}, unsignedFileURLs{})

	if card.Title != nil || card.Description != nil || card.ImageUrl != nil {
		t.Errorf("card = %+v, want only url set", card)
	}
}
