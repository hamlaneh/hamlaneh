package calls

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/livekit"

	"github.com/hamlaneh/hamlaneh/server/internal/calls/callstest"
)

const (
	testKey    = "hamlaneh-test-key"
	testSecret = "a livekit test secret long enough to be one"
)

// TestJoinTokenGrantsNothingElse is the test the whole grant design exists
// for. It decodes a real minted ticket and asserts what is NOT in it.
//
// The absences are the security property (ADR 005): roomList absent is why
// deriving a room name from a channel id is safe, roomAdmin absent is why no
// participant can eject another, and canPublishData denied is why chat stays
// on the one write path. A grant nobody notices creeping in is exactly the
// failure this catches — so the assertion is over the decoded claim map,
// which is what LiveKit reads, rather than over the struct we built.
func TestJoinTokenGrantsNothingElse(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, callstest.New(t).URL, nil)
	channelID, userID := uuid.New(), uuid.New()

	ticket, err := svc.JoinToken(t.Context(), channelID, userID, "Sara Ahmadi")
	if err != nil {
		t.Fatalf("mint join ticket: %v", err)
	}
	if ticket.Room != "chan-"+channelID.String() {
		t.Errorf("room = %q, want chan-%s", ticket.Room, channelID)
	}

	claims := decodeClaims(t, ticket.Token)
	if claims["iss"] != testKey {
		t.Errorf("iss = %v, want the API key", claims["iss"])
	}
	if claims["sub"] != userID.String() {
		t.Errorf("sub = %v, want the account id", claims["sub"])
	}

	video, ok := claims["video"].(map[string]any)
	if !ok {
		t.Fatalf("no video grant in %v", claims)
	}

	// What must be there.
	for key, want := range map[string]any{
		"roomJoin":     true,
		"room":         ticket.Room,
		"canPublish":   true,
		"canSubscribe": true,
	} {
		if got := video[key]; got != want {
			t.Errorf("video[%q] = %v, want %v", key, got, want)
		}
	}

	// canPublishData is the one grant that cannot be denied by omission:
	// LiveKit reads an absent canPublishData as canPublish. So it must be
	// PRESENT and false, and a mutation that drops it turns this red.
	dataGrant, present := video["canPublishData"]
	if !present {
		t.Error("canPublishData is absent, which LiveKit reads as granted")
	} else if dataGrant != false {
		t.Errorf("canPublishData = %v, want false", dataGrant)
	}

	// What must NOT be there. Each of these is a capability ADR 005 refuses a
	// participant, and each is denied by absence — the omitempty on
	// auth.VideoGrant means a false grant never reaches the wire.
	for _, absent := range []string{
		"roomList", "roomAdmin", "roomCreate", "roomRecord",
		"ingressAdmin", "hidden", "recorder", "agent",
		"canUpdateOwnMetadata", "canSubscribeMetrics", "canManageAgentSession",
		"destinationRoom", "canPublishSources",
	} {
		if got, present := video[absent]; present {
			t.Errorf("video[%q] = %v; it must be absent", absent, got)
		}
	}
	// And no other grant family at all: sip, agent, inference, observability,
	// roomConfig and roomPreset are all doors this ticket does not open.
	for _, absent := range []string{"sip", "agent", "inference", "observability", "roomConfig", "roomPreset"} {
		if _, present := claims[absent]; present {
			t.Errorf("claim %q is present; it must be absent", absent)
		}
	}
}

// TestJoinTokenExpires pins the two-minute ticket. The number is load-bearing
// (ADR 005) and is only honest because LiveKit re-mints a connected
// participant's token over the signal channel — see JoinTTL's comment.
func TestJoinTokenExpires(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, callstest.New(t).URL, nil)
	ticket, err := svc.JoinToken(t.Context(), uuid.New(), uuid.New(), "Sara")
	if err != nil {
		t.Fatalf("mint join ticket: %v", err)
	}

	claims := decodeClaims(t, ticket.Token)
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("no exp claim in %v", claims)
	}
	if lifetime := time.Until(time.Unix(int64(exp), 0)); lifetime > JoinTTL || lifetime < JoinTTL-time.Minute {
		t.Errorf("ticket lifetime %s, want about %s", lifetime, JoinTTL)
	}
}

// TestNotConfiguredIsUsable pins that a nil service is a supported install
// rather than a panic waiting to happen: it reports calls off, has no state
// to read, and every hook on it is a no-op.
func TestNotConfiguredIsUsable(t *testing.T) {
	t.Parallel()

	var svc *Service
	if svc.Enabled() {
		t.Error("a nil service reports calls enabled")
	}
	if _, err := svc.JoinToken(context.Background(), uuid.New(), uuid.New(), "x"); err == nil {
		t.Error("a nil service minted a ticket")
	}
	if _, err := svc.GuestToken(context.Background(), uuid.New(), "x"); err == nil {
		t.Error("a nil service minted a guest ticket")
	}
	if active, err := svc.ActiveConferences(context.Background(), []uuid.UUID{uuid.New()}); err != nil || len(active) != 0 {
		t.Errorf("nil service active conferences = %v, %v; want none and no error", active, err)
	}
	// No media plane means no meeting to end, which is success rather than a
	// failure a revocation would have to report.
	if err := svc.CloseConference(context.Background(), uuid.New()); err != nil {
		t.Errorf("nil service close = %v, want no error", err)
	}
	state, err := svc.State(context.Background(), uuid.New())
	if err != nil || state.Active {
		t.Errorf("nil service state = %+v, %v; want an inactive call and no error", state, err)
	}
	svc.Eject(context.Background(), uuid.New(), uuid.New())
	svc.EjectEverywhere(context.Background(), uuid.New())
	if got := svc.Transition(uuid.New(), true); got != TransitionNone {
		t.Errorf("nil service transition = %v, want none", got)
	}
}

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name                string
		key, secret, url    string
		wantService, wantOK bool
	}{
		{name: "unset is calls off", wantOK: true},
		{
			name: "all three configure", key: testKey, secret: testSecret,
			url: "http://livekit:7880", wantService: true, wantOK: true,
		},
		{name: "no secret stops startup", key: testKey, url: "http://livekit:7880"},
		{name: "no key stops startup", secret: testSecret, url: "http://livekit:7880"},
		{name: "no url stops startup", key: testKey, secret: testSecret},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIKey, tc.key)
			t.Setenv(EnvAPISecret, tc.secret)
			t.Setenv(EnvURL, tc.url)

			svc, err := FromEnv()
			if (err == nil) != tc.wantOK {
				t.Fatalf("error = %v, want ok=%v", err, tc.wantOK)
			}
			if (svc != nil) != tc.wantService {
				t.Errorf("service = %v, want configured=%v", svc, tc.wantService)
			}
		})
	}
}

func TestRoomNaming(t *testing.T) {
	t.Parallel()

	channelID := uuid.New()
	got, ours := ChannelFor(RoomFor(channelID))
	if !ours || got != channelID {
		t.Errorf("round trip = %v, %v; want %v, true", got, ours, channelID)
	}

	// Anything that is not one of ours must be refused, so a webhook for a
	// room this instance did not name can never be mapped onto a channel.
	for _, room := range []string{
		"", "chan-", "chan-not-a-uuid", channelID.String(),
		"conf-" + channelID.String(), "xchan-" + channelID.String(),
	} {
		if _, ours := ChannelFor(room); ours {
			t.Errorf("ChannelFor(%q) claimed the room as ours", room)
		}
	}
}

// TestGuestTokenGrantsNothingElse is the member ticket's test asked of the
// wider door. A guest has no account on this instance, so every absence
// matters more here — and the assertion is deliberately the same one, because
// the two tickets come from one minter and any divergence is a bug.
func TestGuestTokenGrantsNothingElse(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, callstest.New(t).URL, nil)
	conferenceID := uuid.New()

	ticket, err := svc.GuestToken(t.Context(), conferenceID, "whoever they say they are")
	if err != nil {
		t.Fatalf("mint guest ticket: %v", err)
	}
	if ticket.Room != "conf-"+conferenceID.String() {
		t.Errorf("room = %q, want conf-%s", ticket.Room, conferenceID)
	}
	if lifetime := time.Until(ticket.ExpiresAt); lifetime > JoinTTL || lifetime < JoinTTL-time.Minute {
		t.Errorf("guest ticket lifetime %s, want the member's %s", lifetime, JoinTTL)
	}

	claims := decodeClaims(t, ticket.Token)
	video, ok := claims["video"].(map[string]any)
	if !ok {
		t.Fatalf("no video grant in %v", claims)
	}
	for key, want := range map[string]any{
		"roomJoin":     true,
		"room":         ticket.Room,
		"canPublish":   true,
		"canSubscribe": true,
	} {
		if got := video[key]; got != want {
			t.Errorf("video[%q] = %v, want %v", key, got, want)
		}
	}
	dataGrant, present := video["canPublishData"]
	if !present {
		t.Error("canPublishData is absent, which LiveKit reads as granted")
	} else if dataGrant != false {
		t.Errorf("canPublishData = %v, want false", dataGrant)
	}
	for _, absent := range []string{
		"roomList", "roomAdmin", "roomCreate", "roomRecord",
		"ingressAdmin", "hidden", "recorder", "agent",
		"canUpdateOwnMetadata", "canSubscribeMetrics", "canManageAgentSession",
		"destinationRoom", "canPublishSources",
	} {
		if got, present := video[absent]; present {
			t.Errorf("video[%q] = %v; it must be absent", absent, got)
		}
	}
	for _, absent := range []string{"sip", "agent", "inference", "observability", "roomConfig", "roomPreset"} {
		if _, present := claims[absent]; present {
			t.Errorf("claim %q is present; it must be absent", absent)
		}
	}
}

// TestGuestIdentityIsNotAnAccount is the property that keeps a guest a guest.
//
// Everything that resolves a participant back to a person parses the identity
// as an account id — stateOf, and both ejection hooks — so a guest identity
// that could parse as one would put an anonymous visitor into a channel
// call's participant list, or make them ejectable as somebody else. It is
// also fresh per join, so a ticket somebody saw names nobody the next time.
func TestGuestIdentityIsNotAnAccount(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, callstest.New(t).URL, nil)
	conferenceID := uuid.New()

	seen := map[string]bool{}
	for range 5 {
		ticket, err := svc.GuestToken(t.Context(), conferenceID, "Guest")
		if err != nil {
			t.Fatalf("mint guest ticket: %v", err)
		}
		identity, ok := decodeClaims(t, ticket.Token)["sub"].(string)
		if !ok {
			t.Fatal("no sub claim in the guest ticket")
		}
		if _, err := uuid.Parse(identity); err == nil {
			t.Errorf("guest identity %q parses as an account id", identity)
		}
		if seen[identity] {
			t.Errorf("guest identity %q was minted twice", identity)
		}
		seen[identity] = true
	}

	// And the participant reader drops it rather than inventing an account
	// for it, which is what the parse above is protecting.
	if st := stateOf([]*livekit.ParticipantInfo{{Identity: "guest-" + uuid.NewString(), JoinedAt: 1}}); st.Active {
		t.Errorf("a guest was reported as a channel-call participant: %+v", st)
	}
}

// TestConferenceRoomNaming pins the second namespace: the two prefixes cannot
// be confused, so a conference id can never be spelled as a channel and a
// channel webhook can never be mapped onto a conference.
func TestConferenceRoomNaming(t *testing.T) {
	t.Parallel()

	conferenceID := uuid.New()
	got, ours := ConferenceFor(ConferenceRoomFor(conferenceID))
	if !ours || got != conferenceID {
		t.Errorf("round trip = %v, %v; want %v, true", got, ours, conferenceID)
	}
	if _, ours := ChannelFor(ConferenceRoomFor(conferenceID)); ours {
		t.Error("a conference room was claimed as a channel's")
	}
	if _, ours := ConferenceFor(RoomFor(conferenceID)); ours {
		t.Error("a channel room was claimed as a conference's")
	}
	for _, room := range []string{"", "conf-", "conf-not-a-uuid", conferenceID.String()} {
		if _, ours := ConferenceFor(room); ours {
			t.Errorf("ConferenceFor(%q) claimed the room as ours", room)
		}
	}
}

// TestActiveConferences pins the read behind `active`: a room with nobody in
// it is not a meeting, and rooms this server did not ask about are not
// answered for.
func TestActiveConferences(t *testing.T) {
	t.Parallel()

	lk := callstest.New(t)
	svc := New(testKey, testSecret, lk.URL, nil)

	busy, quiet, unasked := uuid.New(), uuid.New(), uuid.New()
	lk.Rooms = []string{
		ConferenceRoomFor(busy), ConferenceRoomFor(quiet), ConferenceRoomFor(unasked),
	}
	// A room lingers for a few minutes after its last participant leaves, so
	// "the room exists" and "somebody is in it" are different questions.
	lk.Empty = map[string]bool{ConferenceRoomFor(quiet): true}

	active, err := svc.ActiveConferences(t.Context(), []uuid.UUID{busy, quiet})
	if err != nil {
		t.Fatalf("ActiveConferences: %v", err)
	}
	if !active[busy] {
		t.Error("a conference somebody is in reads as inactive")
	}
	if active[quiet] {
		t.Error("an empty room reads as a live meeting")
	}
	if active[unasked] {
		t.Error("a conference nobody asked about was answered for")
	}

	// An empty ask costs no round trip and is not an error.
	if got, err := svc.ActiveConferences(t.Context(), nil); err != nil || len(got) != 0 {
		t.Errorf("ActiveConferences(nil) = %v, %v; want an empty answer", got, err)
	}
}

// TestCloseConferenceEndsTheMeeting is the half of a revocation that makes it
// one: the link dying is not enough while the meeting runs on (ADR 005).
// TestMintingCreatesTheRoom is the half of durable revocation that is easy to
// forget: with `auto_create: false` in the deploy stack, nothing but this
// server may bring a room into being, so if minting did not create it, every
// legitimate call would fail to start.
func TestMintingCreatesTheRoom(t *testing.T) {
	t.Parallel()

	lk := callstest.New(t)
	svc := New(testKey, testSecret, lk.URL, nil)
	channelID, conferenceID := uuid.New(), uuid.New()

	if _, err := svc.JoinToken(t.Context(), channelID, uuid.New(), "Sara"); err != nil {
		t.Fatalf("mint join ticket: %v", err)
	}
	if !lk.RoomLives(RoomFor(channelID)) {
		t.Error("a member's ticket was minted for a room that was never created")
	}

	if _, err := svc.GuestToken(t.Context(), conferenceID, "A Guest"); err != nil {
		t.Fatalf("mint guest ticket: %v", err)
	}
	if !lk.RoomLives(ConferenceRoomFor(conferenceID)) {
		t.Error("a guest's ticket was minted for a room that was never created")
	}

	// Two people starting a call at the same moment must both succeed: the
	// room names are stable by design (ADR 005), so the second mint asks for a
	// room that already exists and LiveKit answers with it.
	if _, err := svc.JoinToken(t.Context(), channelID, uuid.New(), "Reza"); err != nil {
		t.Errorf("a second ticket for a live room failed: %v", err)
	}

	// A media server that cannot make the room must not yield a ticket to it.
	lk.CreateFails = true
	if _, err := svc.JoinToken(t.Context(), uuid.New(), uuid.New(), "Sara"); err == nil {
		t.Error("a ticket was minted although the room could not be created")
	}
}

// TestRevokedRoomDoesNotComeBack is the security review's finding, pinned.
//
// LiveKit tokens are stateless and DeleteRoom does not invalidate the ones
// already out — a connected participant even holds a rolling ten-minute one.
// So what stops a revoked guest reconnecting forever is not the token going
// bad, it is that there is no room left to join and their ticket cannot make
// one. This asserts the room stays gone, which is the thing a DeleteRoom call
// count cannot see.
func TestRevokedRoomDoesNotComeBack(t *testing.T) {
	t.Parallel()

	lk := callstest.New(t)
	svc := New(testKey, testSecret, lk.URL, nil)
	conferenceID := uuid.New()
	room := ConferenceRoomFor(conferenceID)

	// A guest joins, so the room is live and they hold a ticket for it.
	if _, err := svc.GuestToken(t.Context(), conferenceID, "A Guest"); err != nil {
		t.Fatalf("mint guest ticket: %v", err)
	}
	if !lk.RoomLives(room) {
		t.Fatal("the guest's room was never created")
	}

	if err := svc.CloseConference(t.Context(), conferenceID); err != nil {
		t.Fatalf("CloseConference: %v", err)
	}

	// The ticket minted before the revocation is still a valid JWT and always
	// will be. What it now names is nothing: with auto_create off, LiveKit
	// refuses a join for a room that does not exist rather than instantiating
	// one, and no join ticket carries the roomCreate grant that would bypass
	// that check (asserted in TestGuestTokenGrantsNothingElse).
	if lk.RoomLives(room) {
		t.Error("the revoked room is still there; a reconnecting guest would walk back in")
	}
}

func TestCloseConferenceEndsTheMeeting(t *testing.T) {
	t.Parallel()

	lk := callstest.New(t)
	svc := New(testKey, testSecret, lk.URL, nil)
	conferenceID := uuid.New()

	if err := svc.CloseConference(t.Context(), conferenceID); err != nil {
		t.Fatalf("CloseConference: %v", err)
	}
	if room := lk.AwaitDeletion(t); room != ConferenceRoomFor(conferenceID) {
		t.Errorf("closed %q, want the conference's room", room)
	}

	// A failure must be reported, not swallowed: the caller answers 204 on
	// the strength of this, and a 204 over a meeting that is still running is
	// the exact thing the design forbids.
	lk.DeleteFails = true
	if err := svc.CloseConference(t.Context(), conferenceID); err == nil {
		t.Error("a failed close was reported as success")
	}
}

// TestTransition pins the state machine that decides which of the three
// events a fresh read is. It is the only thing carried across webhook
// deliveries, and it is a label rather than a fact.
func TestTransition(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, "http://livekit.invalid", nil)
	channelID := uuid.New()

	steps := []struct {
		active bool
		want   Transition
	}{
		{active: false, want: TransitionNone},   // a webhook for a room nobody is in
		{active: true, want: TransitionStarted}, // first participant
		{active: true, want: TransitionUpdated}, // second joins
		{active: true, want: TransitionUpdated}, // a duplicate delivery
		{active: false, want: TransitionEnded},  // last one leaves
		{active: false, want: TransitionNone},   // a late, out-of-order delivery
		{active: true, want: TransitionStarted}, // a new call in the same channel
	}
	for i, step := range steps {
		if got := svc.Transition(channelID, step.active); got != step.want {
			t.Errorf("step %d: transition = %v, want %v", i, got, step.want)
		}
	}
}

// TestStateOrdersByJoin pins what GET .../call is built from: participants
// oldest first, so started_at is when the call began and started_by is the
// person who began it, both derived rather than stored.
func TestStateOrdersByJoin(t *testing.T) {
	t.Parallel()

	first, second := uuid.New(), uuid.New()
	st := stateOf([]*livekit.ParticipantInfo{
		{Identity: second.String(), JoinedAt: 200},
		{Identity: "not-a-user-of-ours", JoinedAt: 150},
		{
			Identity: first.String(), JoinedAt: 100,
			Tracks: []*livekit.TrackInfo{{Source: livekit.TrackSource_SCREEN_SHARE}},
		},
	})

	if !st.Active || len(st.Participants) != 2 {
		t.Fatalf("state = %+v; want an active call of two known users", st)
	}
	if st.Participants[0].UserID != first || st.Participants[1].UserID != second {
		t.Errorf("participants are not oldest first: %+v", st.Participants)
	}
	if !st.Participants[0].ScreenSharing || st.Participants[1].ScreenSharing {
		t.Errorf("screen share landed on the wrong participant: %+v", st.Participants)
	}
	if !st.StartedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Errorf("started_at = %s, want the first join", st.StartedAt)
	}
	if startedBy, ok := st.StartedBy(); !ok || startedBy != first {
		t.Errorf("started_by = %v, %v; want %v", startedBy, ok, first)
	}

	if empty := stateOf(nil); empty.Active {
		t.Error("an empty room is an active call")
	}
}

// TestStateReadsLiveParticipants runs State against a fake media server, and
// pins the two answers that matter: a live room is described, and a room that
// does not exist is "no call" rather than an error — rooms only exist while
// somebody is in one.
func TestStateReadsLiveParticipants(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	lk := callstest.New(t)
	lk.Participants = []*livekit.ParticipantInfo{{Identity: userID.String(), JoinedAt: 42}}
	svc := New(testKey, testSecret, lk.URL, nil)

	state, err := svc.State(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !state.Active || len(state.Participants) != 1 || state.Participants[0].UserID != userID {
		t.Errorf("state = %+v, want the one live participant", state)
	}

	lk.NotFound = true
	state, err = svc.State(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("read state of a room that does not exist: %v", err)
	}
	if state.Active {
		t.Errorf("state = %+v, want no call", state)
	}
}

// TestEjectRemovesTheParticipant is the removal mechanism both hooks rest on.
func TestEjectRemovesTheParticipant(t *testing.T) {
	t.Parallel()

	channelID, userID := uuid.New(), uuid.New()
	lk := callstest.New(t)
	svc := New(testKey, testSecret, lk.URL, nil)

	svc.Eject(context.Background(), channelID, userID)

	removed := lk.AwaitRemoval(t)
	if removed.GetRoom() != RoomFor(channelID) || removed.GetIdentity() != userID.String() {
		t.Errorf("removed %+v, want %s from %s", removed, userID, RoomFor(channelID))
	}
}

// TestEjectRetriesOnceAndStops pins the failure policy: a failed RPC is
// retried once and then logged, never queued. The participant is gone at call
// end regardless, and an ejection queue that outlives its call is a worse
// thing to own than one log line (ADR 005).
func TestEjectRetriesOnceAndStops(t *testing.T) {
	t.Parallel()

	lk := callstest.New(t)
	lk.RemoveFails = true
	svc := New(testKey, testSecret, lk.URL, nil)
	svc.ejectRetryFast()

	svc.ejectWithRetry(context.Background(), RoomFor(uuid.New()), uuid.New())

	if attempts := lk.RemoveAttempts(); attempts != 2 {
		t.Errorf("remove attempted %d times, want exactly 2 (the call plus one retry)", attempts)
	}
}

// TestEjectEverywhereSweepsLiveRooms pins the deactivation hook's reach: it
// asks the media server which rooms are live rather than which channels the
// account was in, because an offboarded account has no memberships left.
func TestEjectEverywhereSweepsLiveRooms(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	ours, alsoOurs := RoomFor(uuid.New()), RoomFor(uuid.New())
	lk := callstest.New(t)
	lk.Rooms = []string{ours, "somebody-elses-room", alsoOurs}
	svc := New(testKey, testSecret, lk.URL, nil)

	svc.EjectEverywhere(context.Background(), userID)

	got := map[string]bool{}
	for range 2 {
		removal := lk.AwaitRemoval(t)
		if removal.GetIdentity() != userID.String() {
			t.Errorf("removed %q, want %s", removal.GetIdentity(), userID)
		}
		got[removal.GetRoom()] = true
	}
	if !got[ours] || !got[alsoOurs] {
		t.Errorf("swept %v, want both %s and %s", got, ours, alsoOurs)
	}
}

// TestWebhookRefusesAnythingUnsigned is the receiver's whole authentication.
// It sits outside the contract router, so no session, cookie or CSRF token
// reaches it: the signature over the body IS the credential, and every way it
// can fail must answer the same refusal.
func TestWebhookRefusesAnythingUnsigned(t *testing.T) {
	t.Parallel()

	svc := New(testKey, testSecret, "http://livekit.invalid", nil)
	channelID := uuid.New()
	body := `{"event":"participant_joined","room":{"name":"` + RoomFor(channelID) + `"}}`

	t.Run("a signed body is accepted", func(t *testing.T) {
		got, ok := svc.WebhookChannel(callstest.SignedWebhook(t, testKey, testSecret, body))
		if !ok || got != channelID {
			t.Errorf("verified = %v, %v; want %v, true", got, ok, channelID)
		}
	})

	tampered := strings.Replace(body, "participant_joined", "participant_left", 1)
	refusals := map[string]*http.Request{
		"no authorization header": httptest.NewRequest(http.MethodPost, "/livekit/webhook", strings.NewReader(body)),
		"a token signed with another secret": callstest.SignedWebhook(t, testKey,
			"a different secret that is also long enough", body),
		"a token from another api key": callstest.SignedWebhook(t, "somebody-elses-key", testSecret, body),
		"a body swapped after signing": func() *http.Request {
			r := callstest.SignedWebhook(t, testKey, testSecret, body)
			r.Body = io.NopCloser(strings.NewReader(tampered))
			return r
		}(),
		"a room this instance did not name": callstest.SignedWebhook(t, testKey, testSecret,
			`{"event":"room_started","room":{"name":"somebody-elses-room"}}`),
		"an event with no room at all": callstest.SignedWebhook(t, testKey, testSecret,
			`{"event":"egress_started"}`),
	}
	for name, req := range refusals {
		t.Run(name, func(t *testing.T) {
			if _, ok := svc.WebhookChannel(req); ok {
				t.Error("accepted")
			}
		})
	}
}

// ejectRetryFast shortens the retry pause for the one test that has to wait
// through it. It is a test helper on purpose: the production delay is a
// constant nobody configures.
func (s *Service) ejectRetryFast() { s.retryDelay = time.Millisecond }

// decodeClaims reads a JWT's payload WITHOUT verifying it. That is the point:
// the assertion is about what is on the wire for LiveKit to read, not about
// what our struct held before it was serialized.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a JWT", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("parse token payload: %v", err)
	}
	return claims
}
