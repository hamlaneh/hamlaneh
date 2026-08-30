package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MessageAuthor is the public face of a message's sender — the name row the
// chat shell draws beside a message, and nothing else. It travels with the
// message instead of being looked up per row, because history is read a
// page at a time and a lookup per message would be a page of round trips.
type MessageAuthor struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// Message is a row of the messages table with its author's summary joined
// in.
//
// EditedAt is set by UpdateMessageContent and DeletedAt by
// SoftDeleteMessage; both are nil on a message that has only ever been sent.
// deleted_by is deliberately not here: nothing renders who deleted a message
// before the Phase 1.4 audit log, and a column nobody reads is a column that
// can leak.
type Message struct {
	ID          uuid.UUID
	ChannelID   uuid.UUID
	Author      MessageAuthor
	ClientMsgID uuid.UUID
	Content     string
	CreatedAt   time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
	// Attachments are the message's file cards, oldest upload first. They
	// are filled by the reads that serve a message to a client — a page of
	// history, a send, an edit — in one query for the whole page, never one
	// per message. A read that does not serve a client (MessageByID, which
	// only feeds an authorization decision) leaves them nil.
	Attachments []Attachment
	// Preview is the message's link-preview card, filled by the same
	// client-serving reads (one batched query per page) and set by the
	// enricher before it announces — the announcement's frame is built from
	// this struct, so a preview missing here would make message_updated
	// erase the very card it exists to deliver.
	Preview *LinkPreview
	// Mls is the encrypted body of a message in an e2ee channel, nil
	// everywhere else. Content is '' wherever it is present, which is what
	// keeps the searchable column free of anything the server could read;
	// soft delete clears it exactly as it clears content.
	Mls *MessageMls
}

// MessageMls is a message's ciphertext and the epoch its sender encrypted at.
//
// It is one struct rather than two nullable fields because the two columns
// are present together or not at all (messages_mls_both_or_neither, migration
// 0017): a shape the database refuses should not be a shape Go code can hold,
// or every reader has to test one field and hope for the other.
//
// Ciphertext is opaque. Nothing in this package reads a byte of it, and the
// epoch is a routing hint the sender stated — never a server-verified claim.
type MessageMls struct {
	Epoch      int64
	Ciphertext []byte
}

// NewMessage carries the fields for sending a message. ClientMsgID is the
// sender's idempotency key: generated once per message by the client and
// resent verbatim on every retry (docs/api/ws-protocol.md §5). Validation
// is the handler's job; the database constraints are the backstop.
type NewMessage struct {
	ChannelID   uuid.UUID
	AuthorID    uuid.UUID
	ClientMsgID uuid.UUID
	Content     string
	// AttachmentIDs are files this author already uploaded to this channel
	// and has not attached to anything, claimed atomically with the insert.
	// Any id that is not all three answers ErrAttachmentNotFound and stores
	// no message. Duplicates are the caller's to refuse; here they would
	// simply fail the count.
	AttachmentIDs []uuid.UUID
	// Mls carries the encrypted body on an e2ee channel and is nil
	// elsewhere. Which of the two a channel demands is the handler's
	// decision, made against the channel's own e2ee flag; storage stores
	// what it is given.
	Mls *MessageMls
}

// MessageCursor is a keyset-pagination position in a channel's history: the
// (created_at, id) of the message a page is anchored on. A Before or After
// page never repeats the message it is anchored on; an Around page is the
// exception and includes it, because that message is what the permalink
// points at.
//
// The pair, not the timestamp alone, is the cursor: messages sent in the
// same instant share a created_at, and a timestamp-only cursor either skips
// the rest of that run or returns it again forever.
type MessageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListMessagesParams control one page of ListMessages.
//
// Before, After and Around are mutually exclusive: Before pages into the
// past (scrollback), After pages into the future (the reconnect backfill),
// Around centres a page on the cursor (a permalink), and none of them asks
// for the newest page — the channel as it is opened. Limit must be positive;
// callers own clamping it to the API contract.
type ListMessagesParams struct {
	ChannelID uuid.UUID
	Before    *MessageCursor
	After     *MessageCursor
	// Around is the one cursor whose own message is part of the page it
	// anchors: a permalink that did not include the message it points at
	// would send the reader to a page the message is missing from.
	Around *MessageCursor
	Limit  int
}

// MessagePage is one page of a channel's history.
type MessagePage struct {
	// Messages ascend by (created_at, id) whichever direction the page was
	// read in, so the caller renders them in reading order unchanged.
	Messages []Message
	// HasBefore reports that older messages exist before the first message
	// of this page, HasAfter that newer ones exist after the last. They are
	// what tells the API layer whether to hand the client a cursor in that
	// direction. Both are false for an empty page: there is no row left to
	// anchor a cursor on.
	HasBefore bool
	HasAfter  bool
}

// messageColumns is the canonical column list every message query selects,
// in the order scanMessage expects. It is written against the aliases m
// (the message) and u (its author).
const messageColumns = `m.id, m.channel_id, m.client_msg_id, m.content, m.created_at, m.edited_at, m.deleted_at, u.id, u.username, u.display_name, m.mls_epoch, m.mls_ciphertext`

// messageReturning is what every data-modifying CTE below has to hand its
// SELECT. It exists so the RETURNING lists cannot drift from messageColumns:
// a column added to one and forgotten in the others is a compile-time error
// nowhere and a scan failure at runtime, which is exactly the shape the
// mls columns would have arrived in.
const messageReturning = `id, channel_id, author_id, client_msg_id, content, created_at, edited_at, deleted_at, mls_epoch, mls_ciphertext`

// insertMessageQuery stores a message unless its idempotency key is already
// taken, in which case it stores nothing and returns no row. The author is
// joined onto the inserted row so a send costs one round trip.
//
// The mentions of the message go in with it. Their insert selects from the
// message's own RETURNING, so it writes exactly when a message row is created
// and nothing when the key was taken; the join against channel_members is
// what keeps a token naming a stranger — or nobody at all — from becoming a
// row (see CreateMessage). $5 is the parsed ids, and repeats among them
// collapse against the channel_members primary key.
const insertMessageQuery = `WITH inserted AS (
	    INSERT INTO messages (channel_id, author_id, client_msg_id, content, mls_epoch, mls_ciphertext)
	    VALUES ($1, $2, $3, $4, $6, $7)
	    ON CONFLICT (channel_id, author_id, client_msg_id) DO NOTHING
	    RETURNING ` + messageReturning + `
	), mentioned AS (
	    INSERT INTO message_mentions (message_id, mentioned_user_id)
	    SELECT i.id, cm.user_id
	    FROM inserted i
	    JOIN channel_members cm
	      ON cm.channel_id = i.channel_id AND cm.user_id = ANY($5::uuid[])
	)
	SELECT ` + messageColumns + `
	FROM inserted m JOIN users u ON u.id = m.author_id`

// messageByKeyQuery reads the message an idempotency key already names.
const messageByKeyQuery = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = $1 AND m.author_id = $2 AND m.client_msg_id = $3`

// messageByIDQuery reads one message of one channel. The channel is part of
// the key rather than a filter applied afterwards — see MessageByID.
const messageByIDQuery = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = $1 AND m.id = $2`

// updateMessageQuery replaces a message's content and stamps edited_at,
// unless the message is deleted — a deleted row is a placeholder, and words
// must not reappear on one.
//
// The mentions are rewritten by the same statement, because migration 0003
// says the rows are written "when a message is sent or edited" and because
// CreateMessage's guarantee — the rows the sidebar's "@" badge counts can
// never disagree with the message they came from — would otherwise last only
// until somebody edited. $4 is the ids parsed from the new content: the
// DELETE drops the rows it no longer names, and the INSERT adds the ones it
// does, joined against channel_members exactly as a send is, so an edit can
// no more mention a stranger to the channel than a send can. The two touch
// disjoint sets of rows, so they cannot race each other inside the one
// statement.
//
// $4 is coalesced in the DELETE because an edit that names nobody sends an
// empty list, which arrives as SQL NULL: "not one of NULL" is NULL rather
// than true, and every mention row would survive an edit that removed the
// last mention. The INSERT needs no such guard — its join simply matches
// nothing.
const updateMessageQuery = `WITH updated AS (
	    UPDATE messages
	    SET content = $3, mls_epoch = $5, mls_ciphertext = $6, edited_at = now()
	    WHERE channel_id = $1 AND id = $2 AND deleted_at IS NULL
	    RETURNING ` + messageReturning + `
	), dropped AS (
	    DELETE FROM message_mentions
	    WHERE message_id IN (SELECT id FROM updated)
	      AND mentioned_user_id <> ALL(coalesce($4::uuid[], '{}'::uuid[]))
	), added AS (
	    INSERT INTO message_mentions (message_id, mentioned_user_id)
	    SELECT up.id, cm.user_id
	    FROM updated up
	    JOIN channel_members cm
	      ON cm.channel_id = up.channel_id AND cm.user_id = ANY($4::uuid[])
	    ON CONFLICT DO NOTHING
	)
	SELECT ` + messageColumns + `
	FROM updated m JOIN users u ON u.id = m.author_id`

// softDeleteMessageQuery erases a message's content in place — the plaintext
// column and the ciphertext alike.
//
// The two are erased by one statement rather than by two rules that have to
// agree: a deleted message keeps its place and loses its words in both
// worlds, and there is no path here that clears one and leaves the other.
// Clearing mls_epoch with mls_ciphertext is also what keeps
// messages_mls_both_or_neither satisfied, so the constraint would refuse a
// half-erase rather than store one.
//
// An empty content together with a non-null deleted_at is the only shape
// the messages_content_shape constraint accepts for a deleted row (migration
// 0003): a delete that erased the text without stamping deleted_at, or
// stamped it without erasing, is refused by the database rather than stored.
// The deleted_at IS NULL guard is what makes a second delete a no-op instead
// of a re-stamp, so the first deleter stays the one on record.
const softDeleteMessageQuery = `WITH deleted AS (
	    UPDATE messages
	    SET content = '', mls_epoch = NULL, mls_ciphertext = NULL,
	        deleted_at = now(), deleted_by = $3
	    WHERE channel_id = $1 AND id = $2 AND deleted_at IS NULL
	    RETURNING ` + messageReturning + `
	)
	SELECT ` + messageColumns + `
	FROM deleted m JOIN users u ON u.id = m.author_id`

// The three page queries. Each direction gets its own statement instead of
// one statement with optional bounds, because a cursor bound written as
// `(created_at, id) < ($2, $3)` is a range on messages_channel_created_idx
// while the same bound hidden behind `$2 IS NULL OR ...` degrades into a
// filter that re-walks the channel from its live edge on every page.
const (
	messagePageSelect = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = $1`

	newestMessagesQuery = messagePageSelect + `
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT $2`

	messagesBeforeQuery = messagePageSelect + `
	  AND (m.created_at, m.id) < ($2::timestamptz, $3::uuid)
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT $4`

	messagesAfterQuery = messagePageSelect + `
	  AND (m.created_at, m.id) > ($2::timestamptz, $3::uuid)
	ORDER BY m.created_at, m.id
	LIMIT $4`

	// The newer half of an `around` page. It is >= rather than >, because
	// the anchor of a permalink belongs in the page it anchors — and an
	// anchor naming no message simply starts the half at the position it
	// would have held.
	messagesFromQuery = messagePageSelect + `
	  AND (m.created_at, m.id) >= ($2::timestamptz, $3::uuid)
	ORDER BY m.created_at, m.id
	LIMIT $4`
)

// The two boundary probes: is there anything past the edge of this page?
const (
	olderMessageExistsQuery = `SELECT EXISTS (
	    SELECT 1 FROM messages
	    WHERE channel_id = $1 AND (created_at, id) < ($2::timestamptz, $3::uuid))`

	newerMessageExistsQuery = `SELECT EXISTS (
	    SELECT 1 FROM messages
	    WHERE channel_id = $1 AND (created_at, id) > ($2::timestamptz, $3::uuid))`
)

// sendAttempts bounds the insert/lookup retry in CreateMessage. A second
// attempt is only ever needed when a concurrent sender won the unique index
// and then rolled back; a third would mean yet another sender did the same
// thing again, and there is no honest number of attempts past that.
const sendAttempts = 3

// CreateMessage stores a message idempotently and reports whether this call
// is the one that stored it — the difference between the API's 201 and its
// 200.
//
// The unique index on (channel_id, author_id, client_msg_id) is what makes
// a retry safe: the insert asks for the key and, if the key is taken, does
// nothing and returns no row, and the message that is already there is read
// back and returned unmodified. Content on a resend is deliberately
// ignored: the stored message is the one that exists, and a resend of a
// message somebody has since deleted returns the deleted row rather than
// resurrecting it.
//
// Concurrent sends of one key serialize on the index rather than on any
// lock this package takes: the second INSERT waits for the first to commit,
// then finds the key taken and reads the committed row. The retry covers
// the one remaining case — the transaction holding the key rolled back, so
// there is nothing to read and the key is free again.
//
// A channel or author that does not exist (deleted between the
// authorization check and the send) is ErrNotFound, not a constraint error.
//
// Mentions are derived from the content rather than taken from the caller,
// and are written by the same statement as the message. Deriving them here
// means the rows the sidebar's "@" badge counts can never disagree with the
// message they came from, whatever calls this; one statement means no
// transaction is needed for them to be atomic with it, and a resend — which
// reaches nothing but the lookup below — cannot write them a second time.
//
// Only members of the channel get a mention row. A token naming somebody who
// is not in the conversation — a stale paste, a hand-typed id, a person since
// removed — is dropped rather than stored or refused, and the same join makes
// a token naming no user at all impossible instead of a foreign-key error, so
// no stray token can fail an otherwise good message. The rule that falls out
// is worth having on its own: no mention row can name someone who was not
// entitled to read the message it points at, which is the row a future
// cross-channel "my mentions" view would otherwise leak. The cost is that
// somebody mentioned before they joined gets no "@" badge when they arrive —
// they still get the message as plain unread, like the rest of the history
// they missed.
// A message with neither text nor files is nothing: ErrEmptyMessage, and
// nothing is stored. The rule is enforced here rather than in the handler
// because only here is it atomic with the claim — the database has allowed
// empty content on a live message since migration 0007, so "at least one
// attachment" is a promise about rows this transaction is still writing.
//
// Attachments are claimed by the same transaction that inserts the message
// (see claimAttachmentsQuery): all of them, or no message at all. A resend
// claims nothing — it inserts nothing, so there is nothing to attach to, and
// the ids it names stay unattached rather than being stolen from the message
// that already holds them. The stored message's own attachments come back
// with it either way.
func (s *Store) CreateMessage(ctx context.Context, nm NewMessage) (Message, bool, error) {
	// Ciphertext counts as content for this rule. On an e2ee channel the
	// content column is '' by contract, so testing it alone would refuse
	// every encrypted message ever sent.
	if nm.Content == "" && len(nm.AttachmentIDs) == 0 && nm.Mls == nil {
		return Message{}, false, ErrEmptyMessage
	}
	mentioned := ParseMentions(nm.Content)

	for range sendAttempts {
		msg, err := s.insertMessage(ctx, nm, mentioned)
		if err == nil {
			return msg, true, nil
		}
		// ErrNotFound here is the insert returning no row: DO NOTHING fired,
		// so the key is already taken. Everything else is a real failure.
		if !errors.Is(err, ErrNotFound) {
			return Message{}, false, fmt.Errorf("create message: %w", mapMissingReference(err))
		}

		existing, lookupErr := s.messageByKey(ctx, nm)
		if lookupErr == nil {
			return existing, false, nil
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return Message{}, false, fmt.Errorf("create message: %w", lookupErr)
		}
		// The row that took the key is gone: its transaction rolled back
		// after our insert deferred to it. The key is free — try again.
	}
	return Message{}, false, fmt.Errorf(
		"create message: idempotency key %s still contended after %d attempts",
		nm.ClientMsgID, sendAttempts)
}

// insertMessage stores one message and claims its attachments, reporting
// ErrNotFound when the idempotency key was already taken.
//
// A send with no files stays a single statement: a transaction there would
// be two extra round trips on the hottest write path with nothing to make
// atomic. A send with files needs both statements to succeed or neither, so
// it takes one — messages first, then attachments, which is the package's
// declared lock order (see the note at the top of storage.go).
func (s *Store) insertMessage(ctx context.Context, nm NewMessage, mentioned []uuid.UUID) (Message, error) {
	if len(nm.AttachmentIDs) == 0 {
		return scanMessage(s.pool.QueryRow(ctx, insertMessageQuery,
			nm.ChannelID, nm.AuthorID, nm.ClientMsgID, nm.Content, mentioned,
			mlsEpochArg(nm.Mls), mlsCiphertextArg(nm.Mls)))
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, err
	}
	defer func() {
		// Rollback after a successful Commit is a no-op that returns
		// pgx.ErrTxClosed; nothing else here is worth reporting over the
		// error the function is already returning.
		_ = tx.Rollback(ctx)
	}()

	msg, err := scanMessage(tx.QueryRow(ctx, insertMessageQuery,
		nm.ChannelID, nm.AuthorID, nm.ClientMsgID, nm.Content, mentioned,
		mlsEpochArg(nm.Mls), mlsCiphertextArg(nm.Mls)))
	if err != nil {
		return Message{}, err
	}

	msg.Attachments, err = claimAttachments(ctx, tx, msg.ID, nm)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// claimAttachments attaches nm.AttachmentIDs to messageID, or reports
// ErrAttachmentNotFound.
//
// The check is a count, not a per-id diagnosis: every way an id can fail —
// no such file, another channel's, another person's, one already attached —
// is one row that did not come back, and the caller must not be able to tell
// which. Returning the ids that did match would leak precisely the fact the
// single error code exists to hide.
func claimAttachments(ctx context.Context, tx pgx.Tx, messageID uuid.UUID, nm NewMessage) ([]Attachment, error) {
	rows, err := tx.Query(ctx, claimAttachmentsQuery,
		messageID, nm.AttachmentIDs, nm.ChannelID, nm.AuthorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	claimed := []Attachment{}
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
		return nil, ErrAttachmentNotFound
	}

	// Oldest upload first, matching attachmentsByMessagesQuery, so a message
	// renders its cards in the same order however it was read. The id breaks
	// ties: two files uploaded in the same instant share a created_at, and
	// without it the order would differ between the send and the reload.
	slices.SortFunc(claimed, func(a, b Attachment) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	return claimed, nil
}

// messageByKey reads the message an idempotency key names, with its
// attachments, or ErrNotFound.
func (s *Store) messageByKey(ctx context.Context, nm NewMessage) (Message, error) {
	row := s.pool.QueryRow(ctx, messageByKeyQuery, nm.ChannelID, nm.AuthorID, nm.ClientMsgID)
	msg, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("message by client key: %w", err)
	}
	msgs := []Message{msg}
	if err := s.loadMessageCards(ctx, msgs); err != nil {
		return Message{}, fmt.Errorf("message by client key: %w", err)
	}
	return msgs[0], nil
}

// MessageByID reads one message of one channel, or ErrNotFound.
//
// The channel is part of the key rather than a filter the caller is trusted
// to apply: a message id belonging to another conversation must answer
// exactly like one that never existed, or a member of any channel could
// reach any message on the instance by id. A soft-deleted message is
// returned like any other — it exists, and the caller decides what a
// deleted row means for the operation it is about to attempt.
//
// Access control is not this function's job: it says what a channel holds,
// and the handler that asked has already decided who may ask.
func (s *Store) MessageByID(ctx context.Context, channelID, messageID uuid.UUID) (Message, error) {
	row := s.pool.QueryRow(ctx, messageByIDQuery, channelID, messageID)
	msg, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("message by id: %w", err)
	}
	return msg, nil
}

// UpdateMessageContent replaces a message's content, stamps edited_at, and
// returns the message as it now stands.
//
// The message keeps its id and its created_at, so it keeps its place in
// history: an edit is not a resend. Mentions are re-derived from the new
// content by the same statement (see updateMessageQuery).
//
// mls replaces the ciphertext, and a nil one clears it. Both columns move
// together on every edit, so an edit can neither leave stale ciphertext under
// new plaintext nor the reverse — which is the whole reason it is a parameter
// rather than something the statement leaves alone.
//
// ErrNotFound means the edit changed nothing, which is either of two
// things: no such message in this channel, or a message that is deleted. The
// caller has already read the row it is editing — that is where its
// authorization decision came from — so it knows which, and a deletion that
// landed in between is simply the answer a moment later. Content bounds are
// the caller's to enforce; the messages_content_shape constraint is the
// backstop.
func (s *Store) UpdateMessageContent(ctx context.Context, channelID, messageID uuid.UUID, content string, mls *MessageMls) (Message, error) {
	row := s.pool.QueryRow(ctx, updateMessageQuery, channelID, messageID, content, ParseMentions(content),
		mlsEpochArg(mls), mlsCiphertextArg(mls))
	msg, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("update message: %w", err)
	}
	// An edit changes words, never files: the message keeps the attachments
	// it was sent with, and the response has to carry them or a client that
	// re-renders from it would drop the cards.
	msgs := []Message{msg}
	if err := s.loadMessageCards(ctx, msgs); err != nil {
		return Message{}, fmt.Errorf("update message: %w", err)
	}
	return msgs[0], nil
}

// SoftDeleteMessage erases a message's content in place and records who did
// it, returning the row the "Message removed" placeholder renders from.
//
// The row is never removed: it keeps its id, its author and its created_at,
// so history does not reshape around a deletion and every cursor either side
// of it stays valid.
//
// ErrNotFound means nothing was deleted by this call — the message is not in
// this channel, or it was already deleted. The caller answers 204 for the
// second either way (deletion is idempotent), and the distinction that
// matters to it is a different one: no row back means no event to announce,
// because whoever deleted it first already announced one.
func (s *Store) SoftDeleteMessage(ctx context.Context, channelID, messageID, deletedBy uuid.UUID) (Message, error) {
	row := s.pool.QueryRow(ctx, softDeleteMessageQuery, channelID, messageID, deletedBy)
	msg, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("delete message: %w", err)
	}
	return msg, nil
}

// ListMessages returns one page of a channel's history, ascending by
// (created_at, id) whichever direction it was paged in.
//
// With no cursor it returns the newest Limit messages — the channel as it is
// opened. Before pages into the past, After into the future, and both are
// exclusive of the message they name; Around centres the page on its anchor
// and includes it (see aroundPage). Asking for more than one at once is an
// error rather than a silently-preferred one.
//
// Soft-deleted messages stay in the page, with their content already empty
// and a non-nil DeletedAt. They are not filtered out, for two reasons: the
// design draws a "message removed" placeholder where they were, so history
// never reshapes around a deletion; and a page whose rows are filtered
// after the LIMIT would shrink unpredictably while its cursors still had to
// come from the rows that survived.
//
// Access control is not this function's job: it returns what a channel
// holds, and the caller decides who may ask. An unknown channel is
// indistinguishable from an empty one on purpose — history is a read, not a
// lookup, and the handler has already resolved the channel.
func (s *Store) ListMessages(ctx context.Context, params ListMessagesParams) (MessagePage, error) {
	if cursors := boolCount(params.Before != nil, params.After != nil, params.Around != nil); cursors > 1 {
		return MessagePage{}, errors.New("list messages: before, after and around are mutually exclusive")
	}
	if params.Around != nil {
		return s.aroundPage(ctx, params)
	}

	query, args := messagePageQuery(params)
	messages, err := s.queryMessages(ctx, query, args...)
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", err)
	}

	// The query asked for one row more than the page holds; that extra row
	// is the answer to "is there more this way", and is not part of the page.
	more := len(messages) > params.Limit
	if more {
		messages = messages[:params.Limit]
	}

	page := MessagePage{Messages: messages}
	if params.After == nil {
		// The newest page and a `before` page are both read newest-first.
		slices.Reverse(page.Messages)
		page.HasBefore = more
	} else {
		page.HasAfter = more
	}
	if len(page.Messages) == 0 {
		return page, nil
	}

	// The far edge of the page has to be asked about separately, since the
	// page was read in one direction only. The newest page needs no probe:
	// it starts at the live edge, so nothing is newer than its last row.
	var probeErr error
	switch {
	case params.Before != nil:
		newest := page.Messages[len(page.Messages)-1]
		page.HasAfter, probeErr = s.hasMessageBeyond(ctx, newerMessageExistsQuery, params.ChannelID, newest)
	case params.After != nil:
		oldest := page.Messages[0]
		page.HasBefore, probeErr = s.hasMessageBeyond(ctx, olderMessageExistsQuery, params.ChannelID, oldest)
	}
	if probeErr != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", probeErr)
	}
	if err := s.loadMessageCards(ctx, page.Messages); err != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", err)
	}
	return page, nil
}

// aroundPage centres a page on its anchor: the anchor message itself, plus
// history either side of it. It is how a copied message link resolves.
//
// THE SPLIT. The anchor takes one of the Limit places and what is left
// divides in two, the odd message going to the older side: Limit/2 older,
// the rest newer. A limit of 1 is therefore the anchor alone, and the
// default limit of 50 is 25 older, the anchor, and 24 newer. The older side
// gets the odd one because a permalink is read from the message upwards —
// the conversation that led to it is the context a reader arrives needing.
//
// THE ENDS OF A CHANNEL. A side holding less history than its share
// contributes what it has, and the other side is NOT widened to compensate:
// a permalink to the third message of a channel returns three messages, not
// fifty. That is a decision, not a shortfall. The page is short exactly when
// the channel really does end there, HasBefore and HasAfter say so, and a
// client that renders the page learns where the anchor sits in its channel
// from the shape of what it got. Topping up from the far side would answer
// "this is the start of the conversation" with a screenful of later messages
// that say nothing of the sort — and would still be short on a channel
// holding fewer than Limit messages in total, so it would not even buy a
// promise a client could rely on.
//
// Both halves are read in one direction each, so neither needs the boundary
// probe ListMessages runs: the extra row each asks for is the answer.
func (s *Store) aroundPage(ctx context.Context, params ListMessagesParams) (MessagePage, error) {
	olderShare := params.Limit / 2
	newerShare := params.Limit - 1 - olderShare

	older, err := s.queryMessages(ctx, messagesBeforeQuery,
		params.ChannelID, params.Around.CreatedAt, params.Around.ID, olderShare+1)
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages around: %w", err)
	}
	page := MessagePage{HasBefore: len(older) > olderShare}
	if page.HasBefore {
		older = older[:olderShare]
	}
	// The older half is read newest-first, like every backwards page.
	slices.Reverse(older)

	// The anchor is part of its own page, so the newer half asks for the
	// anchor, its share, and one row past it.
	newer, err := s.queryMessages(ctx, messagesFromQuery,
		params.ChannelID, params.Around.CreatedAt, params.Around.ID, newerShare+2)
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages around: %w", err)
	}
	page.HasAfter = len(newer) > newerShare+1
	if page.HasAfter {
		newer = newer[:newerShare+1]
	}

	page.Messages = append(older, newer...)
	if len(page.Messages) == 0 {
		// An anchor with nothing on either side of it. Both flags go back to
		// false: an empty page has no row to hang a cursor on, so a client
		// offered one could not use it.
		return MessagePage{Messages: page.Messages}, nil
	}
	if err := s.loadMessageCards(ctx, page.Messages); err != nil {
		return MessagePage{}, fmt.Errorf("list messages around: %w", err)
	}
	return page, nil
}

// boolCount counts how many of its arguments are true.
func boolCount(flags ...bool) int {
	n := 0
	for _, flag := range flags {
		if flag {
			n++
		}
	}
	return n
}

// queryMessages runs one messageColumns query and scans its whole result.
func (s *Store) queryMessages(ctx context.Context, query string, args ...any) ([]Message, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		msg, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// messagePageQuery picks the statement and arguments for one page. It asks
// for one row past the page so ListMessages can tell whether the direction
// it paged in has more history.
func messagePageQuery(params ListMessagesParams) (string, []any) {
	probeLimit := params.Limit + 1
	switch {
	case params.Before != nil:
		return messagesBeforeQuery,
			[]any{params.ChannelID, params.Before.CreatedAt, params.Before.ID, probeLimit}
	case params.After != nil:
		return messagesAfterQuery,
			[]any{params.ChannelID, params.After.CreatedAt, params.After.ID, probeLimit}
	default:
		return newestMessagesQuery, []any{params.ChannelID, probeLimit}
	}
}

// hasMessageBeyond reports whether the channel holds a message past anchor
// in the direction query asks about. It runs outside the page query and
// therefore against a slightly later snapshot; a message that lands between
// the two costs at most one extra page fetch, which is why this does not
// need a transaction around it.
func (s *Store) hasMessageBeyond(ctx context.Context, query string, channelID uuid.UUID, anchor Message) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, query, channelID, anchor.CreatedAt, anchor.ID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("probe for more history: %w", err)
	}
	return exists, nil
}

// scanMessage scans one messageColumns row. pgx.ErrNoRows becomes
// ErrNotFound.
func scanMessage(row pgx.Row) (Message, error) {
	var (
		m          Message
		mlsEpoch   *int64
		ciphertext []byte
	)
	err := row.Scan(
		&m.ID, &m.ChannelID, &m.ClientMsgID, &m.Content, &m.CreatedAt, &m.EditedAt, &m.DeletedAt,
		&m.Author.ID, &m.Author.Username, &m.Author.DisplayName,
		&mlsEpoch, &ciphertext,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	// Both columns or neither: messages_mls_both_or_neither refuses any other
	// shape, so a half-filled pair here would mean the constraint was
	// dropped. Requiring both is what stops that turning into a message
	// serialized with an epoch and no ciphertext.
	if mlsEpoch != nil && ciphertext != nil {
		m.Mls = &MessageMls{Epoch: *mlsEpoch, Ciphertext: ciphertext}
	}
	return m, nil
}

// mlsEpochArg and mlsCiphertextArg bind the two nullable columns from one
// optional struct. They exist so no call site can bind an epoch without its
// ciphertext: there is one source for both, and it is the same pointer.
func mlsEpochArg(mls *MessageMls) *int64 {
	if mls == nil {
		return nil
	}
	epoch := mls.Epoch
	return &epoch
}

func mlsCiphertextArg(mls *MessageMls) []byte {
	if mls == nil {
		return nil
	}
	return mls.Ciphertext
}

// mapMissingReference translates the foreign-key violations of the messages
// table — a channel or an author that is not there — into ErrNotFound, so a
// channel deleted between the authorization check and the send answers 404
// rather than 500.
func mapMissingReference(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return ErrNotFound
	}
	return err
}
