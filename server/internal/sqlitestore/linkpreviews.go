package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// saveLinkPreviewQuery upserts one card. Re-enrichment after an edit replaces
// the row rather than adding one — at most one preview per message is the
// primary key's promise, not a rule the caller has to remember.
//
// fetched_at is bound rather than defaulted: PostgreSQL writes now() in the
// VALUES list, and SQLite has no now(), so the value comes from Store.clock.
const saveLinkPreviewQuery = `INSERT INTO link_previews
	    (message_id, url, title, description, image_blob_id, fetched_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (message_id) DO UPDATE
	    SET url           = EXCLUDED.url,
	        title         = EXCLUDED.title,
	        description   = EXCLUDED.description,
	        image_blob_id = EXCLUDED.image_blob_id,
	        fetched_at    = EXCLUDED.fetched_at`

// deleteLinkPreviewQuery removes one card and returns the key it removed, so
// the caller can tell a removal from a no-op.
const deleteLinkPreviewQuery = `DELETE FROM link_previews WHERE message_id = ? RETURNING message_id`

// linkPreviewImageExistsQuery is the files origin's fallback lookup. EXISTS
// yields 0 or 1 in SQLite and scans straight into a bool.
const linkPreviewImageExistsQuery = `SELECT EXISTS (SELECT 1 FROM link_previews WHERE image_blob_id = ?)`

// SaveLinkPreview stores, or replaces, the preview of one message.
//
// A message that no longer exists is storage.ErrNotFound rather than a
// constraint error: enrichment runs off the request path, so the message it was
// started for can legitimately be gone by the time the fetch returns, and that
// is an ordinary outcome the caller drops on the floor.
func (s *Store) SaveLinkPreview(ctx context.Context, preview storage.LinkPreview) error {
	_, err := s.db.ExecContext(ctx, saveLinkPreviewQuery,
		preview.MessageID,
		preview.URL,
		nullableText(preview.Title),
		nullableText(preview.Description),
		nullableUUID(preview.ImageBlobID),
		s.nowText(),
	)
	if err != nil {
		return fmt.Errorf("save link preview: %w", mapMissingReference(err))
	}
	return nil
}

// DeleteLinkPreview removes a message's card and reports whether there was one
// to remove.
//
// This is the other half of re-enrichment: an edit that takes the last URL out
// of a message must take the card with it, or the reader keeps seeing a preview
// of a link the message no longer contains. The boolean is what decides whether
// that removal is worth announcing.
func (s *Store) DeleteLinkPreview(ctx context.Context, messageID uuid.UUID) (bool, error) {
	var deleted uuid.UUID
	err := s.db.QueryRowContext(ctx, deleteLinkPreviewQuery, messageID).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) {
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
// Batched rather than per-message on purpose: a page is up to fifty messages,
// and fifty queries to decorate one response is the read pattern that makes
// history slow. Messages with no card are simply absent from the map — the
// caller ranges over its messages, not over this.
//
// The ids become an IN list rather than PostgreSQL's `= ANY($1::uuid[])`, since
// SQLite has no array type; the empty case returns the empty map before any
// statement is built, because `IN ()` is a syntax error and "no messages" has
// an answer that needs no database at all. A page is at most fifty ids, far
// below SQLite's 32766 bound parameters (msgUUIDList).
//
// This carries no visibility rule of its own. A preview is exactly as readable
// as the message it belongs to, so the entitlement question was already
// answered by whatever read those messages; pass only ids from a page the
// caller was allowed to see.
func (s *Store) LinkPreviewsByMessage(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID]storage.LinkPreview, error) {
	previews := make(map[uuid.UUID]storage.LinkPreview, len(messageIDs))
	if len(messageIDs) == 0 {
		return previews, nil
	}

	list, args := msgUUIDList(messageIDs)
	query := `SELECT message_id, url, title, description, image_blob_id, fetched_at
		FROM link_previews
		WHERE message_id IN (` + list + `)`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list link previews: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			preview     storage.LinkPreview
			title       sql.NullString
			description sql.NullString
			imageBlobID uuid.NullUUID
		)
		if scanErr := rows.Scan(
			&preview.MessageID, &preview.URL, &title, &description, &imageBlobID,
			timeScan{dst: &preview.FetchedAt},
		); scanErr != nil {
			return nil, fmt.Errorf("scan link preview: %w", scanErr)
		}
		preview.Title = title.String
		preview.Description = description.String
		if imageBlobID.Valid {
			preview.ImageBlobID = imageBlobID.UUID
		}
		previews[preview.MessageID] = preview
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate link previews: %w", err)
	}
	return previews, nil
}

// nullableText maps Go's empty string onto SQL NULL. The columns are nullable
// because "the page had no description" and "the description is the empty
// string" are different facts, and only the first one happens. It is also what
// keeps the length CHECKs of migration 0008 satisfiable: they bound a present
// value from 1, so an empty string stored as text would be refused.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableUUID maps uuid.Nil onto SQL NULL, for the same reason.
func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// LinkPreviewImageExists reports whether some message's preview card owns this
// image blob. The files origin asks it as the fallback lookup ADR 003 settles:
// a preview derivative has no attachments row on purpose — a row would drag it
// into channel visibility, the orphan sweep and file search, none of which
// apply to a thumbnail the server fetched for a card — so the origin resolves
// attachments first and this second.
func (s *Store) LinkPreviewImageExists(ctx context.Context, blobID uuid.UUID) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, linkPreviewImageExistsQuery, blobID).Scan(&exists); err != nil {
		return false, fmt.Errorf("link preview image exists: %w", err)
	}
	return exists, nil
}
