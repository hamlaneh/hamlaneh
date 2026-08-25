package storage

import (
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
// EditedAt and DeletedAt are read here but never written: this slice sends
// and reads messages, and only editing and deleting (slice 1.2b) set them.
// They are on the struct because history must render a message a later
// slice deleted — see ListMessages on how a deleted row surfaces.
type Message struct {
	ID          uuid.UUID
	ChannelID   uuid.UUID
	Author      MessageAuthor
	ClientMsgID uuid.UUID
	Content     string
	CreatedAt   time.Time
	EditedAt    *time.Time
	DeletedAt   *time.Time
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
}

// MessageCursor is a keyset-pagination position in a channel's history: the
// (created_at, id) of the message a page is anchored on. The anchor itself
// is never repeated in the page it anchors.
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
// Before and After are mutually exclusive: Before pages into the past
// (scrollback), After pages into the future (the reconnect backfill), and
// neither asks for the newest page — the channel as it is opened. Limit
// must be positive; callers own clamping it to the API contract.
type ListMessagesParams struct {
	ChannelID uuid.UUID
	Before    *MessageCursor
	After     *MessageCursor
	Limit     int
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
const messageColumns = `m.id, m.channel_id, m.client_msg_id, m.content, m.created_at, m.edited_at, m.deleted_at, u.id, u.username, u.display_name`

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
	    INSERT INTO messages (channel_id, author_id, client_msg_id, content)
	    VALUES ($1, $2, $3, $4)
	    ON CONFLICT (channel_id, author_id, client_msg_id) DO NOTHING
	    RETURNING id, channel_id, author_id, client_msg_id, content, created_at, edited_at, deleted_at
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
func (s *Store) CreateMessage(ctx context.Context, nm NewMessage) (Message, bool, error) {
	mentioned := parseMentions(nm.Content)

	for range sendAttempts {
		row := s.pool.QueryRow(ctx, insertMessageQuery,
			nm.ChannelID, nm.AuthorID, nm.ClientMsgID, nm.Content, mentioned)
		msg, err := scanMessage(row)
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

// messageByKey reads the message an idempotency key names, or ErrNotFound.
func (s *Store) messageByKey(ctx context.Context, nm NewMessage) (Message, error) {
	row := s.pool.QueryRow(ctx, messageByKeyQuery, nm.ChannelID, nm.AuthorID, nm.ClientMsgID)
	msg, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("message by client key: %w", err)
	}
	return msg, nil
}

// ListMessages returns one page of a channel's history, ascending by
// (created_at, id) whichever direction it was paged in.
//
// With neither cursor it returns the newest Limit messages — the channel as
// it is opened. Before pages into the past, After into the future; both are
// exclusive of the message they name, and asking for both at once is an
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
	if params.Before != nil && params.After != nil {
		return MessagePage{}, errors.New("list messages: before and after are mutually exclusive")
	}

	query, args := messagePageQuery(params)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return MessagePage{}, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		msg, scanErr := scanMessage(rows)
		if scanErr != nil {
			return MessagePage{}, fmt.Errorf("list messages: %w", scanErr)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
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
	return page, nil
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
	var m Message
	err := row.Scan(
		&m.ID, &m.ChannelID, &m.ClientMsgID, &m.Content, &m.CreatedAt, &m.EditedAt, &m.DeletedAt,
		&m.Author.ID, &m.Author.Username, &m.Author.DisplayName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return m, nil
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
