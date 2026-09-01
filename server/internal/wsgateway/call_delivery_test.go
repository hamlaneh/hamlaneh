package wsgateway

import (
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The three call events (ADR 005, ws-protocol.md §4). They are what licenses
// the WSEnforced rows in internal/authztest — the registry claims membership
// scope for each, and a claim without a test asserting it is a comment.

// callParticipant is one entry of an announced participant list.
func callParticipant(user storage.User) api.CallParticipant {
	sharing := false
	return api.CallParticipant{
		User:          api.UserSummary{Id: user.ID, Username: user.Username, DisplayName: user.DisplayName},
		JoinedAt:      time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		ScreenSharing: &sharing,
	}
}

// TestCallEventsNeverReachANonMember is the WS half of the IDOR matrix for
// calls. That a call is happening in a channel is as much a disclosure as
// what is said in it: a stranger must learn neither.
//
// It is written so it would fail if delivery ever fell back to "everyone with
// a socket", and it covers all three events rather than the first one,
// because three separate enqueue sites are three chances to get the audience
// wrong.
func TestCallEventsNeverReachANonMember(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	mallory := h.store.addUser("mallory")
	private := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	member := h.dial(bob, h.store.addFamily(bob.ID))
	member.hello()
	outsider := h.dial(mallory, h.store.addFamily(mallory.ID))
	outsider.hello()

	participants := []api.CallParticipant{callParticipant(alice)}
	h.gw.CallStarted(private.ID, alice.ID, participants)

	frame := member.expect(typeCallStarted)
	if frameChan(t, frame) != private.ID {
		t.Errorf("call_started carried chan %v, want %s", frame["chan"], private.ID)
	}
	if frame["seq"] != nil {
		// A call event with a seq would enter the replay buffer, and a
		// five-minute-old one would paint a banner for a call nobody is in
		// (ws-protocol.md §5).
		t.Errorf("call_started carried seq %v; call events are never replayed", frame["seq"])
	}
	var started callStartedData
	remarshal(t, frame["data"], &started)
	if started.StartedBy != alice.ID {
		t.Errorf("started_by = %s, want %s", started.StartedBy, alice.ID)
	}
	if len(started.Participants) != 1 || started.Participants[0].User.Id != alice.ID {
		t.Errorf("participants = %+v, want the one caller", started.Participants)
	}

	h.gw.CallUpdated(private.ID, append(participants, callParticipant(bob)))
	frame = member.expect(typeCallUpdated)
	var updated callUpdatedData
	remarshal(t, frame["data"], &updated)
	if len(updated.Participants) != 2 {
		t.Errorf("call_updated carried %d participants, want 2", len(updated.Participants))
	}

	h.gw.CallEnded(private.ID)
	frame = member.expect(typeCallEnded)
	var ended chanData
	remarshal(t, frame["data"], &ended)
	if ended.Chan != private.ID {
		t.Errorf("call_ended named %s, want %s", ended.Chan, private.ID)
	}

	// Not one of the three reached a stranger to the channel.
	outsider.expectNone()
}

// TestCallEventsStopAtRemoval closes the other half: the audience is read
// from the membership table at send time, never captured at connect, so
// somebody removed mid-call stops hearing about it immediately. This is the
// delivery-side companion to the ejection hook — one stops the events, the
// other stops the media.
func TestCallEventsStopAtRemoval(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	socket := h.dial(bob, h.store.addFamily(bob.ID))
	socket.hello()

	h.gw.CallStarted(ch.ID, alice.ID, []api.CallParticipant{callParticipant(alice)})
	socket.expect(typeCallStarted)

	h.store.removeMember(ch.ID, bob.ID)

	h.gw.CallUpdated(ch.ID, []api.CallParticipant{callParticipant(alice)})
	h.gw.CallEnded(ch.ID)
	socket.expectNone()
}

// TestCallStartedReachesADMPeerWithoutSubscribing is the entire 1:1 ringing
// design for this phase (ADR 005): membership scope rather than subscription
// is what makes a DM peer's client ring, so a socket that has subscribed to
// nothing still receives it.
func TestCallStartedReachesADMPeerWithoutSubscribing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	dm := h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)

	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()

	h.gw.CallStarted(dm.ID, alice.ID, []api.CallParticipant{callParticipant(alice)})

	if frame := peer.expect(typeCallStarted); frameChan(t, frame) != dm.ID {
		t.Errorf("call_started carried chan %v, want the DM %s", frame["chan"], dm.ID)
	}
}

// TestCallEventsCarryAnArrayNotNull pins the wire shape a client renders: the
// contract makes participants an array, and null would be a third state a
// client would have to decide what to draw for.
func TestCallEventsCarryAnArrayNotNull(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)

	socket := h.dial(alice, h.store.addFamily(alice.ID))
	socket.hello()

	h.gw.CallUpdated(ch.ID, nil)
	frame := socket.expect(typeCallUpdated)

	data, ok := frame["data"].(map[string]any)
	if !ok {
		t.Fatalf("call_updated data is not an object: %v", frame["data"])
	}
	participants, ok := data["participants"].([]any)
	if !ok {
		t.Fatalf("participants = %v, want an array", data["participants"])
	}
	if len(participants) != 0 {
		t.Errorf("participants = %v, want empty", participants)
	}
}
