package wsgateway

import (
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The two E2EE transport events (ADR 006, ws-protocol.md §4). They are what
// licenses the WSEnforced rows in internal/authztest: the registry claims
// membership scope for mls_commit and self scope for mls_welcome, and a claim
// without a test asserting it is a comment.

// TestMlsEventsNeverReachANonMember is the WS half of the IDOR matrix for the
// transport.
//
// That a channel's group moved is as much a disclosure as what is said in it:
// it says the conversation is encrypted, that its membership or its keys
// changed, and roughly when. A stranger must learn none of it.
func TestMlsEventsNeverReachANonMember(t *testing.T) {
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

	h.gw.MlsCommit(private.ID, 7)

	frame := member.expect(typeMlsCommit)
	if frameChan(t, frame) != private.ID {
		t.Errorf("mls_commit carried chan %v, want %s", frame["chan"], private.ID)
	}
	if frame["seq"] != nil {
		// A seq would put it in the replay buffer, which is exactly the
		// wrong durability: the commit log itself is durable, and a replayed
		// nudge would only send a caught-up client to refetch (§4).
		t.Errorf("mls_commit carried seq %v; it is a notification, never replayed", frame["seq"])
	}
	var commit mlsCommitData
	remarshal(t, frame["data"], &commit)
	if commit.Epoch != 7 {
		t.Errorf("epoch = %d, want 7", commit.Epoch)
	}

	outsider.expectNone()
}

// TestMlsCommitStopsAtRemoval closes the other half: the audience is read
// from the membership table at send time, never captured at connect, so
// somebody removed from a channel stops hearing that its group is moving.
//
// It matters more here than on an ordinary message event. A removed member's
// client still holds group state, and a nudge telling it to fetch the log is
// exactly the prompt it would need to keep trying.
func TestMlsCommitStopsAtRemoval(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	socket := h.dial(bob, h.store.addFamily(bob.ID))
	socket.hello()

	h.gw.MlsCommit(ch.ID, 1)
	socket.expect(typeMlsCommit)

	h.store.removeMember(ch.ID, bob.ID)

	h.gw.MlsCommit(ch.ID, 2)
	socket.expectNone()
}

// TestMlsWelcomeReachesOnlyItsOwnUser pins the one `self` rule on this
// surface, and why it is not a membership rule.
//
// A Welcome exists precisely because its recipient is NOT in the group yet —
// often not even in the channel's MLS tree — so a membership-scoped event
// would refuse the very delivery the design depends on. What keeps it narrow
// instead is that the event reaches one user's own sockets and carries no
// payload at all: a sibling device, and anybody else, learns nothing beyond
// "you have something to fetch", which the recipient can already see.
func TestMlsWelcomeReachesOnlyItsOwnUser(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	// Alice and bob share a channel, so a membership-scoped delivery would
	// leak the nudge to alice. This is what separates `self` from `member`.
	h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	recipient := h.dial(bob, h.store.addFamily(bob.ID))
	recipient.hello()
	// A second socket of the same person: every device of theirs is told,
	// because a Welcome is encrypted to one device's key package and the
	// others hold bytes they cannot open.
	sibling := h.dial(bob, h.store.addFamily(bob.ID))
	sibling.hello()
	bystander := h.dial(alice, h.store.addFamily(alice.ID))
	bystander.hello()

	h.gw.MlsWelcome(bob.ID)

	for _, socket := range []*wsClient{recipient, sibling} {
		frame := socket.expect(typeMlsWelcome)
		if frame["seq"] != nil {
			t.Errorf("mls_welcome carried seq %v; it is a notification, never replayed", frame["seq"])
		}
		// No channel and no device: naming either would say which
		// conversation admitted which of a person's devices, to every socket
		// they hold.
		if frame["chan"] != nil {
			t.Errorf("mls_welcome carried chan %v; it names no channel", frame["chan"])
		}
	}
	bystander.expectNone()
}
