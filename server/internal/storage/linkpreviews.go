package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinkPreview is the card the server derived from the first http(s) URL in a
// message's content (migration 0008, ADR 003). It is derived data, kept in
// its own table so enrichment never touches the message somebody wrote:
// content and edited_at stay exactly as the author left them.
//
// Empty Title or Description means the page offered none; a Nil ImageBlobID
// means the page offered no usable image, or fetching it failed. ImageBlobID
// names a bounded derivative in the blob store — the URL a reader's browser
// is given is minted from it at serialization time and always points at this
// instance's own files origin.
type LinkPreview struct {
	MessageID   uuid.UUID
	URL         string
	Title       string
	Description string
	ImageBlobID uuid.UUID
	FetchedAt   time.Time
}

// saveLinkPreviewQuery upserts one card. Re-enrichment after an edit
// replaces the row rather than adding one — at most one preview per message
// is the primary key's promise, not a rule the caller has to remember.
const saveLinkPreviewQuery = `INSERT INTO link_previews
	    (message_id, url, title, description, image_blob_id, fetched_at)
	VALUES ($1, $2, $3, $4, $5, now())
	ON CONFLICT (message_id) DO UPDATE
	    SET url           = EXCLUDED.url,
	        title         = EXCLUDED.title,
	        description   = EXCLUDED.description,
	        image_blob_id = EXCLUDED.image_blob_id,
	        fetched_at    = EXCLUDED.fetched_at`

// SaveLinkPreview stores, or replaces, the preview of one message.
//
// A message that no longer exists is ErrNotFound rather than a constraint
// error: enrichment runs off the request path, so the message it was started
// for can legitimately be gone by the time the fetch returns, and that is an
// ordinary outcome the caller drops on the floor.
func (s *Store) SaveLinkPreview(ctx context.Context, preview LinkPreview) error {
	_, err := s.pool.Exec(ctx, saveLinkPreviewQuery,
		preview.MessageID,
		preview.URL,
		nullableText(preview.Title),
		nullableText(preview.Description),
		nullableUUID(preview.ImageBlobID),
	)
	if err != nil {
		return fmt.Errorf("save link preview: %w", mapMissingReference(err))
	}
	return nil
}

// DeleteLinkPreview removes a message's card and reports whether there was
// one to remove.
//
// This is the other half of re-enrichment: an edit that takes the last URL
// out of a message must take the card with it, or the reader keeps seeing a
// preview of a link the message no longer contains. The boolean is what
// decides whether that removal is worth announcing.
func (s *Store) DeleteLinkPreview(ctx context.Context, messageID uuid.UUID) (bool, error) {
	var deleted uuid.UUID
	err := s.pool.QueryRow(ctx,
		`DELETE FROM link_previews WHERE message_id = $1 RETURNING message_id`,
		messageID).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("delete link preview: %w", err)
	}
	return true, nil
}

// LinkPreviewsByMessage reads the cards for one page of history in a single
// round trip, keyed by message id.
//
// Batched rather than per-message on purpose: a page is up to fifty
// messages, and fifty queries to decorate one response is the read pattern
// that makes history slow. Messages with no card are simply absent from the
// map — the caller ranges over its messages, not over this.
//
// This carries no visibility rule of its own. A preview is exactly as
// readable as the message it belongs to, so the entitlement question was
// already answered by whatever read those messages; pass only ids from a
// page the caller was allowed to see.
func (s *Store) LinkPreviewsByMessage(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID]LinkPreview, error) {
	previews := make(map[uuid.UUID]LinkPreview, len(messageIDs))
	if len(messageIDs) == 0 {
		return previews, nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT message_id, url, title, description, image_blob_id, fetched_at
		 FROM link_previews
		 WHERE message_id = ANY($1::uuid[])`, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("list link previews: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			preview     LinkPreview
			title       *string
			description *string
			imageBlobID *uuid.UUID
		)
		if scanErr := rows.Scan(
			&preview.MessageID, &preview.URL, &title, &description, &imageBlobID, &preview.FetchedAt,
		); scanErr != nil {
			return nil, fmt.Errorf("scan link preview: %w", scanErr)
		}
		if title != nil {
			preview.Title = *title
		}
		if description != nil {
			preview.Description = *description
		}
		if imageBlobID != nil {
			preview.ImageBlobID = *imageBlobID
		}
		previews[preview.MessageID] = preview
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate link previews: %w", err)
	}
	return previews, nil
}

// nullableText maps Go's empty string onto SQL NULL. The columns are
// nullable because "the page had no description" and "the description is the
// empty string" are different facts, and only the first one happens.
func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableUUID maps uuid.Nil onto SQL NULL, for the same reason.
func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// LinkPreviewImageExists reports whether some message's preview card owns
// this image blob. The files origin asks it as the fallback lookup ADR 003
// settles: a preview derivative has no attachments row on purpose — a row
// would drag it into channel visibility, the orphan sweep and file search,
// none of which apply to a thumbnail the server fetched for a card — so the
// origin resolves attachments first and this second.
func (s *Store) LinkPreviewImageExists(ctx context.Context, blobID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM link_previews WHERE image_blob_id = $1)`, blobID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("link preview image exists: %w", err)
	}
	return exists, nil
}
