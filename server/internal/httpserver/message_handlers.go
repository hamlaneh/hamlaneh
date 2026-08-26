package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Message history, sending, editing, deleting, and read positions.
// Everything these handlers share — how a channel is resolved, why a
// non-member gets 404, how cursors are encoded — is documented at the top of
// channel_handlers.go.
//
// The two moderation answers are not interchangeable and the contract names
// them separately: editing is author-only, admins included, because
// rewriting somebody else's words is impersonation; deleting is the author
// or an admin who is a member of the channel, because deletion removes words
// rather than putting new ones in somebody's mouth. Both decisions are
// authz's (authz.MessageEdit, authz.MessageDelete); the handlers here only
// load the facts and render the refusal.

// maxContentLen is the contract's message bound, in characters.
const maxContentLen = 4000

// messageWriter is the storage half of editing and soft-deleting a message.
//
// It is declared here rather than added to Store for the reason
// messageSearcher is (search_handler.go): slice 1.2b touches no shared file,
// and the orchestrator folds both into Store when the branches meet.
// *storage.Store satisfies it, checked below, so production can never take
// the fail-closed path in resolveMessage.
type messageWriter interface {
	MessageByID(ctx context.Context, channelID, messageID uuid.UUID) (storage.Message, error)
	UpdateMessageContent(ctx context.Context, channelID, messageID uuid.UUID, content string) (storage.Message, error)
	SoftDeleteMessage(ctx context.Context, channelID, messageID, deletedBy uuid.UUID) (storage.Message, error)
}

var _ messageWriter = (*storage.Store)(nil)

// ListMessages returns one page of a channel's history, always ascending by
// (created_at, id) whichever direction it was paged in. Soft-deleted
// messages keep their place with empty content and a deleted_at, because
// the design draws a placeholder where they were.
func (s *apiServer) ListMessages(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, params api.ListMessagesParams) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelRead, sc.resource()) {
		sc.deny(w, r)
		return
	}

	limit, ok := pageLimit(w, r, params.Limit, defaultListLimit, maxListLimit)
	if !ok {
		return
	}
	query, ok := messagePageQuery(w, r, channelID, limit, params)
	if !ok {
		return
	}

	page, err := sc.store.ListMessages(r.Context(), query)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiMessagePage(page))
}

// messagePageQuery turns the three page cursors into one storage query.
//
// At most one of before, after and around may be given; two is a 400 rather
// than a silently preferred one. `around` centres the page on its anchor and
// includes it — it is how a copied message link resolves, and how the limit
// splits either side of the anchor is storage's decision (see
// storage.aroundPage), including what happens when the anchor sits near
// either end of the channel.
func messagePageQuery(w http.ResponseWriter, r *http.Request, channelID uuid.UUID, limit int, params api.ListMessagesParams) (storage.ListMessagesParams, bool) {
	query := storage.ListMessagesParams{ChannelID: channelID, Limit: limit}

	given := 0
	for _, cursor := range []*string{params.Before, params.After, params.Around} {
		if cursor != nil {
			given++
		}
	}
	if given > 1 {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"before, after and around are mutually exclusive")
		return query, false
	}

	var ok bool
	if params.Before != nil {
		if query.Before, ok = messageCursor(w, r, *params.Before); !ok {
			return query, false
		}
	}
	if params.After != nil {
		if query.After, ok = messageCursor(w, r, *params.After); !ok {
			return query, false
		}
	}
	if params.Around != nil {
		if query.Around, ok = messageCursor(w, r, *params.Around); !ok {
			return query, false
		}
	}
	return query, true
}

// messageCursor decodes one history cursor. On a malformed one it answers
// 400 and reports false.
func messageCursor(w http.ResponseWriter, r *http.Request, encoded string) (*storage.MessageCursor, bool) {
	createdAt, id, err := decodeTimeCursor(encoded)
	if err != nil {
		writeInvalidCursor(w, r)
		return nil, false
	}
	return &storage.MessageCursor{CreatedAt: createdAt, ID: id}, true
}

// SendMessage stores a message and announces it.
//
// It is idempotent on client_msg_id: a first send is 201, a resend of the
// same key is 200 with the message that already exists, unmodified and not
// announced a second time — the first send already delivered it.
//
// Mentions are not parsed here. The contract has the client send a mention as
// the literal token <@{user_id}> inside the content, and storage.CreateMessage
// derives the message_mentions rows from that content in the same statement
// that stores the message — so the rows the sidebar's "@" badge counts can
// never disagree with the message they came from, whatever calls it. Nothing
// in this handler needs to know, and nothing here should start guessing.
func (s *apiServer) SendMessage(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.MessageSend, sc.resource()) {
		sc.deny(w, r)
		return
	}

	var req api.SendMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ClientMsgId == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "client_msg_id is required")
		return
	}
	if !storableText(req.Content) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"content must be text that can be stored and returned unchanged")
		return
	}
	if n := utf8.RuneCountInString(req.Content); n < 1 || n > maxContentLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("content must be 1 to %d characters", maxContentLen))
		return
	}

	// The author is the session's user. Nothing in the request selects one,
	// so no caller can post as somebody else.
	msg, created, err := sc.store.CreateMessage(r.Context(), storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    sc.prin.user.ID,
		ClientMsgID: req.ClientMsgId,
		Content:     req.Content,
	})
	if errors.Is(err, storage.ErrNotFound) {
		// The channel went away between the authorization check and the
		// insert; the caller learns exactly what a stranger would.
		writeChannelNotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	if !created {
		writeJSONValue(w, r, http.StatusOK, apiMessage(msg))
		return
	}
	s.realtime.MessageCreated(channelID, msg)
	writeJSONValue(w, r, http.StatusCreated, apiMessage(msg))
}

// EditMessage replaces the content of the caller's own message.
//
// Author-only, admins included: an org admin who is a member of the channel
// still gets 403 not_message_author, because editing somebody else's words
// is impersonation and no role makes that acceptable. The message keeps its
// id and its place in history and gains an edited_at, which the design
// renders as "(edited)". Editing a deleted message is 409: a deleted row is
// the placeholder the design draws, and words must not reappear on one.
func (s *apiServer) EditMessage(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, messageID api.MessageId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	writer, msg, ok := s.resolveMessage(w, r, sc, messageID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.MessageEdit, messageResource(sc, msg)) {
		denyMessage(w, r, sc, codeNotMessageAuthor, "only a message's author may edit it")
		return
	}
	// Permission first, then the message's state: a caller who may not edit
	// this message learns nothing about whether it has been deleted.
	if msg.DeletedAt != nil {
		writeMessageDeleted(w, r)
		return
	}

	var req api.EditMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !storableText(req.Content) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"content must be text that can be stored and returned unchanged")
		return
	}
	if n := utf8.RuneCountInString(req.Content); n < 1 || n > maxContentLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("content must be 1 to %d characters", maxContentLen))
		return
	}

	edited, err := writer.UpdateMessageContent(r.Context(), channelID, messageID, req.Content)
	if errors.Is(err, storage.ErrNotFound) {
		// The message was deleted between the read above and this update.
		// The answer is the one a read a moment later would have given.
		writeMessageDeleted(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.realtime.MessageUpdated(channelID, edited)
	writeJSONValue(w, r, http.StatusOK, apiMessage(edited))
}

// DeleteMessage soft-deletes a message: the row keeps its place with its
// content erased, which is what the dashed "Message removed" placeholder
// renders.
//
// The author may delete their own; an org admin may delete any message in a
// channel they are a member of (403 not_message_author_or_admin otherwise).
// It is idempotent — deleting an already-deleted message is 204 again — and
// the event fires only for the call that actually deleted, so a retry does
// not announce a second deletion of the same message.
func (s *apiServer) DeleteMessage(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, messageID api.MessageId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	writer, msg, ok := s.resolveMessage(w, r, sc, messageID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.MessageDelete, messageResource(sc, msg)) {
		denyMessage(w, r, sc, codeNotMessageAuthorOrAdmin,
			"only a message's author, or an admin who is a member of this channel, may delete it")
		return
	}
	if msg.DeletedAt != nil {
		// Already deleted, by this caller or another: 204, and nothing to
		// announce — whoever deleted it first announced it.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	deleted, err := writer.SoftDeleteMessage(r.Context(), channelID, messageID, sc.prin.user.ID)
	if errors.Is(err, storage.ErrNotFound) {
		// Somebody else deleted it in between. Same answer, same silence.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	// The whole message, not just its id: the placeholder keeps the
	// message's position and metadata (ws-protocol.md §4).
	s.realtime.MessageDeleted(channelID, deleted)
	w.WriteHeader(http.StatusNoContent)
}

// resolveMessage loads the message a message-scoped request acts on.
//
// A caller who is not in the channel is refused before anything is read on
// their behalf: the answer is the channel's own 404 either way, and there is
// nothing to look up for somebody who cannot see the conversation. The read
// itself is scoped to the channel, so an id from another conversation
// answers with that same 404 — no answer on this path can be used to probe
// for messages elsewhere.
//
// It decides no permission: the message it returns is what the handler's
// explicit authz.Can call decides on.
func (s *apiServer) resolveMessage(w http.ResponseWriter, r *http.Request,
	sc channelScope, messageID uuid.UUID,
) (messageWriter, storage.Message, bool) {
	if !sc.member {
		sc.deny(w, r)
		return nil, storage.Message{}, false
	}
	writer, ok := sc.store.(messageWriter)
	if !ok {
		internalError(w, r, errors.New("store cannot edit or delete messages"))
		return nil, storage.Message{}, false
	}

	msg, err := writer.MessageByID(r.Context(), sc.channel.ID, messageID)
	if errors.Is(err, storage.ErrNotFound) {
		writeChannelNotFound(w, r)
		return nil, storage.Message{}, false
	}
	if err != nil {
		internalError(w, r, err)
		return nil, storage.Message{}, false
	}
	return writer, msg, true
}

// messageResource is the authz resource for a message-scoped action: the
// channel's facts plus whether the caller wrote the message.
func messageResource(sc channelScope, msg storage.Message) authz.Message {
	return authz.Message{
		Channel: sc.resource(),
		Author:  msg.Author.ID == sc.prin.user.ID,
	}
}

// denyMessage answers a message action authz refused.
//
// A stranger to the channel gets its 404, exactly as every other
// channel-scoped refusal does. A member who simply lacks this one power gets
// 403 with the operation's own code: "not the author" and "not the author or
// an admin" are different refusals, the contract names them separately, and
// a client that showed one for the other would tell an admin they cannot
// delete a message they can.
func denyMessage(w http.ResponseWriter, r *http.Request, sc channelScope, code errorCode, message string) {
	if !sc.member {
		sc.deny(w, r)
		return
	}
	writeError(w, r, http.StatusForbidden, code, message)
}

// writeMessageDeleted is the 409 an edit of a deleted message answers.
func writeMessageDeleted(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusConflict, codeMessageDeleted, "this message has been deleted")
}

// SetReadPosition moves the caller's read position in a channel — where the
// "New messages" divider goes, and the point every unread count starts
// from.
//
// It is monotonic in storage: a position at or behind the stored one is
// accepted and changes nothing, so a stale tab cannot undo a read. The
// endpoint answers 204 either way.
func (s *apiServer) SetReadPosition(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ReadPositionSet, sc.resource()) {
		sc.deny(w, r)
		return
	}

	var req api.SetReadPositionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.MessageId == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "message_id is required")
		return
	}

	// The position is always written under the authenticated caller. Nothing
	// in the request selects whose it is: this is own-device sync, and the
	// contract carries no cross-user read receipts anywhere.
	err := sc.store.SetReadPosition(r.Context(), channelID, sc.prin.user.ID, req.MessageId)
	if errors.Is(err, storage.ErrNotFound) {
		// The anchor names no message of this channel. A 400 rather than a
		// 404: the 404 on this path is the channel's, and the contract states
		// the rule as a property of the field ("Must belong to this
		// channel"). A message in a conversation the caller cannot see is
		// answered exactly like one that never existed either way, so the
		// answer cannot be used to probe for messages elsewhere.
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"message_id must name a message in this channel")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	// Synced to the same user's other sockets, and to nobody else's. The
	// timestamp is when the move was accepted; storage keeps the anchor's own
	// created_at for its monotonic test and does not report it back. Note
	// that storage also does not report whether the position was stored or
	// ignored as a regression, so a stale tab's replay is announced too —
	// reported to the orchestrator; clients reconcile from REST.
	s.realtime.ReadPosition(sc.prin.user.ID, channelID, req.MessageId, time.Now().UTC())
	w.WriteHeader(http.StatusNoContent)
}

// apiMessagePage maps one page of history onto the contract's MessagePage.
//
// The two cursors are the handles for the next scrollback and the next
// forward fetch, each present only when storage reported more history that
// way. before_cursor anchors on the page's oldest row and after_cursor on
// its newest — anchoring either on the wrong end would skip or repeat a
// page's worth of conversation.
func apiMessagePage(page storage.MessagePage) api.MessagePage {
	out := api.MessagePage{Messages: make([]api.Message, 0, len(page.Messages))}
	for _, m := range page.Messages {
		out.Messages = append(out.Messages, apiMessage(m))
	}
	if len(page.Messages) == 0 {
		return out
	}

	if page.HasBefore {
		oldest := page.Messages[0]
		cursor := encodeTimeCursor(oldest.CreatedAt, oldest.ID)
		out.BeforeCursor = &cursor
	}
	if page.HasAfter {
		newest := page.Messages[len(page.Messages)-1]
		cursor := encodeTimeCursor(newest.CreatedAt, newest.ID)
		out.AfterCursor = &cursor
	}
	return out
}

// apiMessage maps a stored message onto the contract's Message schema.
//
// Attachments is an empty array rather than null: the contract requires the
// field, and the Phase 1.3 upload pipeline is what will ever put anything
// in it. link_preview stays absent until the same phase's preview proxy
// exists.
func apiMessage(m storage.Message) api.Message {
	return api.Message{
		Id:        m.ID,
		ChannelId: m.ChannelID,
		Author: api.UserSummary{
			Id:          m.Author.ID,
			Username:    m.Author.Username,
			DisplayName: m.Author.DisplayName,
		},
		ClientMsgId: m.ClientMsgID,
		Content:     m.Content,
		CreatedAt:   m.CreatedAt,
		EditedAt:    m.EditedAt,
		DeletedAt:   m.DeletedAt,
		Attachments: []api.Attachment{},
	}
}
