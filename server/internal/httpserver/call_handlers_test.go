package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/livekit"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/calls/callstest"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The call surface: the two channel endpoints, the webhook receiver, and the
// removal hook on membership loss.
//
// What is NOT here: the grant shape of a minted ticket, which is asserted
// against the decoded claims in internal/calls, and the authorization matrix,
// which runs both endpoints against a real database in internal/authztest.
// What is here is the boundary between them — that a stranger is refused
// before the media plane is ever consulted, that an unsigned webhook changes
// nothing, and that a removal really ejects.

const (
	callTestKey    = "handler-test-key"
	callTestSecret = "a livekit handler test secret, long enough"
)

// mediaServer builds a calls service against a fake media server.
func mediaServer(t *testing.T) (*calls.Service, *callstest.Server) {
	t.Helper()
	lk := callstest.New(t)
	return calls.New(callTestKey, callTestSecret, lk.URL, nil), lk
}

// TestCallTokenRefusesANonMember is the rule the whole surface rests on: a
// stranger to a channel gets the channel's 404, never a 403 and never the
// instance's 503. A 403 would confirm the channel exists; a 503 would say
// something about the instance to somebody who is owed nothing. An org admin
// who is not a member is a stranger, exactly as everywhere else.
func TestCallTokenRefusesANonMember(t *testing.T) {
	t.Parallel()

	admin := fixtureUser()
	admin.IsAdmin = true
	for name, user := range map[string]storage.User{
		"a plain non-member":   fixtureUser(),
		"an org admin outside": admin,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := channelStore(user, fixtureChannel(), false)
			svc, lk := mediaServer(t)

			rec := doCalls(t, store, svc, request(http.MethodPost,
				channelPath("/call/token"), "", withSessionCookie("tok"), withCSRF()))

			wantError(t, rec, http.StatusNotFound, "channel_not_found")
			if strings.Contains(rec.Body.String(), "calls_unavailable") {
				t.Error("the refusal leaked the instance's call configuration")
			}
			lk.NoRemoval(t)
		})
	}
}

// TestChannelCallRefusesANonMember is the same rule on the state read.
func TestChannelCallRefusesANonMember(t *testing.T) {
	t.Parallel()

	store := channelStore(fixtureUser(), fixtureChannel(), false)
	svc, _ := mediaServer(t)

	rec := doCalls(t, store, svc, request(http.MethodGet,
		channelPath("/call"), "", withSessionCookie("tok")))

	wantError(t, rec, http.StatusNotFound, "channel_not_found")
}

// TestCallTokenMintsATicketForAMember pins the success: a member gets a 201
// carrying a ticket for this channel's room and nothing else.
func TestCallTokenMintsATicketForAMember(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	rec := doCalls(t, memberStore(), svc, request(http.MethodPost,
		channelPath("/call/token"), "", withSessionCookie("tok"), withCSRF()))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var ticket api.CallToken
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("body %q is not a CallToken: %v", rec.Body.String(), err)
	}
	if ticket.Room != "chan-"+testChannelID {
		t.Errorf("room = %q, want the fixture channel's room", ticket.Room)
	}
	if ticket.Token == "" {
		t.Error("no token in the ticket")
	}
	if lifetime := time.Until(ticket.ExpiresAt); lifetime <= 0 || lifetime > calls.JoinTTL {
		t.Errorf("ticket expires in %s, want within %s", lifetime, calls.JoinTTL)
	}
	// The media server's address is never in the answer: the signal endpoint
	// is same-origin, so a client derives it (openapi.yaml, createCallToken).
	if strings.Contains(rec.Body.String(), "http") {
		t.Errorf("the ticket carries a URL: %s", rec.Body.String())
	}
}

// TestCallsWithoutAMediaServer pins the honest answers of an install that has
// no media plane: the ticket is a 503 with a code the UI can localize, and
// the state read is a well-formed "there is no call" rather than an error a
// client would have to special-case.
func TestCallsWithoutAMediaServer(t *testing.T) {
	t.Parallel()

	t.Run("the ticket is refused", func(t *testing.T) {
		t.Parallel()
		rec := doCalls(t, memberStore(), nil, request(http.MethodPost,
			channelPath("/call/token"), "", withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusServiceUnavailable, "calls_unavailable")
	})

	t.Run("the state read says there is no call", func(t *testing.T) {
		t.Parallel()
		rec := doCalls(t, memberStore(), nil, request(http.MethodGet,
			channelPath("/call"), "", withSessionCookie("tok")))

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var call api.ChannelCall
		if err := json.Unmarshal(rec.Body.Bytes(), &call); err != nil {
			t.Fatalf("body %q is not a ChannelCall: %v", rec.Body.String(), err)
		}
		if call.Active || call.Participants != nil || call.StartedAt != nil {
			t.Errorf("call = %+v; want an inactive call with nothing else", call)
		}
	})
}

// TestChannelCallReadsLiveState pins that the answer comes from the media
// server rather than from a table, and that the people in it are named by
// this server — LiveKit never sees a user, so a participant identity is an
// account id and the username is read back here.
func TestChannelCallReadsLiveState(t *testing.T) {
	t.Parallel()

	store := memberStore()
	store.userByID = func(context.Context, uuid.UUID) (storage.User, error) { return peerUser(), nil }

	svc, lk := mediaServer(t)
	joined := time.Now().Add(-time.Minute).Truncate(time.Second)
	lk.Participants = []*livekit.ParticipantInfo{{
		Identity: peerUUID().String(),
		JoinedAt: joined.Unix(),
		Tracks:   []*livekit.TrackInfo{{Source: livekit.TrackSource_SCREEN_SHARE}},
	}}

	rec := doCalls(t, store, svc, request(http.MethodGet,
		channelPath("/call"), "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var call api.ChannelCall
	if err := json.Unmarshal(rec.Body.Bytes(), &call); err != nil {
		t.Fatalf("body %q is not a ChannelCall: %v", rec.Body.String(), err)
	}
	if !call.Active || call.Participants == nil || len(*call.Participants) != 1 {
		t.Fatalf("call = %+v; want one participant in an active call", call)
	}
	p := (*call.Participants)[0]
	if p.User.Id != peerUUID() || p.User.Username != peerUser().Username {
		t.Errorf("participant = %+v, want the peer named from storage", p.User)
	}
	if p.ScreenSharing == nil || !*p.ScreenSharing {
		t.Error("the screen share did not survive")
	}
	if call.StartedAt == nil || !call.StartedAt.Equal(joined.UTC()) {
		t.Errorf("started_at = %v, want the first join at %s", call.StartedAt, joined.UTC())
	}
	// A UserSummary and nothing more: no email, no role, no password state.
	if body := rec.Body.String(); strings.Contains(body, peerUser().PasswordHash) ||
		strings.Contains(body, "is_admin") || strings.Contains(body, "email") {
		t.Errorf("the participant list leaked account state: %s", body)
	}
}

// TestCallWebhookRefusesAnUnsignedDelivery is the receiver's authentication.
// It sits outside the contract router with no session, no cookie and no CSRF
// token, so the signature over the body is the entire credential — and a
// refusal must change nothing and say nothing.
func TestCallWebhookRefusesAnUnsignedDelivery(t *testing.T) {
	t.Parallel()

	body := `{"event":"participant_joined","room":{"name":"chan-` + testChannelID + `"}}`
	deliveries := map[string]*http.Request{
		"no signature at all": httptest.NewRequest(http.MethodPost,
			"/livekit/webhook", strings.NewReader(body)),
		"signed with another secret": callstest.SignedWebhook(t, callTestKey,
			"a secret this instance does not hold, but long", body),
		"a body swapped after signing": func() *http.Request {
			signed := callstest.SignedWebhook(t, callTestKey, callTestSecret, body)
			signed.Body = io.NopCloser(strings.NewReader(
				strings.Replace(body, "participant_joined", "room_finished", 1)))
			return signed
		}(),
	}

	for name, req := range deliveries {
		t.Run(name, func(t *testing.T) {
			svc, lk := mediaServer(t)
			lk.Participants = []*livekit.ParticipantInfo{{Identity: peerUUID().String(), JoinedAt: 1}}
			rt := &recordingRealtime{}

			rec := doHandler(t, httpserver.Handler(memberStore(),
				httpserver.WithCalls(svc), httpserver.WithRealtime(rt)), req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("got status %d, want 404", rec.Code)
			}
			if events := rt.callEvents(); len(events) != 0 {
				t.Errorf("an unverified delivery announced %+v", events)
			}
		})
	}
}

// TestCallWebhookAnnouncesFromAFreshRead is what makes the receiver
// idempotent and order-proof: the delivery is a hint, and the announcement is
// decided by reading live state. The same delivery twice announces the second
// one as an update rather than a second start, and a delivery for an empty
// room ends the call whatever the event said it was.
func TestCallWebhookAnnouncesFromAFreshRead(t *testing.T) {
	t.Parallel()

	store := memberStore()
	store.userByID = func(context.Context, uuid.UUID) (storage.User, error) { return peerUser(), nil }

	svc, lk := mediaServer(t)
	rt := &recordingRealtime{}
	handler := httpserver.Handler(store, httpserver.WithCalls(svc), httpserver.WithRealtime(rt))

	// Every delivery below carries the SAME event name on purpose. What
	// decides the announcement is the room, not the event.
	deliver := func(t *testing.T) {
		t.Helper()
		body := `{"event":"participant_joined","room":{"name":"chan-` + testChannelID + `"}}`
		rec := doHandler(t, handler, callstest.SignedWebhook(t, callTestKey, callTestSecret, body))
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook answered %d, want 200", rec.Code)
		}
	}

	lk.Participants = []*livekit.ParticipantInfo{{Identity: peerUUID().String(), JoinedAt: 100}}
	deliver(t)
	deliver(t) // a duplicate: at-least-once delivery
	lk.Participants = nil
	deliver(t)
	deliver(t) // a late, out-of-order delivery for a call that already ended

	got := rt.callEvents()
	want := []string{"call_started", "call_updated", "call_ended"}
	if len(got) != len(want) {
		t.Fatalf("announced %+v, want exactly %v", got, want)
	}
	for i, name := range want {
		if got[i].event != name {
			t.Errorf("event %d = %s, want %s", i, got[i].event, name)
		}
		if got[i].channelID != channelUUID() {
			t.Errorf("event %d named channel %s, want %s", i, got[i].channelID, channelUUID())
		}
	}
	if got[0].startedBy != peerUUID() {
		t.Errorf("call_started names %s, want the first participant %s", got[0].startedBy, peerUUID())
	}
	if len(got[0].participants) != 1 || got[0].participants[0].User.Id != peerUUID() {
		t.Errorf("call_started carried %+v, want the one participant", got[0].participants)
	}
}

// TestRemovingAMemberEjectsThemFromTheCall is ADR 005's first removal hook: a
// join ticket outlives the membership that justified it, and this is what
// bounds the already-joined half. The removal itself must not wait for the
// ejection or fail with it — it has already committed.
func TestRemovingAMemberEjectsThemFromTheCall(t *testing.T) {
	t.Parallel()

	store := memberStore()
	store.removeChannelMember = func(context.Context, uuid.UUID, uuid.UUID) error { return nil }
	store.userByID = func(context.Context, uuid.UUID) (storage.User, error) { return peerUser(), nil }
	svc, lk := mediaServer(t)

	rec := doCalls(t, store, svc, request(http.MethodDelete,
		channelPath("/members/"+testPeerID), "", withSessionCookie("tok"), withCSRF()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	removed := lk.AwaitRemoval(t)
	if removed.GetRoom() != "chan-"+testChannelID {
		t.Errorf("ejected from %q, want the channel's room", removed.GetRoom())
	}
	if removed.GetIdentity() != testPeerID {
		t.Errorf("ejected %q, want the removed member %s", removed.GetIdentity(), testPeerID)
	}
}

// TestFailedRemovalEjectsNobody pins the other half of the hook: the ejection
// is after-commit work, so a removal the store refused must not eject anybody.
func TestFailedRemovalEjectsNobody(t *testing.T) {
	t.Parallel()

	store := memberStore()
	store.removeChannelMember = func(context.Context, uuid.UUID, uuid.UUID) error {
		return storage.ErrLastMember
	}
	svc, lk := mediaServer(t)

	rec := doCalls(t, store, svc, request(http.MethodDelete,
		channelPath("/members/"+testPeerID), "", withSessionCookie("tok"), withCSRF()))
	wantError(t, rec, http.StatusBadRequest, "last_member")
	lk.NoRemoval(t)
}

// TestInstanceReportsCalls pins the flag the UI omits call controls from. It
// follows configuration, like sso.enabled beside it: the control exists when
// the door does.
func TestInstanceReportsCalls(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	for name, tc := range map[string]struct {
		svc  *calls.Service
		want bool
	}{
		"configured":   {svc: svc, want: true},
		"unconfigured": {svc: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doCalls(t, &fakeStore{}, tc.svc,
				request(http.MethodGet, "/api/v1/instance", ""))

			var info api.InstanceInfo
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
				t.Fatalf("body %q is not InstanceInfo: %v", rec.Body.String(), err)
			}
			if info.Calls == nil || *info.Calls != tc.want {
				t.Errorf("calls = %v, want %v", info.Calls, tc.want)
			}
		})
	}
}

// doCalls serves req against store with svc as the media plane. A nil svc is
// an install with calls off, which is a supported one.
func doCalls(t *testing.T, store httpserver.Store, svc *calls.Service, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	return doHandler(t, httpserver.Handler(store, httpserver.WithCalls(svc)), req)
}
