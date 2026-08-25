package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
			"around alone (1.2b)":   "?around=" + validMessageCursor(t),
			"cursor not base64":     "?before=%21%21%21",
			"cursor not a position": "?before=bm9zZXBhcmF0b3I",
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
