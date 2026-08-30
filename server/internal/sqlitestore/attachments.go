package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// attachmentColumns is the canonical column list every attachment query
// selects, in the order scanAttachment expects.
const attachmentColumns = `id, channel_id, uploader_id, message_id, filename, content_type, size_bytes, width, height, has_thumbnail, created_at`

// insertAttachmentQuery records one upload. created_at is bound rather than
// defaulted: PostgreSQL's column carries DEFAULT now(), and the SQLite schema
// has no default, so the value comes from Store.clock.
const insertAttachmentQuery = `INSERT INTO attachments
	    (id, channel_id, uploader_id, filename, content_type, size_bytes, width, height, has_thumbnail, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	RETURNING ` + attachmentColumns

// attachmentByIDQuery reads one attachment by id alone.
const attachmentByIDQuery = `SELECT ` + attachmentColumns + ` FROM attachments WHERE id = ?`

// sweepOrphansQuery deletes the uploads that never became a message and hands
// back their ids so the caller can delete the blobs. The row goes first: a row
// without bytes is a broken card, a blob without a row is disk the next sweep
// cannot even find.
const sweepOrphansQuery = `DELETE FROM attachments
	WHERE message_id IS NULL AND created_at < ?
	RETURNING id`

// CreateAttachment records one uploaded file against its channel and uploader,
// unattached to any message.
//
// A channel that went away between the membership check and this call is
// storage.ErrNotFound, not a constraint error — the same translation every
// messaging write does.
func (s *Store) CreateAttachment(ctx context.Context, na storage.NewAttachment) (storage.Attachment, error) {
	row := s.db.QueryRowContext(ctx, insertAttachmentQuery,
		na.ID, na.ChannelID, na.UploaderID, na.Filename, na.ContentType, na.SizeBytes,
		na.Width, na.Height, boolValue(na.HasThumbnail), s.nowText())
	att, err := scanAttachment(row)
	if err != nil {
		return storage.Attachment{}, fmt.Errorf("create attachment: %w", mapMissingReference(err))
	}
	return att, nil
}

// AttachmentByID reads one attachment by id alone, or storage.ErrNotFound.
//
// It is the one read that is not scoped to a channel, because it serves the
// cookie-less files origin, where there is no caller to scope it to:
// entitlement was settled when the URL was signed, and the signature is the
// credential (ADR 003). Every other read of an attachment goes through the
// message that carries it.
func (s *Store) AttachmentByID(ctx context.Context, id uuid.UUID) (storage.Attachment, error) {
	att, err := scanAttachment(s.db.QueryRowContext(ctx, attachmentByIDQuery, id))
	if err != nil {
		return storage.Attachment{}, fmt.Errorf("attachment by id: %w", err)
	}
	return att, nil
}

// AttachmentsByMessages reads the attachments of many messages at once, grouped
// by message id. It is how a page of history gets its file cards: one query for
// the page, never one per message.
//
// The ids become an IN list rather than PostgreSQL's `= ANY($1::uuid[])`, since
// SQLite has no array type; the empty case returns before any statement is
// built, because `IN ()` is a syntax error and "no messages" has an answer that
// needs no database at all. The list is bounded — a page is at most fifty
// messages, far below SQLite's 32766 bound parameters (msgUUIDList).
//
// Access control is not its job — the caller has already decided who may see
// the messages it is asked about.
func (s *Store) AttachmentsByMessages(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]storage.Attachment, error) {
	byMessage := map[uuid.UUID][]storage.Attachment{}
	if len(messageIDs) == 0 {
		return byMessage, nil
	}

	list, args := msgUUIDList(messageIDs)
	query := `SELECT ` + attachmentColumns + `
		FROM attachments
		WHERE message_id IN (` + list + `)
		ORDER BY created_at, id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("attachments by messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		att, scanErr := scanAttachment(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("attachments by messages: %w", scanErr)
		}
		// message_id is never NULL here: the query filters on it.
		byMessage[*att.MessageID] = append(byMessage[*att.MessageID], att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attachments by messages: %w", err)
	}
	return byMessage, nil
}

// SweepOrphanAttachments deletes the uploads older than age that never reached
// a message, and returns their ids so the caller can delete the blobs behind
// them.
//
// An abandoned composer therefore costs a day of disk rather than forever
// (ADR 003). The returned ids are the caller's to act on: a blob left behind by
// a failed delete is disk nothing will ever reclaim, so the caller logs rather
// than swallows.
//
// The cutoff is computed in Go — SQLite has no interval type — and read from
// Store.clock rather than time.Now, so a test that pins the clock pins the
// sweep too. In production the two are the same call.
func (s *Store) SweepOrphanAttachments(ctx context.Context, age time.Duration) ([]uuid.UUID, error) {
	rows, err := s.db.QueryContext(ctx, sweepOrphansQuery, asTime(s.clock().Add(-age)))
	if err != nil {
		return nil, fmt.Errorf("sweep orphan attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("sweep orphan attachments: %w", scanErr)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep orphan attachments: %w", err)
	}
	return ids, nil
}

// claimAttachments attaches nm.AttachmentIDs to messageID, or reports
// storage.ErrAttachmentNotFound. It runs inside the send's transaction, so the
// message and its files land together or neither does.
//
// The check is a count, not a per-id diagnosis: every way an id can fail — no
// such file, another channel's, another person's, one already attached — is one
// row that did not come back, and the caller must not be able to tell which.
// Returning the ids that did match would leak precisely the fact the single
// error code exists to hide.
//
// PostgreSQL reaches the same count through a locking sub-select:
//
//	SELECT id FROM attachments WHERE id = ANY($ids) AND … ORDER BY id FOR UPDATE
//
// and the ORDER BY is the whole point of writing it that way. Without it
// PostgreSQL locks the matched rows in whatever order the scan yields, and two
// sends claiming an overlapping set of ids could take the same two rows in
// opposite orders — a deadlock. With it the second sender queues behind the
// first, then (READ COMMITTED re-evaluating the predicate against the updated
// row) finds message_id no longer NULL and drops it from the result.
//
// Here there is no row lock and no ordering requirement, because there is
// nothing to order against: two sends cannot be in flight at once — each holds
// the database's write lock for its whole transaction — so no second claim can
// interleave with this one, and a cycle needs two. The short count that fails
// the send comes from the UPDATE's own predicate instead: the second sender
// runs after the first has committed, sees message_id already set, and matches
// nothing. TestConcurrentSendsClaimOneAttachmentOnceIntegration holds unchanged
// — exactly one of eight racing sends claims the file and the rest get
// storage.ErrAttachmentNotFound — and it holds without a deadlock to avoid.
//
// The ids become an IN list because SQLite has no arrays; it is bounded by the
// attachments one message may carry, so the parameter limit is not in reach.
func claimAttachments(ctx context.Context, tx *sql.Tx, messageID uuid.UUID, nm storage.NewMessage) ([]storage.Attachment, error) {
	if len(nm.AttachmentIDs) == 0 {
		// A send with no files claims nothing, and its Attachments stay nil
		// rather than becoming an empty slice — the same shape PostgreSQL
		// returns, where this path is not entered at all.
		return nil, nil
	}

	list, ids := msgUUIDList(nm.AttachmentIDs)
	query := `UPDATE attachments
		SET message_id = ?
		WHERE id IN (` + list + `)
		  AND channel_id = ?
		  AND uploader_id = ?
		  AND message_id IS NULL
		RETURNING ` + attachmentColumns

	args := make([]any, 0, len(ids)+3)
	args = append(args, messageID)
	args = append(args, ids...)
	args = append(args, nm.ChannelID, nm.AuthorID)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	claimed := []storage.Attachment{}
	for rows.Next() {
		att, scanErr := scanAttachment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		claimed = append(claimed, att)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(claimed) != len(nm.AttachmentIDs) {
		return nil, storage.ErrAttachmentNotFound
	}

	// Oldest upload first, matching AttachmentsByMessages, so a message renders
	// its cards in the same order however it was read. The id breaks ties: two
	// files uploaded in the same instant share a created_at, and without it the
	// order would differ between the send and the reload. It is sorted here
	// rather than in the statement because RETURNING takes no ORDER BY.
	slices.SortFunc(claimed, func(a, b storage.Attachment) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	return claimed, nil
}

// loadMessageCards fills everything a client-serving read owes beyond the
// message rows themselves — the file cards and the link-preview card — each as
// one batched query for the whole page, never one per message.
func (s *Store) loadMessageCards(ctx context.Context, msgs []storage.Message) error {
	if err := s.loadAttachments(ctx, msgs); err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
	}
	previews, err := s.LinkPreviewsByMessage(ctx, ids)
	if err != nil {
		return err
	}
	for i := range msgs {
		if preview, ok := previews[msgs[i].ID]; ok {
			p := preview
			msgs[i].Preview = &p
		}
	}
	return nil
}

// loadAttachments fills in the Attachments of every message in msgs, in one
// query for the whole slice.
func (s *Store) loadAttachments(ctx context.Context, msgs []storage.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}

	byMessage, err := s.AttachmentsByMessages(ctx, ids)
	if err != nil {
		return err
	}
	for i := range msgs {
		msgs[i].Attachments = byMessage[msgs[i].ID]
	}
	return nil
}

// scanAttachment scans one attachmentColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound.
func scanAttachment(row rowScanner) (storage.Attachment, error) {
	var (
		a         storage.Attachment
		messageID uuid.NullUUID
		width     sql.NullInt64
		height    sql.NullInt64
	)
	err := row.Scan(
		&a.ID, &a.ChannelID, &a.UploaderID, &messageID, &a.Filename,
		&a.ContentType, &a.SizeBytes, &width, &height, &a.HasThumbnail,
		timeScan{dst: &a.CreatedAt},
	)
	if err != nil {
		return storage.Attachment{}, notFound(err)
	}
	if messageID.Valid {
		id := messageID.UUID
		a.MessageID = &id
	}
	a.Width = attachmentIntPtr(width)
	a.Height = attachmentIntPtr(height)
	return a, nil
}

// attachmentIntPtr decodes a nullable dimension column into the *int the domain
// type models it as. codec.go's int64Ptr covers the *int64 columns; the image
// dimensions are the one place the domain narrows to int, and the values are
// pixel counts a CHECK already bounds above zero.
func attachmentIntPtr(ni sql.NullInt64) *int {
	if !ni.Valid {
		return nil
	}
	v := int(ni.Int64)
	return &v
}
