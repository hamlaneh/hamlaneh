package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Fixture ids for the conversation surface.
const (
	testChannelID = "cccccccc-0000-0000-0000-000000000001"
	testMessageID = "dddddddd-0000-0000-0000-000000000001"
	testPeerID    = "eeeeeeee-0000-0000-0000-000000000001"
)

func channelUUID() uuid.UUID { return uuid.MustParse(testChannelID) }
func peerUUID() uuid.UUID    { return uuid.MustParse(testPeerID) }

// channelPath is the request target of a channel-scoped route.
func channelPath(suffix string) string {
	return "/api/v1/channels/" + testChannelID + suffix
}

// fixtureChannel is a public channel the fixture user created, carrying the
// caller-scoped counts only ChannelForUser and its siblings can fill.
func fixtureChannel() storage.Channel {
	slug := "deploys"
	creator := fixtureUser().ID
	lastMessage := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	return storage.Channel{
		ID:            channelUUID(),
		Kind:          storage.ChannelKindPublic,
		Slug:          &slug,
		Topic:         "ship it",
		CreatedBy:     &creator,
		MemberCount:   3,
		UnreadCount:   2,
		MentionCount:  1,
		LastMessageAt: &lastMessage,
		CreatedAt:     time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC),
	}
}

// fixtureDM is the direct message between the fixture user and the peer.
func fixtureDM() storage.Channel {
	ch := fixtureChannel()
	ch.Kind = storage.ChannelKindDM
	ch.Slug = nil
	ch.Topic = ""
	ch.MemberCount = 2
	a, b := fixtureUser().ID, peerUUID()
	ch.DMUserA, ch.DMUserB = &a, &b
	return ch
}

// channelStore wires a fakeStore that authenticates user, answers ch for
// every channel read, and reports the caller's membership.
func channelStore(user storage.User, ch storage.Channel, member bool) *fakeStore {
	store := authedStore(user)
	store.channelForUser = func(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, error) {
		return ch, nil
	}
	store.isChannelMember = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
		return member, nil
	}
	return store
}

// memberStore is channelStore for a plain member of the fixture channel.
func memberStore() *fakeStore {
	return channelStore(fixtureUser(), fixtureChannel(), true)
}

// recordingRealtime captures what a handler announced, so a test can assert
// both that an event fired and that it carried the right payload.
type recordingRealtime struct {
	mu             sync.Mutex
	messageCreated []storage.Message
	channelCreated []recordedChannelCreated
	channelUpdated []storage.Channel
	channelRemoved []recordedChannelRemoved
	readPositions  []recordedReadPosition
}

type recordedChannelCreated struct {
	userIDs []uuid.UUID
	channel storage.Channel
}

type recordedChannelRemoved struct{ userID, channelID uuid.UUID }

type recordedReadPosition struct {
	userID, channelID, messageID uuid.UUID
	readAt                       time.Time
}

func (rt *recordingRealtime) MessageCreated(_ uuid.UUID, m storage.Message) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.messageCreated = append(rt.messageCreated, m)
}

func (rt *recordingRealtime) ChannelCreated(userIDs []uuid.UUID, ch storage.Channel) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.channelCreated = append(rt.channelCreated, recordedChannelCreated{userIDs: userIDs, channel: ch})
}

func (rt *recordingRealtime) ChannelUpdated(_ uuid.UUID, ch storage.Channel) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.channelUpdated = append(rt.channelUpdated, ch)
}

func (rt *recordingRealtime) MemberAdded(uuid.UUID, storage.User)   {}
func (rt *recordingRealtime) MemberRemoved(uuid.UUID, storage.User) {}

func (rt *recordingRealtime) ChannelRemoved(userID, channelID uuid.UUID) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.channelRemoved = append(rt.channelRemoved, recordedChannelRemoved{userID: userID, channelID: channelID})
}

func (rt *recordingRealtime) ReadPosition(userID, channelID, messageID uuid.UUID, readAt time.Time) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.readPositions = append(rt.readPositions,
		recordedReadPosition{userID: userID, channelID: channelID, messageID: messageID, readAt: readAt})
}

// doRealtime serves req against store with rt attached.
func doRealtime(t *testing.T, store httpserver.Store, rt httpserver.Realtime, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return doHandler(t, httpserver.Handler(store, httpserver.WithRealtime(rt)), req)
}

func TestGetChannel(t *testing.T) {
	t.Parallel()

	t.Run("a member gets the channel with their own counts", func(t *testing.T) {
		t.Parallel()
		rec := do(t, memberStore(), request(http.MethodGet, channelPath(""), "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}

		var got api.Channel
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not the contract Channel shape: %v", err)
		}
		if got.Id != channelUUID() {
			t.Errorf("id = %s, want %s", got.Id, channelUUID())
		}
		if got.UnreadCount != 2 || got.MentionCount != 1 {
			t.Errorf("counts = (%d, %d), want (2, 1) — a caller-scoped read fills them",
				got.UnreadCount, got.MentionCount)
		}
		if got.MemberCount != 3 {
			t.Errorf("member_count = %d, want 3", got.MemberCount)
		}
		if got.Slug == nil || *got.Slug != "deploys" {
			t.Errorf("slug = %v, want deploys", got.Slug)
		}
	})

	t.Run("a non-member gets 404, never 403", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodGet, channelPath(""), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("an org admin who is not a member gets the same 404", func(t *testing.T) {
		t.Parallel()
		admin := fixtureUser()
		admin.IsAdmin = true
		store := channelStore(admin, fixtureChannel(), false)
		rec := do(t, store, request(http.MethodGet, channelPath(""), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("a channel id that is not a uuid is 400", func(t *testing.T) {
		t.Parallel()
		// channelForUser stays unwired: the router rejects the path before
		// any handler runs, which is the contract's 400 on this route.
		rec := do(t, authedStore(fixtureUser()),
			request(http.MethodGet, "/api/v1/channels/not-a-uuid", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("an unknown channel is the same answer as a hidden one", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.channelForUser = func(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, error) {
			return storage.Channel{}, storage.ErrChannelNotFound
		}
		unknown := do(t, store, request(http.MethodGet, channelPath(""), "", withSessionCookie("tok")))
		hidden := do(t, channelStore(fixtureUser(), fixtureChannel(), false),
			request(http.MethodGet, channelPath(""), "", withSessionCookie("tok")))
		assertIdentical(t, "unknown vs hidden channel", unknown, hidden)
	})
}

func TestListChannels(t *testing.T) {
	t.Parallel()

	t.Run("returns the caller's sidebar", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotUser uuid.UUID
		store.listChannelsForUser = func(_ context.Context, userID uuid.UUID, _ storage.ListChannelsParams) ([]storage.Channel, error) {
			gotUser = userID
			return []storage.Channel{fixtureChannel()}, nil
		}

		rec := do(t, store, request(http.MethodGet, "/api/v1/channels", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID {
			t.Errorf("storage scoped to %s, want the authenticated caller %s", gotUser, fixtureUser().ID)
		}

		var page api.ChannelPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not a ChannelPage: %v", err)
		}
		if len(page.Channels) != 1 {
			t.Fatalf("page has %d channels, want 1", len(page.Channels))
		}
		if page.NextCursor != nil {
			t.Error("page has a next_cursor although the set is exhausted")
		}
	})

	t.Run("pages with a keyset cursor", func(t *testing.T) {
		t.Parallel()
		all := make([]storage.Channel, 3)
		base := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
		for i := range all {
			all[i] = fixtureChannel()
			all[i].ID = uuid.MustParse(fmt.Sprintf("%08d-0000-0000-0000-000000000000", i+1))
			all[i].CreatedAt = base.Add(time.Duration(i) * time.Second)
		}

		store := authedStore(fixtureUser())
		var gotParams []storage.ListChannelsParams
		store.listChannelsForUser = func(_ context.Context, _ uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error) {
			gotParams = append(gotParams, params)
			start := 0
			if params.After != nil {
				for i, ch := range all {
					if ch.ID == params.After.ID {
						start = i + 1
					}
				}
			}
			return all[start:min(start+params.Limit, len(all))], nil
		}
		handler := httpserver.Handler(store)

		first := doHandler(t, handler, request(http.MethodGet, "/api/v1/channels?limit=2", "", withSessionCookie("tok")))
		var page1 api.ChannelPage
		if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
			t.Fatalf("first page is not a ChannelPage: %v", err)
		}
		if len(page1.Channels) != 2 || page1.NextCursor == nil {
			t.Fatalf("first page: %d channels, cursor %v; want 2 and a cursor", len(page1.Channels), page1.NextCursor)
		}
		if gotParams[0].Limit != 3 {
			t.Errorf("storage asked for limit %d, want limit+1 = 3", gotParams[0].Limit)
		}

		second := doHandler(t, handler, request(http.MethodGet,
			"/api/v1/channels?limit=2&cursor="+url.QueryEscape(*page1.NextCursor), "", withSessionCookie("tok")))
		var page2 api.ChannelPage
		if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
			t.Fatalf("second page is not a ChannelPage: %v", err)
		}
		if len(page2.Channels) != 1 || page2.NextCursor != nil {
			t.Fatalf("second page: %d channels, cursor %v; want 1 and none", len(page2.Channels), page2.NextCursor)
		}
		if gotParams[1].After == nil {
			t.Fatal("second call reached storage without a cursor")
		}
		if gotParams[1].After.ID != all[1].ID || !gotParams[1].After.CreatedAt.Equal(all[1].CreatedAt) {
			t.Errorf("cursor decoded to (%v, %s), want (%v, %s)",
				gotParams[1].After.CreatedAt, gotParams[1].After.ID, all[1].CreatedAt, all[1].ID)
		}
	})

	t.Run("rejects malformed paging", func(t *testing.T) {
		t.Parallel()
		for _, query := range []string{"?limit=0", "?limit=201", "?limit=-1", "?cursor=%21%21%21"} {
			t.Run(query, func(t *testing.T) {
				t.Parallel()
				rec := do(t, authedStore(fixtureUser()),
					request(http.MethodGet, "/api/v1/channels"+query, "", withSessionCookie("tok")))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestCreateChannel(t *testing.T) {
	t.Parallel()

	t.Run("creates the channel and announces it to its creator", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var got storage.NewChannel
		store.createChannel = func(_ context.Context, nc storage.NewChannel) (storage.Channel, error) {
			got = nc
			return fixtureChannel(), nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, "/api/v1/channels",
			`{"slug":"deploys","kind":"private","topic":"ship it"}`, withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Slug != "deploys" || got.Kind != storage.ChannelKindPrivate || got.Topic != "ship it" {
			t.Errorf("storage got %+v, want the request's slug, kind and topic", got)
		}
		if got.CreatedBy != fixtureUser().ID {
			t.Errorf("created_by = %s, want the authenticated caller", got.CreatedBy)
		}
		if len(rt.channelCreated) != 1 {
			t.Fatalf("announced %d ChannelCreated events, want 1", len(rt.channelCreated))
		}
		if len(rt.channelCreated[0].userIDs) != 1 || rt.channelCreated[0].userIDs[0] != fixtureUser().ID {
			t.Errorf("announced to %v, want only the creator", rt.channelCreated[0].userIDs)
		}
	})

	t.Run("a taken slug is 409 channel_slug_taken", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.createChannel = func(context.Context, storage.NewChannel) (storage.Channel, error) {
			return storage.Channel{}, storage.ErrChannelSlugTaken
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/channels",
			`{"slug":"deploys","kind":"public"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "channel_slug_taken")
	})

	t.Run("rejects malformed requests", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			body string
		}{
			{"empty object", `{}`},
			{"slug too short", `{"slug":"a","kind":"public"}`},
			{"slug too long", `{"slug":"` + strings.Repeat("a", 65) + `","kind":"public"}`},
			{"slug uppercase", `{"slug":"Deploys","kind":"public"}`},
			{"slug starts with dash", `{"slug":"-deploys","kind":"public"}`},
			{"slug has a space", `{"slug":"two words","kind":"public"}`},
			{"slug has a dot", `{"slug":"a.b","kind":"public"}`},
			{"kind dm is opened through /api/v1/dms", `{"slug":"deploys","kind":"dm"}`},
			{"unknown kind", `{"slug":"deploys","kind":"secret"}`},
			{"topic too long", `{"slug":"deploys","kind":"public","topic":"` + strings.Repeat("t", 251) + `"}`},
			{"not json", `{`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				// createChannel stays unwired: a rejected request must never
				// reach storage.
				rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/channels",
					tt.body, withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestOpenDirectMessage(t *testing.T) {
	t.Parallel()

	t.Run("a new direct message is 201 and reaches both participants", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		var gotCaller, gotPeer uuid.UUID
		store.openDirectMessage = func(_ context.Context, caller, peer uuid.UUID) (storage.Channel, bool, error) {
			gotCaller, gotPeer = caller, peer
			return fixtureDM(), true, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, "/api/v1/dms",
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if gotCaller != fixtureUser().ID || gotPeer != peerUUID() {
			t.Errorf("storage got (%s, %s), want (caller, peer)", gotCaller, gotPeer)
		}
		if len(rt.channelCreated) != 1 || len(rt.channelCreated[0].userIDs) != 2 {
			t.Fatalf("announced %v, want one event to both participants", rt.channelCreated)
		}
	})

	t.Run("an existing direct message is 200 and announces nothing", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.openDirectMessage = func(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, bool, error) {
			return fixtureDM(), false, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPost, "/api/v1/dms",
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if len(rt.channelCreated) != 0 {
			t.Errorf("announced %d events for a direct message that already existed, want 0", len(rt.channelCreated))
		}
	})

	t.Run("a direct message with yourself is 400", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.openDirectMessage = func(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, bool, error) {
			return storage.Channel{}, false, storage.ErrDMWithSelf
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/dms",
			`{"user_id":"`+fixtureUser().ID.String()+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("an unknown peer is 404 user_not_found", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.openDirectMessage = func(context.Context, uuid.UUID, uuid.UUID) (storage.Channel, bool, error) {
			return storage.Channel{}, false, storage.ErrNotFound
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/dms",
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "user_not_found")
	})

	t.Run("rejects a missing or malformed user_id", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{`{}`, `{"user_id":"not-a-uuid"}`, `{`} {
			t.Run(body, func(t *testing.T) {
				t.Parallel()
				rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/dms",
					body, withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestUpdateChannel(t *testing.T) {
	t.Parallel()

	t.Run("a member sets the topic and the broadcast carries no caller counts", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var gotTopic string
		// UpdateChannelTopic has no caller in scope, so it answers with the
		// counts zeroed — exactly the row that may be broadcast.
		store.updateChannelTopic = func(_ context.Context, _ uuid.UUID, topic string) (storage.Channel, error) {
			gotTopic = topic
			ch := fixtureChannel()
			ch.Topic = topic
			ch.UnreadCount, ch.MentionCount, ch.LastReadMessageID = 0, 0, nil
			return ch, nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodPatch, channelPath(""),
			`{"topic":"new topic"}`, withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if gotTopic != "new topic" {
			t.Errorf("stored topic %q, want %q", gotTopic, "new topic")
		}

		// The response is the caller-scoped re-read, so it carries their counts.
		var got api.Channel
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not a Channel: %v", err)
		}
		if got.UnreadCount != 2 || got.MentionCount != 1 {
			t.Errorf("response counts = (%d, %d), want the caller's (2, 1)", got.UnreadCount, got.MentionCount)
		}

		if len(rt.channelUpdated) != 1 {
			t.Fatalf("announced %d ChannelUpdated events, want 1", len(rt.channelUpdated))
		}
		if rt.channelUpdated[0].UnreadCount != 0 || rt.channelUpdated[0].MentionCount != 0 {
			t.Errorf("broadcast carried the actor's counts (%d, %d) to every member",
				rt.channelUpdated[0].UnreadCount, rt.channelUpdated[0].MentionCount)
		}
		if rt.channelUpdated[0].Topic != "new topic" {
			t.Errorf("broadcast topic %q, want the new one", rt.channelUpdated[0].Topic)
		}
	})

	t.Run("a direct message has no topic: 400, not 403", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureDM(), true)
		store.updateChannelTopic = func(context.Context, uuid.UUID, string) (storage.Channel, error) {
			return storage.Channel{}, storage.ErrDMHasNoTopic
		}
		rec := do(t, store, request(http.MethodPatch, channelPath(""),
			`{"topic":"nope"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "invalid_request")
	})

	t.Run("a non-member gets 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodPatch, channelPath(""),
			`{"topic":"nope"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("rejects malformed requests", func(t *testing.T) {
		t.Parallel()
		for name, body := range map[string]string{
			"topic too long": `{"topic":"` + strings.Repeat("t", 251) + `"}`,
			"not json":       `{`,
			"wrong type":     `{"topic":42}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				// updateChannelTopic stays unwired: nothing must be stored.
				rec := do(t, memberStore(), request(http.MethodPatch, channelPath(""),
					body, withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestListChannelMembers(t *testing.T) {
	t.Parallel()

	t.Run("returns the public face of each member", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.listChannelMembers = func(context.Context, uuid.UUID, storage.ListChannelMembersParams) ([]storage.User, error) {
			member := fixtureUser()
			member.Email = ptr("member@example.com")
			return []storage.User{member}, nil
		}

		rec := do(t, store, request(http.MethodGet, channelPath("/members"), "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var page api.MemberPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("body is not a MemberPage: %v", err)
		}
		if len(page.Members) != 1 || page.Members[0].Username != "member" {
			t.Fatalf("page = %+v, want the one member", page.Members)
		}
		for _, leaked := range []string{"member@example.com", "argon2id", "is_admin", "must_change_password"} {
			if strings.Contains(rec.Body.String(), leaked) {
				t.Errorf("member list leaked %q: %s", leaked, rec.Body.String())
			}
		}
	})

	t.Run("pages by username", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var gotParams []storage.ListChannelMembersParams
		store.listChannelMembers = func(_ context.Context, _ uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error) {
			gotParams = append(gotParams, params)
			if params.After != nil {
				return []storage.User{fixtureUser()}, nil
			}
			first, second := fixtureUser(), fixtureUser()
			second.ID = peerUUID()
			second.Username = "zoe"
			// One row beyond the page proves a next page exists.
			return []storage.User{first, second, fixtureUser()}, nil
		}
		handler := httpserver.Handler(store)

		first := doHandler(t, handler, request(http.MethodGet, channelPath("/members")+"?limit=2", "", withSessionCookie("tok")))
		var page1 api.MemberPage
		if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
			t.Fatalf("first page is not a MemberPage: %v", err)
		}
		if len(page1.Members) != 2 || page1.NextCursor == nil {
			t.Fatalf("first page: %d members, cursor %v; want 2 and a cursor", len(page1.Members), page1.NextCursor)
		}

		doHandler(t, handler, request(http.MethodGet,
			channelPath("/members")+"?cursor="+url.QueryEscape(*page1.NextCursor), "", withSessionCookie("tok")))
		if len(gotParams) != 2 || gotParams[1].After == nil {
			t.Fatalf("second call reached storage with %+v, want a cursor", gotParams)
		}
		if gotParams[1].After.Username != "zoe" || gotParams[1].After.UserID != peerUUID() {
			t.Errorf("cursor decoded to (%q, %s), want (zoe, %s)",
				gotParams[1].After.Username, gotParams[1].After.UserID, peerUUID())
		}
	})

	t.Run("a non-member gets 404", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodGet, channelPath("/members"), "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("rejects malformed paging", func(t *testing.T) {
		t.Parallel()
		for _, query := range []string{"?limit=0", "?limit=101", "?cursor=%21%21%21"} {
			t.Run(query, func(t *testing.T) {
				t.Parallel()
				rec := do(t, memberStore(), request(http.MethodGet, channelPath("/members")+query, "", withSessionCookie("tok")))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestAddChannelMember(t *testing.T) {
	t.Parallel()

	t.Run("any member may invite anybody", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		var gotChannel, gotUser, gotAddedBy uuid.UUID
		store.addChannelMember = func(_ context.Context, channelID, userID, addedBy uuid.UUID) error {
			gotChannel, gotUser, gotAddedBy = channelID, userID, addedBy
			return nil
		}

		rec := do(t, store, request(http.MethodPost, channelPath("/members"),
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotChannel != channelUUID() || gotUser != peerUUID() || gotAddedBy != fixtureUser().ID {
			t.Errorf("storage got (%s, %s, %s), want (channel, peer, caller)", gotChannel, gotUser, gotAddedBy)
		}
	})

	t.Run("a direct message's membership is fixed: 400", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureDM(), true)
		store.addChannelMember = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			return storage.ErrDMMembershipFixed
		}
		rec := do(t, store, request(http.MethodPost, channelPath("/members"),
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "dm_membership_fixed")
	})

	t.Run("an unknown user is 404 user_not_found", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.addChannelMember = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			return storage.ErrNotFound
		}
		rec := do(t, store, request(http.MethodPost, channelPath("/members"),
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "user_not_found")
	})

	t.Run("a channel that vanished mid-request is 404 channel_not_found", func(t *testing.T) {
		t.Parallel()
		store := memberStore()
		store.addChannelMember = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
			return storage.ErrChannelNotFound
		}
		rec := do(t, store, request(http.MethodPost, channelPath("/members"),
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("a non-member cannot invite, and learns nothing", func(t *testing.T) {
		t.Parallel()
		// addChannelMember stays unwired: the refusal must happen first.
		store := channelStore(fixtureUser(), fixtureChannel(), false)
		rec := do(t, store, request(http.MethodPost, channelPath("/members"),
			`{"user_id":"`+testPeerID+`"}`, withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("rejects a missing or malformed user_id", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{`{}`, `{"user_id":"not-a-uuid"}`, `{`} {
			t.Run(body, func(t *testing.T) {
				t.Parallel()
				rec := do(t, memberStore(), request(http.MethodPost, channelPath("/members"),
					body, withSessionCookie("tok"), withCSRF()))
				wantError(t, rec, http.StatusBadRequest, "invalid_request")
			})
		}
	})
}

func TestRemoveChannelMember(t *testing.T) {
	t.Parallel()

	// leavePath removes the fixture user themselves; removePath removes the peer.
	leavePath := channelPath("/members/" + fixtureUser().ID.String())
	removePath := channelPath("/members/" + testPeerID)

	t.Run("leaving is always allowed to a member", func(t *testing.T) {
		t.Parallel()
		// A plain member of a channel somebody else created.
		ch := fixtureChannel()
		ch.CreatedBy = ptr(peerUUID())
		store := channelStore(fixtureUser(), ch, true)
		var gotUser uuid.UUID
		store.removeChannelMember = func(_ context.Context, _, userID uuid.UUID) error {
			gotUser = userID
			return nil
		}
		rt := &recordingRealtime{}

		rec := doRealtime(t, store, rt, request(http.MethodDelete, leavePath, "", withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
		if gotUser != fixtureUser().ID {
			t.Errorf("removed %s, want the caller", gotUser)
		}
		if len(rt.channelRemoved) != 1 || rt.channelRemoved[0].userID != fixtureUser().ID {
			t.Errorf("announced %v, want ChannelRemoved to the departing user", rt.channelRemoved)
		}
	})

	t.Run("removing somebody else needs the creator or an admin member", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name       string
			creator    uuid.UUID
			admin      bool
			wantStatus int
			wantCode   string
		}{
			{"the channel's creator", fixtureUser().ID, false, http.StatusNoContent, ""},
			{"an admin who is a member", peerUUID(), true, http.StatusNoContent, ""},
			{"a plain member", peerUUID(), false, http.StatusForbidden, "forbidden"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				user := fixtureUser()
				user.IsAdmin = tt.admin
				ch := fixtureChannel()
				ch.CreatedBy = &tt.creator
				store := channelStore(user, ch, true)
				store.removeChannelMember = func(context.Context, uuid.UUID, uuid.UUID) error { return nil }

				rec := do(t, store, request(http.MethodDelete, removePath, "", withSessionCookie("tok"), withCSRF()))
				if tt.wantCode != "" {
					wantError(t, rec, tt.wantStatus, tt.wantCode)
					return
				}
				if rec.Code != tt.wantStatus {
					t.Fatalf("got status %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
				}
			})
		}
	})

	t.Run("an admin who is not a member gets 404, not 403", func(t *testing.T) {
		t.Parallel()
		admin := fixtureUser()
		admin.IsAdmin = true
		store := channelStore(admin, fixtureChannel(), false)
		rec := do(t, store, request(http.MethodDelete, removePath, "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusNotFound, "channel_not_found")
	})

	t.Run("a direct message's membership is fixed: 400", func(t *testing.T) {
		t.Parallel()
		store := channelStore(fixtureUser(), fixtureDM(), true)
		store.removeChannelMember = func(context.Context, uuid.UUID, uuid.UUID) error {
			return storage.ErrDMMembershipFixed
		}
		rec := do(t, store, request(http.MethodDelete, leavePath, "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusBadRequest, "dm_membership_fixed")
	})
}

// ptr is the address of a value, for the optional fields of a fixture.
func ptr[T any](v T) *T { return &v }
