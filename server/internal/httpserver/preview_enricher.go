package httpserver

import (
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// PreviewEnricher is the asynchronous link-preview pipeline
// (internal/linkpreview), declared here for the same reason Realtime is:
// this package is the one that calls it, and the thing that implements it
// must be replaceable without touching a handler.
//
// Enqueue must not block and must not fail. A send returns as soon as the
// message is stored; the preview arrives later as a message_updated event or
// it does not arrive at all, and neither outcome is the author's business
// (ADR 003).
type PreviewEnricher interface {
	Enqueue(channelID uuid.UUID, message storage.Message)
}

// WithLinkPreviews attaches the enricher. Without it, messages simply carry
// no preview cards — the honest state of an install with no egress.
func WithLinkPreviews(enricher PreviewEnricher) Option {
	return func(s *apiServer) {
		if enricher != nil {
			s.previews = enricher
		}
	}
}

// enrichPreview asks for a message to be enriched, if enrichment is wired.
//
// Call it after a successful send and after a successful edit. An edit is
// not only a re-fetch: content that no longer holds a URL must lose its
// card, which is a removal only this call can start.
func (s *apiServer) enrichPreview(channelID uuid.UUID, message storage.Message) {
	if s.previews == nil {
		return
	}
	s.previews.Enqueue(channelID, message)
}

// apiLinkPreview maps a stored card onto the contract's LinkPreview.
//
// image_url is minted here, fresh, from this instance's own signer — never
// stored and never the remote site's URL. A reader's browser must not be
// made to fetch a stranger's server, and the app origin's img-src 'self'
// would refuse it if it were asked to (ADR 003).
//
// The signer is a parameter rather than read off the server, because the
// caller is message serialization and that is where the entitled reader is
// known: the URL is the credential, so it is minted per serialization and
// never cached.
func apiLinkPreview(preview storage.LinkPreview, signer fileURLSigner) *api.LinkPreview {
	out := &api.LinkPreview{Url: preview.URL}
	if preview.Title != "" {
		title := preview.Title
		out.Title = &title
	}
	if preview.Description != "" {
		description := preview.Description
		out.Description = &description
	}
	if preview.ImageBlobID != uuid.Nil && signer != nil {
		// Signed exactly like an attachment: the derivative is an ordinary
		// blob under a server-generated id, and it has no thumbnail because
		// it already is one.
		imageURL, _ := signer.AttachmentURLs(preview.ImageBlobID, false)
		out.ImageUrl = &imageURL
	}
	return out
}
