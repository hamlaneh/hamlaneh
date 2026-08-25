package wsgateway

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

func TestSubscribeToAChannelYouAreIn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)

	c := h.dial(alice, h.store.addFamily(alice.ID))
	c.hello()
	c.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})

	got := c.expect(typeSubscribed)
	if got["id"] != "s1" {
		t.Errorf("subscribed echoed id %v, want s1", got["id"])
	}
	if frameChan(t, got) != ch.ID {
		t.Errorf("subscribed echoed chan %v, want %s", got["chan"], ch.ID)
	}
}

// TestSubscribeToAChannelYouAreNotInLeaksNothing is (d) and the §3 rule that
// matters most about it: the refusal is channel_not_found, never forbidden,
// and it is the same answer an id naming nothing at all gets. A socket must
// not become the one place a private channel's existence leaks.
func TestSubscribeToAChannelYouAreNotInLeaksNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	private := h.store.addChannel(storage.ChannelKindPrivate, bob.ID)

	c := h.dial(alice, h.store.addFamily(alice.ID))
	c.hello()

	for _, target := range []uuid.UUID{private.ID, uuid.New()} {
		c.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": target.String()})

		frame := c.expect(typeError)
		var data errorData
		remarshal(t, frame["data"], &data)
		if data.Code != codeChannelNotFound {
			t.Fatalf("error code = %q, want %q", data.Code, codeChannelNotFound)
		}
	}

	// The socket stays open: a wrong subscribe is a client bug, not an
	// attack worth dropping the connection over.
	c.send(map[string]any{"type": typePing, "id": "alive"})
	if got := c.expect(typePong); got["id"] != "alive" {
		t.Errorf("socket did not survive a refused subscribe")
	}
}

// TestMembershipIsCheckedOnEveryOperation is the §3 rule against a
// membership set captured at connect: a user removed mid-socket stops being
// able to act on the channel immediately.
func TestMembershipIsCheckedOnEveryOperation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)

	c := h.dial(alice, h.store.addFamily(alice.ID))
	c.hello()
	c.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	c.expect(typeSubscribed)

	h.store.removeMember(ch.ID, alice.ID)

	c.send(map[string]any{"type": typeSubscribe, "id": "s2", "chan": ch.ID.String()})
	frame := c.expect(typeError)
	var data errorData
	remarshal(t, frame["data"], &data)
	if data.Code != codeChannelNotFound {
		t.Fatalf("error code = %q, want %q", data.Code, codeChannelNotFound)
	}
}

// TestMembershipReadFailureFailsClosed: a membership question the database
// cannot answer is never answered "yes".
func TestMembershipReadFailureFailsClosed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)

	c := h.dial(alice, h.store.addFamily(alice.ID))
	c.hello()

	h.store.mu.Lock()
	h.store.failMembership = true
	h.store.mu.Unlock()

	c.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	frame := c.expect(typeError)
	var data errorData
	remarshal(t, frame["data"], &data)
	if data.Code != codeInternalError {
		t.Fatalf("error code = %q, want %q", data.Code, codeInternalError)
	}
}

// TestMessageEventsNeverReachANonMember is half of (e), and the WS half of
// the IDOR matrix. It is written so it would fail if delivery ever fell back
// to "everyone with a socket" or to a membership set captured at connect.
func TestMessageEventsNeverReachANonMember(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	mallory := h.store.addUser("mallory")
	private := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	bobSocket := h.dial(bob, h.store.addFamily(bob.ID))
	bobSocket.hello()
	mallorySocket := h.dial(mallory, h.store.addFamily(mallory.ID))
	mallorySocket.hello()

	msg := testMessage(private.ID, alice, "members only")
	h.gw.MessageCreated(private.ID, msg)

	frame := bobSocket.expect(typeMessageCreated)
	if frameChan(t, frame) != private.ID {
		t.Errorf("message_created carried chan %v, want %s", frame["chan"], private.ID)
	}
	var payload messageData
	remarshal(t, frame["data"], &payload)
	if payload.Message.Id != msg.ID {
		t.Errorf("message id = %s, want %s", payload.Message.Id, msg.ID)
	}
	if frame["seq"] == nil {
		t.Error("message_created carried no seq")
	}

	mallorySocket.expectNone()
}

// TestRemovedMemberStopsReceiving closes the other half of (e): membership
// is read at send time, so a user removed while connected receives nothing
// more.
func TestRemovedMemberStopsReceiving(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	bobSocket := h.dial(bob, h.store.addFamily(bob.ID))
	bobSocket.hello()

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "while a member"))
	bobSocket.expect(typeMessageCreated)

	h.store.removeMember(ch.ID, bob.ID)

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "after removal"))
	bobSocket.expectNone()
}

// TestTypingIsSubscriptionScopedAndMemberOnly: membership earns you the
// channel's ordered events, but the ephemeral ones need a subscription too,
// and neither is available to a non-member.
func TestTypingIsSubscriptionScopedAndMemberOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	carol := h.store.addUser("carol")
	mallory := h.store.addUser("mallory")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID, carol.ID)

	subscribed := h.dial(bob, h.store.addFamily(bob.ID))
	subscribed.hello()
	subscribed.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	subscribed.expect(typeSubscribed)

	// A member who never subscribed.
	quiet := h.dial(carol, h.store.addFamily(carol.ID))
	quiet.hello()

	// A non-member who tries to subscribe and is refused, then listens.
	outsider := h.dial(mallory, h.store.addFamily(mallory.ID))
	outsider.hello()
	outsider.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	outsider.expect(typeError)

	author := h.dial(alice, h.store.addFamily(alice.ID))
	author.hello()
	author.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	author.expect(typeSubscribed)
	author.send(map[string]any{"type": typeTyping, "chan": ch.ID.String()})

	frame := subscribed.expect(typeTyping)
	var typing typingData
	remarshal(t, frame["data"], &typing)
	if typing.UserID != alice.ID {
		t.Errorf("typing user_id = %s, want %s", typing.UserID, alice.ID)
	}

	quiet.expectNone()
	outsider.expectNone()
	author.expectNone()
}

// TestPresenceIsDMScopedOnly is the privacy floor of §4: a member of a busy
// channel learns nothing about who else is online.
func TestPresenceIsDMScopedOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	carol := h.store.addUser("carol")

	dm := h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)
	h.store.addChannel(storage.ChannelKindPrivate, alice.ID, carol.ID)

	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()
	coworker := h.dial(carol, h.store.addFamily(carol.ID))
	coworker.hello()

	subject := h.dial(alice, h.store.addFamily(alice.ID))
	subject.hello()

	frame := peer.expect(typePresence)
	if frameChan(t, frame) != dm.ID {
		t.Errorf("presence carried chan %v, want the DM %s", frame["chan"], dm.ID)
	}
	var data presenceEventData
	remarshal(t, frame["data"], &data)
	if data.UserID != alice.ID || data.State != presenceOnline {
		t.Errorf("presence = %+v, want alice online", data)
	}

	// The channel co-member learns nothing.
	coworker.expectNone()

	// A reported away state reaches the DM peer and nobody else.
	subject.send(map[string]any{"type": typePresence, "data": map[string]any{"state": presenceAway}})
	frame = peer.expect(typePresence)
	remarshal(t, frame["data"], &data)
	if data.State != presenceAway {
		t.Errorf("presence state = %q, want away", data.State)
	}
	coworker.expectNone()
}

// TestPresenceGoesOfflineAfterTheGrace: offline is what the server says
// after a user's last socket has been gone long enough that a reload has
// been ruled out (§4).
func TestPresenceGoesOfflineAfterTheGrace(t *testing.T) {
	t.Parallel()

	h := newHarness(t,
		WithSweepInterval(20*time.Millisecond),
		WithPresenceGrace(50*time.Millisecond))
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)

	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()
	subject := h.dial(alice, h.store.addFamily(alice.ID))
	subject.hello()
	peer.expect(typePresence)

	subject.closeNow()

	frame := peer.expect(typePresence)
	var data presenceEventData
	remarshal(t, frame["data"], &data)
	if data.UserID != alice.ID || data.State != presenceOffline {
		t.Errorf("presence = %+v, want alice offline", data)
	}
}

// TestPresenceAfterAReconnectInsideTheGrace is the state-machine trap: a
// user who was away, dropped, and came back before the grace expired is
// online again, and the server has to remember that. Recording away while
// announcing online would make the next away report look like no change at
// all, and the peer would sit on a stale "online" forever.
func TestPresenceAfterAReconnectInsideTheGrace(t *testing.T) {
	t.Parallel()

	h := newHarness(t,
		WithSweepInterval(20*time.Millisecond),
		WithPresenceGrace(3*time.Second))
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)
	familyID := h.store.addFamily(alice.ID)

	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()

	first := h.dial(alice, familyID)
	first.hello()
	expectPresence(t, peer, alice.ID, presenceOnline)

	first.send(map[string]any{"type": typePresence, "data": map[string]any{"state": presenceAway}})
	expectPresence(t, peer, alice.ID, presenceAway)

	// Dropped and back well inside the grace, so the remembered state is
	// still there to be got wrong.
	first.closeNow()
	second := h.dial(alice, familyID)
	second.hello()
	expectPresence(t, peer, alice.ID, presenceOnline)

	second.send(map[string]any{"type": typePresence, "data": map[string]any{"state": presenceAway}})
	expectPresence(t, peer, alice.ID, presenceAway)
}

func expectPresence(t *testing.T, c *wsClient, userID uuid.UUID, state string) {
	t.Helper()

	var data presenceEventData
	remarshal(t, c.expect(typePresence)["data"], &data)
	if data.UserID != userID || data.State != state {
		t.Fatalf("presence = %+v, want %s %s", data, userID, state)
	}
}

// TestPresenceOfflineCannotBeClaimed: offline is server-derived (§3).
func TestPresenceOfflineCannotBeClaimed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)

	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()
	subject := h.dial(alice, h.store.addFamily(alice.ID))
	subject.hello()
	peer.expect(typePresence)

	subject.send(map[string]any{"type": typePresence, "data": map[string]any{"state": presenceOffline}})
	subject.send(map[string]any{"type": typePresence, "data": map[string]any{"state": "invisible"}})

	peer.expectNone()
}

// TestRemovalIsTwoEventsWithDisjointAudiences is §4's rule that a
// membership-scoped event must never reach somebody who is no longer a
// member. The remaining members get member_removed; the removed user's own
// sockets get channel_removed, which names nothing that is now none of their
// business.
func TestRemovalIsTwoEventsWithDisjointAudiences(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	remaining := h.dial(alice, h.store.addFamily(alice.ID))
	remaining.hello()
	removed := h.dial(bob, h.store.addFamily(bob.ID))
	removed.hello()
	removed.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	removed.expect(typeSubscribed)

	// The handler commits the removal first, then announces both halves.
	h.store.removeMember(ch.ID, bob.ID)
	h.gw.MemberRemoved(ch.ID, bob)
	h.gw.ChannelRemoved(bob.ID, ch.ID)

	frame := remaining.expect(typeMemberRemoved)
	var member memberData
	remarshal(t, frame["data"], &member)
	if member.User.Id != bob.ID || member.Chan != ch.ID {
		t.Errorf("member_removed = %+v, want bob in %s", member, ch.ID)
	}

	frame = removed.expect(typeChannelRemoved)
	var gone chanData
	remarshal(t, frame["data"], &gone)
	if gone.Chan != ch.ID {
		t.Errorf("channel_removed chan = %s, want %s", gone.Chan, ch.ID)
	}

	// Neither socket sees the other's event.
	remaining.expectNone()
	removed.expectNone()
}

// TestRemovalDropsTheSubscriptionServerSide: the server does not wait for
// the client to be polite about it (§4).
func TestRemovalDropsTheSubscriptionServerSide(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	removed := h.dial(bob, h.store.addFamily(bob.ID))
	removed.hello()
	removed.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": ch.ID.String()})
	removed.expect(typeSubscribed)

	h.store.removeMember(ch.ID, bob.ID)
	h.gw.MemberRemoved(ch.ID, bob)

	// member_removed reaches the members that remain, so the removed socket
	// is told nothing at all: the subscription has to be dropped server-side
	// or it would never be dropped.
	sockets := h.gw.socketsFor(bob.ID)
	if len(sockets) != 1 {
		t.Fatalf("bob has %d sockets, want 1", len(sockets))
	}
	deadline := time.Now().Add(waitFor)
	for sockets[0].subscribed(ch.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the removed user's subscription survived the removal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	removed.expectNone()
}

// TestReadPositionReachesOnlyTheSameUser: there are no cross-user read
// receipts anywhere in this protocol.
func TestReadPositionReachesOnlyTheSameUser(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	otherTab := h.dial(alice, h.store.addFamily(alice.ID))
	otherTab.hello()
	peer := h.dial(bob, h.store.addFamily(bob.ID))
	peer.hello()

	messageID := uuid.New()
	h.gw.ReadPosition(alice.ID, ch.ID, messageID, testNow())

	frame := otherTab.expect(typeReadPosition)
	var data readPositionData
	remarshal(t, frame["data"], &data)
	if data.MessageID != messageID || data.Chan != ch.ID {
		t.Errorf("read_position = %+v, want %s in %s", data, messageID, ch.ID)
	}

	peer.expectNone()
}

// TestChannelCreatedIsFilteredByMembership: the audience a handler names is
// still checked against the membership table, so a handler bug cannot
// deliver a channel to somebody who is not in it.
func TestChannelCreatedIsFilteredByMembership(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	mallory := h.store.addUser("mallory")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)

	member := h.dial(alice, h.store.addFamily(alice.ID))
	member.hello()
	outsider := h.dial(mallory, h.store.addFamily(mallory.ID))
	outsider.hello()

	h.gw.ChannelCreated([]uuid.UUID{alice.ID, mallory.ID}, ch)

	frame := member.expect(typeChannelCreated)
	var data channelData
	remarshal(t, frame["data"], &data)
	if data.Channel.Id != ch.ID {
		t.Errorf("channel_created carried %s, want %s", data.Channel.Id, ch.ID)
	}
	outsider.expectNone()
}

// TestChannelCreatedCarriesTheDMPeer: a direct message has no slug, so
// dm_peer is the only thing that names it. A channel_created that dropped it
// would put a nameless row in the recipient's sidebar — which is what
// happened until this frame started carrying the field the REST mapping
// always has.
func TestChannelCreatedCarriesTheDMPeer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindDM, alice.ID, bob.ID)
	// The row as BOB sees it: the other one is alice. Announcing one shared
	// row to both is what the handler must not do, and this is the shape it
	// hands over instead.
	ch.DMPeer = &storage.DMPeer{ID: alice.ID, Username: alice.Username, DisplayName: alice.DisplayName}

	c := h.dial(bob, h.store.addFamily(bob.ID))
	c.hello()

	h.gw.ChannelCreated([]uuid.UUID{bob.ID}, ch)

	var data channelData
	remarshal(t, c.expect(typeChannelCreated)["data"], &data)
	if data.Channel.DmPeer == nil {
		t.Fatal("channel_created carried no dm_peer; the sidebar row would have no name")
	}
	if data.Channel.DmPeer.Id != alice.ID || data.Channel.DmPeer.DisplayName != alice.DisplayName {
		t.Errorf("dm_peer = %+v, want alice", *data.Channel.DmPeer)
	}
}

// TestOrderedEventsCarrySequenceNumbers pins the §4 `seq` column: the
// ordered events advance one per channel and the ephemeral ones carry none.
func TestOrderedEventsCarrySequenceNumbers(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	c := h.dial(bob, h.store.addFamily(bob.ID))
	c.hello()

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "one"))
	h.gw.ChannelUpdated(ch.ID, ch)
	h.gw.MemberAdded(ch.ID, alice)

	var seqs []uint64
	for _, want := range []string{typeMessageCreated, typeChannelUpdated, typeMemberAdded} {
		seqs = append(seqs, frameSeq(t, c.expect(want)))
	}
	for i, seq := range seqs {
		if seq != uint64(i)+1 {
			t.Errorf("seq[%d] = %d, want %d", i, seq, i+1)
		}
	}
}

// TestDeliverDropsRatherThanBlocks is the slow-subscriber rule: a full
// outbound queue drops the event and remembers the channel, so the socket
// gets a resync instead of the sender getting stalled.
func TestDeliverDropsRatherThanBlocks(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	channelID := uuid.New()
	s := &socket{
		g:      h.gw,
		out:    make(chan []byte, 1),
		resync: make(map[uuid.UUID]struct{}),
	}

	s.deliver(channelID, []byte(`{"type":"message_created"}`))
	if len(s.resync) != 0 {
		t.Fatalf("a delivery that fit still owed a resync: %v", s.resync)
	}

	before := h.gw.dropped.Load()
	s.deliver(channelID, []byte(`{"type":"message_created"}`))

	if _, owed := s.resync[channelID]; !owed {
		t.Error("a dropped delivery did not owe the channel a resync")
	}
	if h.gw.dropped.Load() != before+1 {
		t.Error("a dropped delivery was not counted")
	}
	if len(s.out) != 1 {
		t.Errorf("outbound queue holds %d frames, want 1", len(s.out))
	}
}

// TestUnsubscribeStopsEphemeralEventsAndRefusesStrangers covers the one
// operation pair the rest of this suite left untested. It matters for the
// same reason subscribe does: unsubscribe is member-scoped in the protocol's
// operation table, so a stranger asking must get channel_not_found — the
// non-leaking answer — rather than a confirmation that the channel is there
// to leave.
func TestUnsubscribeStopsEphemeralEventsAndRefusesStrangers(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	shared := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)
	private := h.store.addChannel(storage.ChannelKindPrivate, bob.ID)

	c := h.dial(alice, h.store.addFamily(alice.ID))
	c.hello()

	c.send(map[string]any{"type": typeSubscribe, "id": "s1", "chan": shared.ID.String()})
	c.expect(typeSubscribed)

	c.send(map[string]any{"type": typeUnsubscribe, "id": "u1", "chan": shared.ID.String()})
	if got := c.expect(typeUnsubscribed); got["id"] != "u1" {
		t.Errorf("unsubscribed did not echo the correlation id")
	}

	// Typing is subscription-scoped, so after unsubscribing it must stop
	// arriving — that is the whole point of the operation, and a reply that
	// changed nothing would be indistinguishable from one that worked.
	other := h.dial(bob, h.store.addFamily(bob.ID))
	other.hello()
	other.send(map[string]any{"type": typeSubscribe, "id": "s2", "chan": shared.ID.String()})
	other.expect(typeSubscribed)
	other.send(map[string]any{"type": typeTyping, "id": "t1", "chan": shared.ID.String()})

	// Alice must not hear it. A ping behind the typing frame is what makes
	// that assertable: the pong proves the socket kept up, so silence on
	// typing is a decision rather than a race with the dispatcher.
	c.send(map[string]any{"type": typePing, "id": "after-unsub"})
	if got := c.expect(typePong); got["id"] != "after-unsub" {
		t.Fatalf("unexpected frame while draining: %v", got)
	}

	// A channel alice is not in answers exactly as an unknown id does.
	for _, target := range []uuid.UUID{private.ID, uuid.New()} {
		c.send(map[string]any{"type": typeUnsubscribe, "id": "u2", "chan": target.String()})

		frame := c.expect(typeError)
		var data errorData
		remarshal(t, frame["data"], &data)
		if data.Code != codeChannelNotFound {
			t.Fatalf("error code = %q, want %q", data.Code, codeChannelNotFound)
		}
	}
}
