package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// messageColumns is the canonical column list every message query selects, in
// the order scanMessage expects. It is written against the aliases m (the
// message) and u (its author).
const messageColumns = `m.id, m.channel_id, m.client_msg_id, m.content, m.created_at, m.edited_at, m.deleted_at, u.id, u.username, u.display_name, m.mls_epoch, m.mls_ciphertext`

// The PostgreSQL driver also carries a messageReturning list, because each of
// its writes is a data-modifying CTE whose RETURNING has to hand messageColumns
// exactly the columns it names — two lists that must not drift apart.
//
// There is no such list here. SQLite has no data-modifying CTE, so every write
// below states its own two statements inside one transaction: it RETURNS only
// the id it just wrote, then reads the row back through messageByIDQuery, the
// same statement MessageByID uses. One projection, one scan, nothing to drift.

// messageByKeyQuery reads the message an idempotency key already names.
const messageByKeyQuery = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = ? AND m.author_id = ? AND m.client_msg_id = ?`

// messageByIDQuery reads one message of one channel. The channel is part of
// the key rather than a filter applied afterwards — see MessageByID. It is
// also the read-back every write below runs inside its own transaction.
const messageByIDQuery = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = ? AND m.id = ?`

// insertMessageQuery stores a message unless its idempotency key is already
// taken, in which case it stores nothing and returns no row.
//
// id is bound rather than defaulted: PostgreSQL's messages.id carries
// DEFAULT gen_random_uuid(), and the SQLite schema has no such default, so the
// value comes from uuid.New() in insertMessage. created_at is bound for the
// same reason and from Store.clock, which is the one clock in home mode.
//
// It returns the id alone. PostgreSQL joins the author onto the inserted row
// inside the same CTE so that a send costs one round trip; here a round trip is
// a call into a library in this process, so the join is a second statement
// against a row nothing else can touch — the transaction holds the write lock.
const insertMessageQuery = `INSERT INTO messages
	    (id, channel_id, author_id, client_msg_id, content, created_at, mls_epoch, mls_ciphertext)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (channel_id, author_id, client_msg_id) DO NOTHING
	RETURNING id`

// updateMessageQuery replaces a message's content and stamps edited_at, unless
// the message is deleted — a deleted row is a placeholder, and words must not
// reappear on one. edited_at is bound because SQLite has no now().
//
// The mentions it must rewrite are a separate statement here
// (rewriteMessageMentions) rather than two more arms of a CTE; the caller's
// transaction is what keeps them atomic with the edit.
const updateMessageQuery = `UPDATE messages
	SET content = ?, mls_epoch = ?, mls_ciphertext = ?, edited_at = ?
	WHERE channel_id = ? AND id = ? AND deleted_at IS NULL
	RETURNING id`

// softDeleteMessageQuery erases a message's content in place — the plaintext
// column and the ciphertext alike.
//
// The two are erased by one statement rather than by two rules that have to
// agree: a deleted message keeps its place and loses its words in both worlds,
// and there is no path here that clears one and leaves the other. Clearing
// mls_epoch with mls_ciphertext is also what keeps the both-or-neither rule
// satisfied — a rule migration 0017 states as a pair of triggers rather than
// as PostgreSQL's CHECK, because SQLite cannot add a CHECK to an existing
// table. A half-erase would abort with the trigger's RAISE, which errors.go
// classifies alongside a CHECK violation, so the refusal reads the same on
// both drivers.
//
// An empty content together with a non-null deleted_at is the only shape the
// messages_content_shape constraint accepts for a deleted row (migration 0003):
// a delete that erased the text without stamping deleted_at, or stamped it
// without erasing, is refused by the database rather than stored. The
// deleted_at IS NULL guard is what makes a second delete a no-op instead of a
// re-stamp, so the first deleter stays the one on record.
const softDeleteMessageQuery = `UPDATE messages
	SET content = '', mls_epoch = NULL, mls_ciphertext = NULL,
	    deleted_at = ?, deleted_by = ?
	WHERE channel_id = ? AND id = ? AND deleted_at IS NULL
	RETURNING id`

// The three page queries. Each direction gets its own statement instead of one
// statement with optional bounds, because a cursor bound spelled out is a range
// on messages_channel_created_idx while the same bound hidden behind
// `? IS NULL OR ...` degrades into a filter that re-walks the channel from its
// live edge on every page. That is as true of SQLite's planner as of
// PostgreSQL's, so the shape is kept exactly.
//
// What changes is the comparison itself. PostgreSQL writes the cursor as the
// row-value `(m.created_at, m.id) < ($2, $3)`; SQLite has no row values, so
// each is expanded to the two-term OR it means. The expansion is still an
// index range because created_at leads the index and the equality branch pins
// it, and it is still correct against ties because the id breaks them — which
// is the whole reason the cursor is a pair. The timestamp is bound twice, once
// per branch (see msgCursorArgs), and compares as text because codec.go's one
// fixed-width UTC layout makes lexicographic order chronological.
const (
	messagePageSelect = `SELECT ` + messageColumns + `
	FROM messages m JOIN users u ON u.id = m.author_id
	WHERE m.channel_id = ?`

	newestMessagesQuery = messagePageSelect + `
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT ?`

	messagesBeforeQuery = messagePageSelect + `
	  AND (m.created_at < ? OR (m.created_at = ? AND m.id < ?))
	ORDER BY m.created_at DESC, m.id DESC
	LIMIT ?`

	messagesAfterQuery = messagePageSelect + `
	  AND (m.created_at > ? OR (m.created_at = ? AND m.id > ?))
	ORDER BY m.created_at, m.id
	LIMIT ?`

	// The newer half of an `around` page. It is >= rather than >, because the
	// anchor of a permalink belongs in the page it anchors — and an anchor
	// naming no message simply starts the half at the position it would have
	// held. Expanded, the >= lands on the id: a later timestamp qualifies
	// outright, an equal one qualifies from the anchor's own id onwards.
	messagesFromQuery = messagePageSelect + `
	  AND (m.created_at > ? OR (m.created_at = ? AND m.id >= ?))
	ORDER BY m.created_at, m.id
	LIMIT ?`
)

// The two boundary probes: is there anything past the edge of this page? Same
// row-value expansion as the page queries above.
const (
	olderMessageExistsQuery = `SELECT EXISTS (
	    SELECT 1 FROM messages
	    WHERE channel_id = ? AND (created_at < ? OR (created_at = ? AND id < ?)))`

	newerMessageExistsQuery = `SELECT EXISTS (
	    SELECT 1 FROM messages
	    WHERE channel_id = ? AND (created_at > ? OR (created_at = ? AND id > ?)))`
)

// sendAttempts bounds the insert/lookup retry in CreateMessage. A second
// attempt is only ever needed when the row that took the idempotency key is
// gone by the time it is read back; a third would mean that happened again,
// and there is no honest number of attempts past that.
const sendAttempts = 3

// CreateMessage stores a message idempotently and reports whether this call is
// the one that stored it — the difference between the API's 201 and its 200.
//
// The unique index on (channel_id, author_id, client_msg_id) is what makes a
// retry safe: the insert asks for the key and, if the key is taken, does
// nothing and returns no row, and the message that is already there is read
// back and returned unmodified. Content on a resend is deliberately ignored:
// the stored message is the one that exists, and a resend of a message somebody
// has since deleted returns the deleted row rather than resurrecting it.
//
// Concurrent sends of one key serialize twice over here. PostgreSQL relies on
// the unique index alone — the second INSERT waits for the first to commit,
// then finds the key taken — and its retry covers the case where the holder
// rolled back and freed the key again. On SQLite the transactions cannot
// overlap at all: the first holds the database's write lock from BEGIN and the
// second waits for it, so by the time the second insert runs the first has
// either committed (key taken, read it back) or rolled back (key free, insert
// wins). The retry therefore covers only the narrower case of a message whose
// whole channel was deleted in between, and TestCreateMessageConcurrentIntegration
// holds unchanged: exactly one sender reports creating the message, all of them
// return the same row, and the channel holds one message per key.
//
// A channel or author that does not exist (deleted between the authorization
// check and the send) is storage.ErrNotFound, not a constraint error.
//
// Mentions are derived from the content rather than taken from the caller, and
// are written inside the same transaction as the message. Deriving them here
// means the rows the sidebar's "@" badge counts can never disagree with the
// message they came from, whatever calls this; the shared transaction means
// they are atomic with it, and a resend — which reaches nothing but the lookup
// below — cannot write them a second time.
//
// Only members of the channel get a mention row. A token naming somebody who is
// not in the conversation — a stale paste, a hand-typed id, a person since
// removed — is dropped rather than stored or refused, and the same join makes a
// token naming no user at all impossible instead of a foreign-key error, so no
// stray token can fail an otherwise good message. The rule that falls out is
// worth having on its own: no mention row can name someone who was not entitled
// to read the message it points at, which is the row a future cross-channel "my
// mentions" view would otherwise leak. The cost is that somebody mentioned
// before they joined gets no "@" badge when they arrive — they still get the
// message as plain unread, like the rest of the history they missed.
//
// A message with neither text nor files is nothing: storage.ErrEmptyMessage,
// and nothing is stored. The rule is enforced here rather than in the handler
// because only here is it atomic with the claim — the database has allowed
// empty content on a live message since migration 0007, so "at least one
// attachment" is a promise about rows this transaction is still writing.
//
// Attachments are claimed by the same transaction that inserts the message
// (see claimAttachments): all of them, or no message at all. A resend claims
// nothing — it inserts nothing, so there is nothing to attach to, and the ids
// it names stay unattached rather than being stolen from the message that
// already holds them. The stored message's own attachments come back with it
// either way.
func (s *Store) CreateMessage(ctx context.Context, nm storage.NewMessage) (storage.Message, bool, error) {
	// Ciphertext counts as content for this rule. On an e2ee channel the
	// content column is '' by contract, so testing it alone would refuse every
	// encrypted message ever sent.
	if nm.Content == "" && len(nm.AttachmentIDs) == 0 && nm.Mls == nil {
		return storage.Message{}, false, storage.ErrEmptyMessage
	}
	mentioned := storage.ParseMentions(nm.Content)

	for range sendAttempts {
		msg, err := s.insertMessage(ctx, nm, mentioned)
		if err == nil {
			return msg, true, nil
		}
		// storage.ErrNotFound here is the insert returning no row: DO NOTHING
		// fired, so the key is already taken. Everything else is a real failure.
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Message{}, false, fmt.Errorf("create message: %w", mapMissingReference(err))
		}

		existing, lookupErr := s.messageByKey(ctx, nm)
		if lookupErr == nil {
			return existing, false, nil
		}
		if !errors.Is(lookupErr, storage.ErrNotFound) {
			return storage.Message{}, false, fmt.Errorf("create message: %w", lookupErr)
		}
		// The row that took the key is gone — its channel was deleted between
		// the two statements, taking the message with it. The key is free again;
		// try once more, and the insert will fail on the missing channel's
		// foreign key rather than loop.
	}
	return storage.Message{}, false, fmt.Errorf(
		"create message: idempotency key %s still contended after %d attempts",
		nm.ClientMsgID, sendAttempts)
}

// insertMessage stores one message with its mentions and its attachments,
// reporting storage.ErrNotFound when the idempotency key was already taken.
//
// PostgreSQL does the whole thing in ONE statement: insertMessageQuery there is
// a data-modifying CTE whose `inserted` arm writes the message, whose
// `mentioned` arm selects from that arm's RETURNING to write the mention rows,
// and whose final SELECT joins the author on — so a send with no files is a
// single round trip and needs no transaction to be atomic, and a send with
// files opens one only to add the attachment claim.
//
// SQLite has no data-modifying CTE, so those arms become the separate
// statements they always were, and every send opens a transaction. That
// transaction is not a weaker substitute for the CTE: _txlock=immediate takes
// the database's write lock at BEGIN, no second writer can run until this one
// commits or rolls back, and the four statements below therefore land together
// or not at all. The atomicity the CTE bought is exactly the atomicity the
// write lock supplies; what is lost is only the round-trip count, which in a
// single process is calls into a library.
//
// The id is generated here because PostgreSQL's gen_random_uuid() default has
// no SQLite counterpart in the schema.
func (s *Store) insertMessage(ctx context.Context, nm storage.NewMessage, mentioned []uuid.UUID) (storage.Message, error) {
	id := uuid.New()

	var msg storage.Message
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var inserted uuid.UUID
		row := tx.QueryRowContext(ctx, insertMessageQuery,
			id, nm.ChannelID, nm.AuthorID, nm.ClientMsgID, nm.Content, s.nowText(),
			mlsEpochArg(nm.Mls), mlsCiphertextArg(nm.Mls))
		if scanErr := row.Scan(&inserted); scanErr != nil {
			return notFound(scanErr)
		}

		if mentionErr := insertMessageMentions(ctx, tx, inserted, nm.ChannelID, mentioned); mentionErr != nil {
			return mentionErr
		}

		claimed, claimErr := claimAttachments(ctx, tx, inserted, nm)
		if claimErr != nil {
			return claimErr
		}

		stored, readErr := scanMessage(tx.QueryRowContext(ctx, messageByIDQuery, nm.ChannelID, inserted))
		if readErr != nil {
			return readErr
		}
		stored.Attachments = claimed
		msg = stored
		return nil
	})
	if err != nil {
		return storage.Message{}, err
	}
	return msg, nil
}

// insertMessageMentions writes the mention rows a message's content names.
//
// PostgreSQL writes them as the `mentioned` arm of its send CTE and as the
// `added` arm of its edit CTE, both selecting from the arm that wrote the
// message; here they are one statement the caller runs inside its transaction,
// which is what keeps them atomic with the write they belong to.
//
// The join against channel_members carries the whole rule: a mentioned id that
// is not a member of this channel — a stranger, or no user at all — matches
// nothing and simply produces no row, rather than being stored or raising a
// foreign-key error. Repeats among the ids collapse against the channel_members
// primary key, so a name written twice is still one row.
//
// ON CONFLICT DO NOTHING matters only on the edit path, where a mention the
// author kept still has its row: the row survives rewriteMessageMentions'
// delete and this insert declines to touch it, so a kept mention is kept rather
// than churned. A send cannot conflict — nothing else can name a message id
// this transaction has only just written.
func insertMessageMentions(ctx context.Context, tx *sql.Tx, messageID, channelID uuid.UUID, mentioned []uuid.UUID) error {
	if len(mentioned) == 0 {
		return nil
	}
	list, ids := msgUUIDList(mentioned)
	// #nosec G202 -- list is placeholders only (see msgUUIDList); the ids travel in args
	query := `INSERT INTO message_mentions (message_id, mentioned_user_id)
		SELECT ?, cm.user_id
		FROM channel_members cm
		WHERE cm.channel_id = ? AND cm.user_id IN (` + list + `)
		ON CONFLICT DO NOTHING`

	args := make([]any, 0, len(ids)+2)
	args = append(args, messageID, channelID)
	args = append(args, ids...)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return nil
}

// rewriteMessageMentions makes a message's mention rows agree with its edited
// content: the ones the new content no longer names are dropped, the ones it
// adds are written, and the ones it kept are left where they are.
//
// PostgreSQL coalesces the id list in its DELETE, because an edit that names
// nobody sends an empty array which arrives as SQL NULL — and "not one of NULL"
// is NULL rather than true, so every mention row would survive an edit that
// removed the last mention. There is no such hazard here: an empty list is not
// an empty IN list (which SQLite cannot even parse) but a different statement,
// the unconditional delete, which is what "this message mentions nobody" means.
func rewriteMessageMentions(ctx context.Context, tx *sql.Tx, messageID, channelID uuid.UUID, mentioned []uuid.UUID) error {
	query := `DELETE FROM message_mentions WHERE message_id = ?`
	args := []any{messageID}
	if len(mentioned) > 0 {
		list, ids := msgUUIDList(mentioned)
		query += ` AND mentioned_user_id NOT IN (` + list + `)` // #nosec G202 -- list is placeholders only (see msgUUIDList); the ids travel in args
		args = append(args, ids...)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return insertMessageMentions(ctx, tx, messageID, channelID, mentioned)
}

// messageByKey reads the message an idempotency key names, with its
// attachments, or storage.ErrNotFound.
func (s *Store) messageByKey(ctx context.Context, nm storage.NewMessage) (storage.Message, error) {
	row := s.db.QueryRowContext(ctx, messageByKeyQuery, nm.ChannelID, nm.AuthorID, nm.ClientMsgID)
	msg, err := scanMessage(row)
	if err != nil {
		return storage.Message{}, fmt.Errorf("message by client key: %w", err)
	}
	msgs := []storage.Message{msg}
	if err := s.loadMessageCards(ctx, msgs); err != nil {
		return storage.Message{}, fmt.Errorf("message by client key: %w", err)
	}
	return msgs[0], nil
}

// MessageByID reads one message of one channel, or storage.ErrNotFound.
//
// The channel is part of the key rather than a filter the caller is trusted to
// apply: a message id belonging to another conversation must answer exactly
// like one that never existed, or a member of any channel could reach any
// message on the instance by id. A soft-deleted message is returned like any
// other — it exists, and the caller decides what a deleted row means for the
// operation it is about to attempt.
//
// Access control is not this function's job: it says what a channel holds, and
// the handler that asked has already decided who may ask.
func (s *Store) MessageByID(ctx context.Context, channelID, messageID uuid.UUID) (storage.Message, error) {
	row := s.db.QueryRowContext(ctx, messageByIDQuery, channelID, messageID)
	msg, err := scanMessage(row)
	if err != nil {
		return storage.Message{}, fmt.Errorf("message by id: %w", err)
	}
	return msg, nil
}

// UpdateMessageContent replaces a message's content, stamps edited_at, and
// returns the message as it now stands.
//
// The message keeps its id and its created_at, so it keeps its place in
// history: an edit is not a resend. Mentions are re-derived from the new
// content and rewritten inside the same transaction as the edit — migration
// 0003 says the rows are written "when a message is sent or edited", and
// CreateMessage's guarantee that the "@" badge can never disagree with the
// message it came from would otherwise last only until somebody edited.
//
// mls replaces the ciphertext, and a nil one clears it. Both columns move
// together on every edit, so an edit can neither leave stale ciphertext under
// new plaintext nor the reverse — which is the whole reason it is a parameter
// rather than something the statement leaves alone.
//
// storage.ErrNotFound means the edit changed nothing, which is either of two
// things: no such message in this channel, or a message that is deleted. The
// caller has already read the row it is editing — that is where its
// authorization decision came from — so it knows which, and a deletion that
// landed in between is simply the answer a moment later. Content bounds are the
// caller's to enforce; the messages_content_shape constraint is the backstop.
func (s *Store) UpdateMessageContent(ctx context.Context, channelID, messageID uuid.UUID, content string, mls *storage.MessageMls) (storage.Message, error) {
	mentioned := storage.ParseMentions(content)

	var msg storage.Message
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var updated uuid.UUID
		row := tx.QueryRowContext(ctx, updateMessageQuery,
			content, mlsEpochArg(mls), mlsCiphertextArg(mls), s.nowText(), channelID, messageID)
		if scanErr := row.Scan(&updated); scanErr != nil {
			return notFound(scanErr)
		}

		if mentionErr := rewriteMessageMentions(ctx, tx, updated, channelID, mentioned); mentionErr != nil {
			return mentionErr
		}

		stored, readErr := scanMessage(tx.QueryRowContext(ctx, messageByIDQuery, channelID, updated))
		if readErr != nil {
			return readErr
		}
		msg = stored
		return nil
	})
	if err != nil {
		return storage.Message{}, fmt.Errorf("update message: %w", err)
	}

	// An edit changes words, never files: the message keeps the attachments it
	// was sent with, and the response has to carry them or a client that
	// re-renders from it would drop the cards.
	msgs := []storage.Message{msg}
	if err := s.loadMessageCards(ctx, msgs); err != nil {
		return storage.Message{}, fmt.Errorf("update message: %w", err)
	}
	return msgs[0], nil
}

// SoftDeleteMessage erases a message's content in place and records who did it,
// returning the row the "Message removed" placeholder renders from.
//
// The row is never removed: it keeps its id, its author and its created_at, so
// history does not reshape around a deletion and every cursor either side of it
// stays valid.
//
// PostgreSQL erases and reads back in one CTE. Here the two statements run
// inside one transaction, which is what makes the row that comes back the row
// this call produced rather than whatever the next writer left: the write lock
// is held from BEGIN, so nothing can touch the message in between.
//
// storage.ErrNotFound means nothing was deleted by this call — the message is
// not in this channel, or it was already deleted. The caller answers 204 for
// the second either way (deletion is idempotent), and the distinction that
// matters to it is a different one: no row back means no event to announce,
// because whoever deleted it first already announced one.
func (s *Store) SoftDeleteMessage(ctx context.Context, channelID, messageID, deletedBy uuid.UUID) (storage.Message, error) {
	var msg storage.Message
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var deleted uuid.UUID
		row := tx.QueryRowContext(ctx, softDeleteMessageQuery,
			s.nowText(), deletedBy, channelID, messageID)
		if scanErr := row.Scan(&deleted); scanErr != nil {
			return notFound(scanErr)
		}

		stored, readErr := scanMessage(tx.QueryRowContext(ctx, messageByIDQuery, channelID, deleted))
		if readErr != nil {
			return readErr
		}
		msg = stored
		return nil
	})
	if err != nil {
		return storage.Message{}, fmt.Errorf("delete message: %w", err)
	}
	return msg, nil
}

// ListMessages returns one page of a channel's history, ascending by
// (created_at, id) whichever direction it was paged in.
//
// With no cursor it returns the newest Limit messages — the channel as it is
// opened. Before pages into the past, After into the future, and both are
// exclusive of the message they name; Around centres the page on its anchor and
// includes it (see aroundPage). Asking for more than one at once is an error
// rather than a silently-preferred one.
//
// Soft-deleted messages stay in the page, with their content already empty and
// a non-nil DeletedAt. They are not filtered out, for two reasons: the design
// draws a "message removed" placeholder where they were, so history never
// reshapes around a deletion; and a page whose rows are filtered after the
// LIMIT would shrink unpredictably while its cursors still had to come from the
// rows that survived.
//
// Access control is not this function's job: it returns what a channel holds,
// and the caller decides who may ask. An unknown channel is indistinguishable
// from an empty one on purpose — history is a read, not a lookup, and the
// handler has already resolved the channel.
func (s *Store) ListMessages(ctx context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
	if cursors := boolCount(params.Before != nil, params.After != nil, params.Around != nil); cursors > 1 {
		return storage.MessagePage{}, errors.New("list messages: before, after and around are mutually exclusive")
	}
	if params.Around != nil {
		return s.aroundPage(ctx, params)
	}

	query, args := messagePageQuery(params)
	messages, err := s.queryMessages(ctx, query, args...)
	if err != nil {
		return storage.MessagePage{}, fmt.Errorf("list messages: %w", err)
	}

	// The query asked for one row more than the page holds; that extra row is
	// the answer to "is there more this way", and is not part of the page.
	more := len(messages) > params.Limit
	if more {
		messages = messages[:params.Limit]
	}

	page := storage.MessagePage{Messages: messages}
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

	// The far edge of the page has to be asked about separately, since the page
	// was read in one direction only. The newest page needs no probe: it starts
	// at the live edge, so nothing is newer than its last row.
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
		return storage.MessagePage{}, fmt.Errorf("list messages: %w", probeErr)
	}
	if err := s.loadMessageCards(ctx, page.Messages); err != nil {
		return storage.MessagePage{}, fmt.Errorf("list messages: %w", err)
	}
	return page, nil
}

// aroundPage centres a page on its anchor: the anchor message itself, plus
// history either side of it. It is how a copied message link resolves.
//
// THE SPLIT. The anchor takes one of the Limit places and what is left divides
// in two, the odd message going to the older side: Limit/2 older, the rest
// newer. A limit of 1 is therefore the anchor alone, and the default limit of
// 50 is 25 older, the anchor, and 24 newer. The older side gets the odd one
// because a permalink is read from the message upwards — the conversation that
// led to it is the context a reader arrives needing.
//
// THE ENDS OF A CHANNEL. A side holding less history than its share contributes
// what it has, and the other side is NOT widened to compensate: a permalink to
// the third message of a channel returns three messages, not fifty. That is a
// decision, not a shortfall. The page is short exactly when the channel really
// does end there, HasBefore and HasAfter say so, and a client that renders the
// page learns where the anchor sits in its channel from the shape of what it
// got. Topping up from the far side would answer "this is the start of the
// conversation" with a screenful of later messages that say nothing of the
// sort — and would still be short on a channel holding fewer than Limit
// messages in total, so it would not even buy a promise a client could rely on.
//
// Both halves are read in one direction each, so neither needs the boundary
// probe ListMessages runs: the extra row each asks for is the answer.
func (s *Store) aroundPage(ctx context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
	olderShare := params.Limit / 2
	newerShare := params.Limit - 1 - olderShare

	older, err := s.queryMessages(ctx, messagesBeforeQuery,
		msgCursorArgs(params.ChannelID, *params.Around, olderShare+1)...)
	if err != nil {
		return storage.MessagePage{}, fmt.Errorf("list messages around: %w", err)
	}
	page := storage.MessagePage{HasBefore: len(older) > olderShare}
	if page.HasBefore {
		older = older[:olderShare]
	}
	// The older half is read newest-first, like every backwards page.
	slices.Reverse(older)

	// The anchor is part of its own page, so the newer half asks for the anchor,
	// its share, and one row past it.
	newer, err := s.queryMessages(ctx, messagesFromQuery,
		msgCursorArgs(params.ChannelID, *params.Around, newerShare+2)...)
	if err != nil {
		return storage.MessagePage{}, fmt.Errorf("list messages around: %w", err)
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
		return storage.MessagePage{Messages: page.Messages}, nil
	}
	if err := s.loadMessageCards(ctx, page.Messages); err != nil {
		return storage.MessagePage{}, fmt.Errorf("list messages around: %w", err)
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

// queryMessages runs one messageColumns query and scans its whole result. It
// reads outside any transaction: under WAL a reader never blocks the writer and
// is never blocked by it.
func (s *Store) queryMessages(ctx context.Context, query string, args ...any) ([]storage.Message, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	messages := []storage.Message{}
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

// messagePageQuery picks the statement and arguments for one page. It asks for
// one row past the page so ListMessages can tell whether the direction it paged
// in has more history.
func messagePageQuery(params storage.ListMessagesParams) (string, []any) {
	probeLimit := params.Limit + 1
	switch {
	case params.Before != nil:
		return messagesBeforeQuery, msgCursorArgs(params.ChannelID, *params.Before, probeLimit)
	case params.After != nil:
		return messagesAfterQuery, msgCursorArgs(params.ChannelID, *params.After, probeLimit)
	default:
		return newestMessagesQuery, []any{params.ChannelID, probeLimit}
	}
}

// msgCursorArgs binds one expanded keyset comparison. PostgreSQL binds the
// cursor once, as a row value; each of the expansions above names the timestamp
// in both branches of its OR, so it is bound twice — the same value, and
// deliberately so, since a second parameter that could differ from the first
// would be a way to write a comparison that means nothing.
func msgCursorArgs(channelID uuid.UUID, cursor storage.MessageCursor, limit int) []any {
	at := asTime(cursor.CreatedAt)
	return []any{channelID, at, at, cursor.ID, limit}
}

// hasMessageBeyond reports whether the channel holds a message past anchor in
// the direction query asks about. It runs outside the page query and therefore
// against a slightly later snapshot; a message that lands between the two costs
// at most one extra page fetch, which is why this does not need a transaction
// around it.
func (s *Store) hasMessageBeyond(ctx context.Context, query string, channelID uuid.UUID, anchor storage.Message) (bool, error) {
	at := asTime(anchor.CreatedAt)
	var exists bool
	if err := s.db.QueryRowContext(ctx, query, channelID, at, at, anchor.ID).Scan(&exists); err != nil {
		return false, fmt.Errorf("probe for more history: %w", err)
	}
	return exists, nil
}

// scanMessage scans one messageColumns row. sql.ErrNoRows becomes
// storage.ErrNotFound.
func scanMessage(row rowScanner) (storage.Message, error) {
	var (
		m          storage.Message
		mlsEpoch   sql.NullInt64
		ciphertext []byte
	)
	err := row.Scan(
		&m.ID, &m.ChannelID, &m.ClientMsgID, &m.Content,
		timeScan{dst: &m.CreatedAt}, nullTimeScan{dst: &m.EditedAt}, nullTimeScan{dst: &m.DeletedAt},
		&m.Author.ID, &m.Author.Username, &m.Author.DisplayName,
		&mlsEpoch, &ciphertext,
	)
	if err != nil {
		return storage.Message{}, notFound(err)
	}
	// Both columns or neither: the messages_mls_both_or_neither triggers refuse
	// any other shape, so a half-filled pair here would mean they were dropped.
	// Requiring both is what stops that turning into a message serialized with
	// an epoch and no ciphertext.
	if mlsEpoch.Valid && ciphertext != nil {
		m.Mls = &storage.MessageMls{Epoch: mlsEpoch.Int64, Ciphertext: ciphertext}
	}
	return m, nil
}

// mlsEpochArg and mlsCiphertextArg bind the two nullable columns from one
// optional struct. They exist so no call site can bind an epoch without its
// ciphertext: there is one source for both, and it is the same pointer.
//
// The ciphertext goes through nullBytes because a nil []byte reaches SQLite as
// an empty blob rather than as NULL, and an empty blob beside a NULL epoch is
// precisely the half-filled pair the triggers exist to refuse.
func mlsEpochArg(mls *storage.MessageMls) any {
	if mls == nil {
		return nil
	}
	return mls.Epoch
}

func mlsCiphertextArg(mls *storage.MessageMls) any {
	if mls == nil {
		return nil
	}
	return nullBytes(mls.Ciphertext)
}

// msgUUIDList renders the "?, ?, …" of an IN list for ids, together with the
// arguments to bind into it.
//
// PostgreSQL passes each of these sets as one parameter, `= ANY($n::uuid[])`.
// SQLite has neither arrays nor unnest, so the list is spelled out and every
// element is bound on its own. Callers must refuse an empty slice before
// calling: `IN ()` is a syntax error, and an empty input has an answer —
// nothing — that needs no statement at all.
//
// The list is bounded by what can reach it, so it cannot approach SQLite's
// default limit of 32766 bound parameters: a history page is at most fifty
// messages, and a message's mentions are bounded by its 4000-character content
// against a 39-byte token, so fewer than 103 of them.
//
// The returned string is a function of len(ids) and NOTHING else — it is
// "?, ?, ?", never a value. That is what makes it safe to concatenate into a
// statement, and it is why every caller that does so carries a `#nosec G202`:
// gosec sees a non-constant operand joined into SQL and cannot see that the
// operand contains no data. TestMsgUUIDListIsPlaceholdersOnly pins it, so the
// suppressions stay honest if this function is ever changed to interpolate.
func msgUUIDList(ids []uuid.UUID) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", "), args
}

// mapMissingReference translates the foreign-key violations of the messaging
// tables — a channel or an author that is not there — into storage.ErrNotFound,
// so a channel deleted between the authorization check and the send answers 404
// rather than 500. It is the SQLite counterpart of storage.mapMissingReference,
// which reads a pgerrcode where this reads an extended result code.
func mapMissingReference(err error) error {
	if isForeignKeyViolation(err) {
		return storage.ErrNotFound
	}
	return err
}
