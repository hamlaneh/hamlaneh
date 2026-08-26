package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

const testClientMsgID = "ffffffff-0000-0000-0000-000000000001"

func messageUUID() uuid.UUID   { return uuid.MustParse(testMessageID) }
func clientMsgUUID() uuid.UUID { return uuid.MustParse(testClientMsgID) }

// fixtureMessage is one stored message in the fixture channel.
func fixtureMessage() storage.Message {
	author := fixtureUser()
	return storage.Message{
		ID:          messageUUID(),
		ChannelID:   channelUUID(),
		Author:      storage.MessageAuthor{ID: author.ID, Username: author.Username, DisplayName: author.DisplayName},
		ClientMsgID: clientMsgUUID(),
		Content:     "hello",
		CreatedAt:   time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
	}
}

func TestListMessages(t *testing.T) {
	t.Parallel()

	t.Run("returns the newest page with no cursor at all", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var got storage.ListMessagesParams
		store.listMessages = func(_ context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
			got = params
			return storage.MessagePage{Messages: []storage.Message{fixtureMessage()}}, nil
		}

		rec := do(t, store, request(http.MethodGet, channelPath("/messages"), "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got.ChannelID != channelUUID() || got.Before != nil || got.After != nil {
			t.Errorf("storage got %+v, want the channel and no cursor", got)
		}
		if got.Limit != 50 {
			t.Errorf("limit defaulted to %d, want 50", got.Limit)
		}

		var page api.MessagePage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not a MessagePage: %v", err)
		}
		if len(page.Messages) != 1 {
			t.Fatalf("page has %d messages, want 1", len(page.Messages))
		}
		msg := page.Messages[0]
		if msg.Id != messageUUID() || msg.ClientMsgId != clientMsgUUID() || msg.Content != "hello" {
			t.Errorf("message = %+v, want the stored one", msg)
		}
		if msg.Attachments == nil {
			t.Error("attachments is null; the contract requires an array")
		}
		if msg.Author.Username != "member" {
			t.Errorf("author = %+v, want the message's author summary", msg.Author)
		}
		if page.BeforeCursor != nil || page.AfterCursor != nil {
			t.Error("page offered a cursor although storage reported no more history")
		}
	})

	t.Run("offers a cursor in each direction storage reports more history", func(t *testing.T) {
		t.Parallel()
		older, newer := fixtureMessage(), fixtureMessage()
		newer.ID = uuid.MustParse(testPeerID)
		newer.CreatedAt = older.CreatedAt.Add(time.Minute)

		store := memberStore()
		var got []storage.ListMessagesParams
		store.listMessages = func(_ context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
			got = append(got, params)
			return storage.MessagePage{
				Messages:  []storage.Message{older, newer},
				HasBefore: true,
				HasAfter:  true,
			}, nil
		}
		handler := httpserver.Handler(store)

		rec := doHandler(t, handler, request(http.MethodGet, channelPath("/messages"), "", withSessionCookie("tok")))
		var page api.MessagePage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not a MessagePage: %v", err)
		}
		if page.BeforeCursor == nil || page.AfterCursor == nil {
			t.Fatalf("page cursors = (%v, %v), want both", page.BeforeCursor, page.AfterCursor)
		}

		// before_cursor anchors on the oldest row of the page, after_cursor on
		// the newest — otherwise paging either skips or repeats history.
		doHandler(t, handler, request(http.MethodGet,
			channelPath("/messages")+"?before="+*page.BeforeCursor, "", withSessionCookie("tok")))
		doHandler(t, handler, request(http.MethodGet,
			channelPath("/messages")+"?after="+*page.AfterCursor, "", withSessionCookie("tok")))
		if len(got) != 3 {
			t.Fatalf("storage called %d times, want 3", len(got))
		}
		if got[1].Before == nil || got[1].Before.ID != older.ID || !got[1].Before.CreatedAt.Equal(older.CreatedAt) {
			t.Errorf("before cursor decoded to %+v, want the oldest row", got[1].Before)
		}
		if got[2].After == nil || got[2].After.ID != newer.ID || !got[2].After.CreatedAt.Equal(newer.CreatedAt) {
			t.Errorf("after cursor decoded to %+v, want the newest row", got[2].After)
		}
	})

	t.Run("a deleted message keeps its place in the page", func(t *testing.T) {
		t.Parallel()
		deletedAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
		removed := fixtureMessage()
		removed.Content = ""
		removed.DeletedAt = &deletedAt

		store := memberStore()
		store.listMessages = func(context.Context, storage.ListMessagesParams) (storage.MessagePage, error) {
			return storage.MessagePage{Messages: []storage.Message{removed}}, nil
		}
		rec := do(t, store, request(http.MethodGet, channelPath("/messages"), "", withSessionCookie("tok")))

		var page api.MessagePage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not a MessagePage: %v", err)
		}
		if len(page.Messages) != 1 {
			t.Fatalf("deleted message dropped from the page: %+v", page.Messages)
		}
		if page.Messages[0].DeletedAt == nil || page.Messages[0].Content != "" {
			t.Errorf("deleted message = %+v, want empty content and a deleted_at", page.Messages[0])
		}
	})

	t.Run("rejects malformed paging", func(t *testing.T) {
		t.Parallel()
		tests := map[string]string{
			"limit zero":            "?limit=0",
			"limit over max":        "?limit=101",
			"before and after":      "?before=" + validMessageCursor(t) + "&after=" + validMessageCursor(t),
			"before and around":     "?before=" + validMessageCursor(t) + "&around=" + validMessageCursor(t),
			"after and around":      "?after=" + validMessageCursor(t) + "&around=" + validMessageCursor(t),
			"cursor not base64":     "?before=%21%21%21",
			"cursor not a position": "?before=bm9zZXBhcmF0b3I",
			"around not base64":     "?around=%21%21%21",
		}
		for name, query := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// listMessages stays unwired: a rejected page must not be read.
				rec := do(t, memberStore(), request(http.MethodGet, channelPath("/messages")+query, "", withSessionCookie("tok")))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("an around cursor reaches storage", func(t *testing.T) {
		t.Parallel()
		// The permalink page. How the limit splits either side of the anchor
		// is storage's decision (storage.aroundPage); what this pins is that
		// the handler hands the cursor over as `around` and as nothing else.
		store := memberStore()
		var got storage.ListMessagesParams
		store.listMessages = func(_ context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
			got = params
			return storage.MessagePage{Messages: []storage.Message{fixtureMessage()}}, nil
		}

		rec := do(t, store, request(http.MethodGet,
			channelPath("/messages")+"?around="+validMessageCursor(t), "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Around == nil {
			t.Fatalf("storage got %+v, want the around cursor", got)
		}
		if got.Before != nil || got.After != nil {
			t.Errorf("storage got before=%v after=%v alongside around", got.Before, got.After)
		}
		if got.Around.ID != messageUUID() {
			t.Errorf("around anchor = %s, want the cursor's message %s", got.Around.ID, messageUUID())
		}
	})

	t.Run("a non-member gets 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodGet, channelPath("/messages"), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})
}

// validMessageCursor mints a cursor the server will decode, by reading one
// off a page the server itself produced.
func validMessageCursor(t *testing.T) string {
	t.Helper()

	store := memberStore()
	store.listMessages = func(context.Context, storage.ListMessagesParams) (storage.MessagePage, error) {
		return storage.MessagePage{Messages: []storage.Message{fixtureMessage()}, HasBefore: true}, nil
	}
	rec := do(t, store, request(http.MethodGet, channelPath("/messages"), "", withSessionCookie("tok")))

	var page api.MessagePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("cursor fixture: body is not a MessagePage: %v", err)
	}
	if page.BeforeCursor == nil {
		t.Fatal("cursor fixture: page carried no before_cursor")
	}
	return *page.BeforeCursor
}

func TestSendMessage(t *testing.T) {
	t.Parallel()

	body := `{"client_msg_id":"` + testClientMsgID + `","content":"hello"}`

	t.Run("a first send is 201 and is announced", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var got storage.NewMessage
		store.createMessage = func(_ context.Context, nm storage.NewMessage) (storage.Message, bool, error) {
			got = nm
			return fixtureMessage(), true, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, channelPath("/messages"), body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if got.ChannelID != channelUUID() || got.ClientMsgID != clientMsgUUID() || got.Content != "hello" {
			t.Errorf("storage got %+v, want the request's channel, key and content", got)
		}
		if got.AuthorID != fixtureUser().ID {
			t.Errorf("author = %s, want the authenticated caller", got.AuthorID)
		}
		if len(rt.messageCreated) != 1 || rt.messageCreated[0].ID != messageUUID() {
			t.Errorf("announced %v, want the stored message", rt.messageCreated)
		}
	})

	t.Run("a resend is 200 with the same message and is not announced again", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.createMessage = func(context.Context, storage.NewMessage) (storage.Message, bool, error) {
			return fixtureMessage(), false, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, channelPath("/messages"), body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var got api.Message
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not a Message: %v", err)
		}
		if got.Id != messageUUID() || got.ClientMsgId != clientMsgUUID() || got.Content != "hello" {
			t.Errorf("resend returned %+v, want the stored message unmodified", got)
		}
		if len(rt.messageCreated) != 0 {
			t.Errorf("a resend announced %d events, want 0 — the first send already delivered it",
				len(rt.messageCreated))
		}
	})

	t.Run("a channel that vanished mid-request is 404", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.createMessage = func(context.Context, storage.NewMessage) (storage.Message, bool, error) {
			return storage.Message{}, false, storage.ErrNotFound
		}
		rec := do(t, store, request(http.MethodPost, channelPath("/messages"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("a non-member cannot send, and learns nothing", func(t *testing.T) {
		t.Parallel()
		// createMessage stays unwired: the refusal must happen first.
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/messages"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("rejects malformed requests", func(t *testing.T) {
		t.Parallel()
		tests := map[string]string{
			"empty object":       `{}`,
			"no client_msg_id":   `{"content":"hello"}`,
			"bad client_msg_id":  `{"client_msg_id":"nope","content":"hello"}`,
			"empty content":      `{"client_msg_id":"` + testClientMsgID + `","content":""}`,
			"content over 4000":  `{"client_msg_id":"` + testClientMsgID + `","content":"` + strings.Repeat("x", 4001) + `"}`,
			"content wrong type": `{"client_msg_id":"` + testClientMsgID + `","content":42}`,
			"not json":           `{`,
		}
		for name, req := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				rec := do(t, memberStore(), request(http.MethodPost, channelPath("/messages"), req,
					withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("accepts content at exactly the contract's bound", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.createMessage = func(context.Context, storage.NewMessage) (storage.Message, bool, error) {
			return fixtureMessage(), true, nil
		}
		// 4000 multi-byte runes: the bound is characters, not bytes.
		rec := do(t, store, request(http.MethodPost, channelPath("/messages"),
			`{"client_msg_id":"`+testClientMsgID+`","content":"`+strings.Repeat("ی", 4000)+`"}`,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

func TestSetReadPosition(t *testing.T) {
	t.Parallel()

	body := `{"message_id":"` + testMessageID + `"}`

	t.Run("stores the caller's own position and syncs their other devices", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var gotChannel, gotUser, gotMessage uuid.UUID
		store.setReadPosition = func(_ context.Context, channelID, userID, messageID uuid.UUID) error {
			gotChannel, gotUser, gotMessage = channelID, userID, messageID
			return nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPut, channelPath("/read"), body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotMessage != messageUUID() {
			t.Errorf("storage got (%s, %s), want (channel, message)", gotChannel, gotMessage)
		}
		if gotUser != fixtureUser().ID {
			t.Errorf("stored under %s, want the authenticated caller — nothing in the request selects whose position it is", gotUser)
		}
		if len(rt.readPositions) != 1 {
			t.Fatalf("announced %d read positions, want 1", len(rt.readPositions))
		}
		if rt.readPositions[0].userID != fixtureUser().ID {
			t.Errorf("announced for %s, want the caller's own sockets only", rt.readPositions[0].userID)
		}
	})

	t.Run("a message that is not in this channel is 400", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.setReadPosition = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			return storage.ErrNotFound
		}
		rec := do(t, store, request(http.MethodPut, channelPath("/read"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a non-member gets 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodPut, channelPath("/read"), body,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("rejects a missing or malformed message_id", func(t *testing.T) {
		t.Parallel()
		for _, req := range []string{`{}`, `{"message_id":"nope"}`, `{`} {
			t.Run(req, func(t *testing.T) {
				t.Parallel()
				rec := do(t, memberStore(), request(http.MethodPut, channelPath("/read"), req,
					withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

// editStore is a fakeStore that can also read, edit and soft-delete one
// message. The three reads are not on httpserver.Store — they are declared
// in message_handlers.go for the same reason SearchMessages is declared in
// search_handler.go — so the fake grows them the same way searchStore does.
type editStore struct {
	*fakeStore
	messageByID          func(ctx context.Context, channelID, messageID uuid.UUID) (storage.Message, error)
	updateMessageContent func(ctx context.Context, channelID, messageID uuid.UUID, content string) (storage.Message, error)
	softDeleteMessage    func(ctx context.Context, channelID, messageID, deletedBy uuid.UUID) (storage.Message, error)
}

var _ httpserver.Store = (*editStore)(nil)

func (s *editStore) MessageByID(ctx context.Context, channelID, messageID uuid.UUID) (storage.Message, error) {
	if s.messageByID == nil {
		return storage.Message{}, errFakeUnwired
	}
	return s.messageByID(ctx, channelID, messageID)
}

func (s *editStore) UpdateMessageContent(
	ctx context.Context, channelID, messageID uuid.UUID, content string,
) (storage.Message, error) {
	if s.updateMessageContent == nil {
		return storage.Message{}, errFakeUnwired
	}
	return s.updateMessageContent(ctx, channelID, messageID, content)
}

func (s *editStore) SoftDeleteMessage(
	ctx context.Context, channelID, messageID, deletedBy uuid.UUID,
) (storage.Message, error) {
	if s.softDeleteMessage == nil {
		return storage.Message{}, errFakeUnwired
	}
	return s.softDeleteMessage(ctx, channelID, messageID, deletedBy)
}

// messageStore wires an editStore for a member of the fixture channel whose
// message read answers msg. The non-member cells build their own store: they
// must leave messageByID unwired, so that a handler which read a message on
// a stranger's behalf would fail loudly.
func messageStore(user storage.User, msg storage.Message) *editStore {
	store := &editStore{fakeStore: channelStore(user, fixtureChannel(), true)}
	store.messageByID = func(context.Context, uuid.UUID, uuid.UUID) (storage.Message, error) {
		return msg, nil
	}
	return store
}

// authoredBy re-attributes the fixture message, so a test can say plainly
// whose words are being edited or deleted.
func authoredBy(user storage.User) storage.Message {
	msg := fixtureMessage()
	msg.Author = storage.MessageAuthor{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
	}
	return msg
}

// adminUser is a member of the fixture channel who also holds the instance
// admin role — the principal the two moderation rules disagree about.
func adminUser() storage.User {
	admin := fixtureUser()
	admin.IsAdmin = true
	return admin
}

// MessageUpdated and MessageDeleted keep *recordingRealtime a whole
// httpserver.Realtime. They record nothing: its fields are declared in
// channel_handlers_test.go, which this slice does not touch, and the tests
// that assert these two events use messageRealtime below.
func (rt *recordingRealtime) MessageUpdated(uuid.UUID, storage.Message) {}
func (rt *recordingRealtime) MessageDeleted(uuid.UUID, storage.Message) {}

// messageRealtime records the two events editing and deleting announce.
type messageRealtime struct {
	recordingRealtime
	mu      sync.Mutex
	updated []recordedMessageEvent
	deleted []recordedMessageEvent
}

// recordedMessageEvent is one message_updated or message_deleted: the
// channel it was announced on and the whole message it carried.
type recordedMessageEvent struct {
	channelID uuid.UUID
	message   storage.Message
}

func (rt *messageRealtime) MessageUpdated(channelID uuid.UUID, m storage.Message) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.updated = append(rt.updated, recordedMessageEvent{channelID: channelID, message: m})
}

func (rt *messageRealtime) MessageDeleted(channelID uuid.UUID, m storage.Message) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.deleted = append(rt.deleted, recordedMessageEvent{channelID: channelID, message: m})
}

// messagePath is the request target of one message of the fixture channel.
func messagePath() string { return channelPath("/messages/" + testMessageID) }

// editRequest is a PATCH of the fixture message with the given content.
func editRequest(content string) *http.Request {
	return request(http.MethodPatch, messagePath(),
		fmt.Sprintf(`{"content":%q}`, content), withSessionCookie("tok"), withCSRF())
}

// deleteRequest is a DELETE of the fixture message.
func deleteRequest() *http.Request {
	return request(http.MethodDelete, messagePath(), "", withSessionCookie("tok"), withCSRF())
}

func TestEditMessage(t *testing.T) {
	t.Parallel()

	t.Run("the author's edit is stored and announced", func(t *testing.T) {
		t.Parallel()
		editedAt := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		var gotChannel, gotMessage uuid.UUID
		var gotContent string
		store.updateMessageContent = func(
			_ context.Context, channelID, messageID uuid.UUID, content string,
		) (storage.Message, error) {
			gotChannel, gotMessage, gotContent = channelID, messageID, content
			msg := authoredBy(fixtureUser())
			msg.Content = content
			msg.EditedAt = &editedAt
			return msg, nil
		}
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, editRequest("second thoughts"))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotMessage != messageUUID() || gotContent != "second thoughts" {
			t.Errorf("storage got (%s, %s, %q), want the fixture message and the new content",
				gotChannel, gotMessage, gotContent)
		}

		var msg api.Message
		if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
			t.Fatalf("body is not a Message: %v", err)
		}
		if msg.Content != "second thoughts" {
			t.Errorf("content = %q, want the edit", msg.Content)
		}
		if msg.EditedAt == nil || !msg.EditedAt.Equal(editedAt) {
			t.Errorf("edited_at = %v, want %v; the client draws (edited) from it", msg.EditedAt, editedAt)
		}
		if msg.Id != messageUUID() {
			t.Errorf("id = %s, want the message that was edited", msg.Id)
		}

		if len(rt.updated) != 1 {
			t.Fatalf("announced %d message_updated events, want 1", len(rt.updated))
		}
		if rt.updated[0].channelID != channelUUID() || rt.updated[0].message.Content != "second thoughts" {
			t.Errorf("announced %+v, want the edited message on the fixture channel", rt.updated[0])
		}
	})

	t.Run("an admin member still cannot edit somebody else's message", func(t *testing.T) {
		t.Parallel()
		// The one rule an admin does not hold: editing another person's
		// words is impersonation. updateMessageContent stays unwired, so a
		// refusal that still wrote would fail loudly.
		store := messageStore(adminUser(), authoredBy(peerUser()))
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, editRequest("words in your mouth"))
		wantError(t, rec, http.StatusForbidden, "not_message_author")
		if len(rt.updated) != 0 {
			t.Errorf("a refused edit announced %+v", rt.updated)
		}
	})

	t.Run("a member who is not the author is refused", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(peerUser()))
		rec := do(t, store, editRequest("not mine to edit"))
		wantError(t, rec, http.StatusForbidden, "not_message_author")
	})

	t.Run("a stranger to the channel gets its 404 and no message is read", func(t *testing.T) {
		t.Parallel()
		// messageByID stays unwired: nothing is looked up for somebody who
		// cannot see the conversation.
		store := &editStore{fakeStore: channelStore(fixtureUser(), fixtureChannel(), false)}
		rec := do(t, store, editRequest("whose message?"))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("a message from another channel is the same 404", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), storage.Message{})
		store.messageByID = func(context.Context, uuid.UUID, uuid.UUID) (storage.Message, error) {
			return storage.Message{}, storage.ErrNotFound
		}
		rec := do(t, store, editRequest("reaching elsewhere"))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("editing a deleted message is 409", func(t *testing.T) {
		t.Parallel()
		deletedAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
		removed := authoredBy(fixtureUser())
		removed.Content = ""
		removed.DeletedAt = &deletedAt

		// updateMessageContent stays unwired: the refusal happens before it.
		store := messageStore(fixtureUser(), removed)
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, editRequest("back from the dead"))
		wantError(t, rec, http.StatusConflict, "message_deleted")
		if len(rt.updated) != 0 {
			t.Errorf("a refused edit announced %+v", rt.updated)
		}
	})

	t.Run("a deletion that lands mid-request is the same 409", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		store.updateMessageContent = func(
			context.Context, uuid.UUID, uuid.UUID, string,
		) (storage.Message, error) {
			return storage.Message{}, storage.ErrNotFound
		}
		rec := do(t, store, editRequest("racing a delete"))
		wantError(t, rec, http.StatusConflict, "message_deleted")
	})

	t.Run("rejects content outside the contract bounds", func(t *testing.T) {
		t.Parallel()
		for name, content := range map[string]string{
			"empty":    "",
			"too long": strings.Repeat("a", 4001),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// updateMessageContent stays unwired: a rejected edit writes nothing.
				store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
				rec := do(t, store, editRequest(content))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})

	t.Run("a storage failure is a 500", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		store.updateMessageContent = func(
			context.Context, uuid.UUID, uuid.UUID, string,
		) (storage.Message, error) {
			return storage.Message{}, errors.New("boom")
		}
		rec := do(t, store, editRequest("unlucky"))
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
	})
}

func TestDeleteMessage(t *testing.T) {
	t.Parallel()

	// deletedFixture is what storage hands back: the row erased in place.
	deletedFixture := func(author storage.User) storage.Message {
		deletedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
		msg := authoredBy(author)
		msg.Content = ""
		msg.DeletedAt = &deletedAt
		return msg
	}

	t.Run("the author's delete is 204 and announces the whole message", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		var gotChannel, gotMessage, gotDeletedBy uuid.UUID
		store.softDeleteMessage = func(
			_ context.Context, channelID, messageID, deletedBy uuid.UUID,
		) (storage.Message, error) {
			gotChannel, gotMessage, gotDeletedBy = channelID, messageID, deletedBy
			return deletedFixture(fixtureUser()), nil
		}
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, deleteRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotMessage != messageUUID() {
			t.Errorf("storage got (%s, %s), want the fixture message", gotChannel, gotMessage)
		}
		if gotDeletedBy != fixtureUser().ID {
			t.Errorf("deleted_by = %s, want the caller %s", gotDeletedBy, fixtureUser().ID)
		}

		if len(rt.deleted) != 1 {
			t.Fatalf("announced %d message_deleted events, want 1", len(rt.deleted))
		}
		announced := rt.deleted[0]
		if announced.channelID != channelUUID() || announced.message.ID != messageUUID() {
			t.Errorf("announced %+v, want the deleted message on the fixture channel", announced)
		}
		if announced.message.Content != "" || announced.message.DeletedAt == nil {
			t.Errorf("announced content=%q deleted_at=%v; the placeholder needs both",
				announced.message.Content, announced.message.DeletedAt)
		}
	})

	t.Run("an admin member may delete somebody else's message", func(t *testing.T) {
		t.Parallel()
		// The rule editing does not have: deletion removes words, it does
		// not put new ones in somebody's mouth.
		store := messageStore(adminUser(), authoredBy(peerUser()))
		store.softDeleteMessage = func(
			context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
		) (storage.Message, error) {
			return deletedFixture(peerUser()), nil
		}
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, deleteRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if len(rt.deleted) != 1 {
			t.Errorf("announced %d message_deleted events, want 1", len(rt.deleted))
		}
	})

	t.Run("a member who is neither author nor admin is refused", func(t *testing.T) {
		t.Parallel()
		// softDeleteMessage stays unwired: a refusal that still deleted
		// would fail loudly.
		store := messageStore(fixtureUser(), authoredBy(peerUser()))
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, deleteRequest())
		wantError(t, rec, http.StatusForbidden, "not_message_author_or_admin")
		if len(rt.deleted) != 0 {
			t.Errorf("a refused delete announced %+v", rt.deleted)
		}
	})

	t.Run("a stranger to the channel gets its 404", func(t *testing.T) {
		t.Parallel()
		store := &editStore{fakeStore: channelStore(fixtureUser(), fixtureChannel(), false)}
		rec := do(t, store, deleteRequest())
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("deleting an already-deleted message is 204 and announces nothing", func(t *testing.T) {
		t.Parallel()
		// Idempotent, and silent: whoever deleted it first announced it.
		// softDeleteMessage stays unwired — there is nothing left to delete.
		store := messageStore(fixtureUser(), deletedFixture(fixtureUser()))
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, deleteRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if len(rt.deleted) != 0 {
			t.Errorf("a second delete announced %+v", rt.deleted)
		}
	})

	t.Run("a delete that lands mid-request is 204 and announces nothing", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		store.softDeleteMessage = func(
			context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
		) (storage.Message, error) {
			return storage.Message{}, storage.ErrNotFound
		}
		rt := &messageRealtime{}

		rec := doRealtime(t, store, rt, deleteRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if len(rt.deleted) != 0 {
			t.Errorf("a raced delete announced %+v", rt.deleted)
		}
	})

	t.Run("a storage failure is a 500", func(t *testing.T) {
		t.Parallel()
		store := messageStore(fixtureUser(), authoredBy(fixtureUser()))
		store.softDeleteMessage = func(
			context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
		) (storage.Message, error) {
			return storage.Message{}, errors.New("boom")
		}
		rec := do(t, store, deleteRequest())
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
	})
}

// Fixture ids for the route-gating walk below. Nothing looks them up: the
// gate answers before any handler runs.
const (
	gateChannelID = "11111111-2222-3333-4444-555555555555"
	gateUserID    = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	gateMessageID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
)

// gatedRoute is one Phase 1.2 route with a concrete request target.
type gatedRoute struct {
	method string
	target string
}

// messagingRoutes is every operation the Phase 1.2 contract added. The
// route-level gate runs before all of them, which is what the test below
// walks.
func messagingRoutes() []gatedRoute {
	base := "/api/v1/channels/" + gateChannelID
	return []gatedRoute{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodPost, "/api/v1/channels"},
		{http.MethodGet, base},
		{http.MethodPatch, base},
		{http.MethodGet, base + "/members"},
		{http.MethodPost, base + "/members"},
		{http.MethodDelete, base + "/members/" + gateUserID},
		{http.MethodGet, base + "/messages"},
		{http.MethodPost, base + "/messages"},
		{http.MethodPatch, base + "/messages/" + gateMessageID},
		{http.MethodDelete, base + "/messages/" + gateMessageID},
		{http.MethodPut, base + "/read"},
		{http.MethodPost, "/api/v1/dms"},
		{http.MethodGet, "/api/v1/search?q=hello"},
	}
}

// TestMessagingRoutesAreGatedBeforeTheHandler pins the ordering every
// messaging route depends on: route-level security runs first, so an
// anonymous caller never reaches a handler and a user who still owes a
// password change never gets past the gate.
func TestMessagingRoutesAreGatedBeforeTheHandler(t *testing.T) {
	t.Parallel()

	locked := fixtureUser()
	locked.MustChangePassword = true

	for _, route := range messagingRoutes() {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()

			anon := do(t, &fakeStore{}, request(route.method, route.target, ""))
			wantError(t, anon, http.StatusUnauthorized, "not_authenticated")

			mods := []func(*http.Request){withSessionCookie("tok")}
			if route.method != http.MethodGet {
				mods = append(mods, withCSRF())
			}
			gated := do(t, authedStore(locked), request(route.method, route.target, "", mods...))
			wantError(t, gated, http.StatusForbidden, "password_change_required")
		})
	}
}
