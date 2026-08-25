package wsgateway

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

func TestReplayBufferWindow(t *testing.T) {
	t.Parallel()

	var buf replayBuffer
	now := time.Now()
	for i := range 3 {
		seq := buf.nextSeq()
		buf.store(seq, []byte{byte(i)}, now)
	}

	if buf.lastSeq != 3 {
		t.Fatalf("lastSeq = %d, want 3", buf.lastSeq)
	}
	if got := len(buf.after(1)); got != 2 {
		t.Errorf("after(1) returned %d frames, want 2", got)
	}
	if got := len(buf.after(3)); got != 0 {
		t.Errorf("after(3) returned %d frames, want 0", got)
	}

	tests := []struct {
		name string
		seq  uint64
		want bool
	}{
		{"caught up", 3, true},
		{"one behind", 2, true},
		{"at the oldest edge", 0, true},
		{"ahead of the server", 4, false},
	}
	for _, tc := range tests {
		if got := buf.canResume(tc.seq); got != tc.want {
			t.Errorf("canResume(%d) [%s] = %v, want %v", tc.seq, tc.name, got, tc.want)
		}
	}
}

// TestReplayBufferDropsPastTheWindow: a client further behind than the
// buffer reaches cannot be resumed, and the sequence counter survives the
// pruning so a reconnect is never handed numbers below ones it holds.
func TestReplayBufferDropsPastTheWindow(t *testing.T) {
	t.Parallel()

	var buf replayBuffer
	old := time.Now().Add(-2 * replayMaxAge)
	for range 3 {
		buf.store(buf.nextSeq(), []byte("x"), old)
	}
	buf.store(buf.nextSeq(), []byte("x"), time.Now())

	buf.prune(time.Now().Add(-replayMaxAge))

	if len(buf.entries) != 1 {
		t.Fatalf("pruned buffer holds %d entries, want 1", len(buf.entries))
	}
	if buf.lastSeq != 4 {
		t.Fatalf("prune reset the sequence counter to %d", buf.lastSeq)
	}
	if buf.canResume(1) {
		t.Error("canResume(1) is true after the window moved past it")
	}
	if !buf.canResume(3) {
		t.Error("canResume(3) is false although event 4 is still buffered")
	}
	if !buf.canResume(4) {
		t.Error("a caught-up client cannot resume")
	}
}

func TestReplayBufferCap(t *testing.T) {
	t.Parallel()

	var buf replayBuffer
	now := time.Now()
	for range replayMaxEvents + 50 {
		buf.store(buf.nextSeq(), []byte("x"), now)
	}

	if len(buf.entries) != replayMaxEvents {
		t.Fatalf("buffer holds %d entries, want %d", len(buf.entries), replayMaxEvents)
	}
	if buf.entries[0].seq != 51 {
		t.Errorf("oldest retained seq = %d, want 51", buf.entries[0].seq)
	}
}

// TestResumeDeliversMissedMessagesExactlyOnce is (f).
func TestResumeDeliversMissedMessagesExactlyOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)
	familyID := h.store.addFamily(bob.ID)

	first := h.dial(bob, familyID)
	first.hello()

	seen := testMessage(ch.ID, alice, "seen")
	h.gw.MessageCreated(ch.ID, seen)
	frame := first.expect(typeMessageCreated)
	seq := frameSeq(t, frame)
	first.closeNow()

	missed := []storage.Message{
		testMessage(ch.ID, alice, "missed one"),
		testMessage(ch.ID, alice, "missed two"),
	}
	for _, m := range missed {
		h.gw.MessageCreated(ch.ID, m)
	}
	h.waitForSeq(ch.ID, seq+uint64(len(missed)))

	second := h.dial(bob, familyID)
	ok := second.hello(resumedCursor{Chan: ch.ID, Seq: seq})

	if len(ok.Resumed) != 1 || ok.Resumed[0].Chan != ch.ID {
		t.Fatalf("hello_ok resumed %v, want the one channel", ok.Resumed)
	}
	if len(ok.Resync) != 0 {
		t.Fatalf("hello_ok asked to resync %v", ok.Resync)
	}

	for i, want := range missed {
		frame := second.expect(typeMessageCreated)
		var payload messageData
		remarshal(t, frame["data"], &payload)
		if payload.Message.Id != want.ID {
			t.Fatalf("replayed message %d = %s, want %s", i, payload.Message.Id, want.ID)
		}
		if got := frameSeq(t, frame); got != seq+uint64(i)+1 {
			t.Errorf("replayed seq = %d, want %d", got, seq+uint64(i)+1)
		}
	}

	// Exactly once: the message the client already held is not replayed, and
	// neither is anything else.
	second.expectNone()
}

// TestResumeRechecksMembership is the IDOR through the reconnect path. The
// resume list is the client's claim about what it once saw; a channel the
// user is not a member of now is never replayed (§5).
func TestResumeRechecksMembership(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)
	familyID := h.store.addFamily(bob.ID)

	first := h.dial(bob, familyID)
	first.hello()
	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "while a member"))
	frame := first.expect(typeMessageCreated)
	seq := frameSeq(t, frame)
	first.closeNow()

	// Removed while disconnected, and the conversation moves on without him.
	h.store.removeMember(ch.ID, bob.ID)
	secret := testMessage(ch.ID, alice, "after he was removed")
	h.gw.MessageCreated(ch.ID, secret)
	h.waitForSeq(ch.ID, seq+1)

	second := h.dial(bob, familyID)
	ok := second.hello(resumedCursor{Chan: ch.ID, Seq: seq})

	if len(ok.Resumed) != 0 {
		t.Fatalf("hello_ok resumed %v for a channel the user was removed from", ok.Resumed)
	}
	if len(ok.Resync) != 1 || ok.Resync[0] != ch.ID {
		t.Fatalf("hello_ok resync = %v, want [%s]", ok.Resync, ch.ID)
	}
	second.expectNone()
}

// TestResumeOfAChannelYouWereNeverInResyncs: the answer reveals nothing,
// because the list is derived from the client's own request.
func TestResumeOfAChannelYouWereNeverInResyncs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	mallory := h.store.addUser("mallory")
	private := h.store.addChannel(storage.ChannelKindPrivate, alice.ID)
	unknown := uuid.New()

	h.gw.MessageCreated(private.ID, testMessage(private.ID, alice, "members only"))
	h.waitForSeq(private.ID, 1)

	c := h.dial(mallory, h.store.addFamily(mallory.ID))
	ok := c.hello(
		resumedCursor{Chan: private.ID, Seq: 0},
		resumedCursor{Chan: unknown, Seq: 0},
	)

	if len(ok.Resumed) != 0 {
		t.Fatalf("hello_ok resumed %v for a stranger", ok.Resumed)
	}
	if len(ok.Resync) != 2 {
		t.Fatalf("hello_ok resync = %v, want both channels", ok.Resync)
	}
	c.expectNone()
}

// TestResumeBeyondTheWindowResyncs: a client further behind than the buffer
// reaches backfills over REST instead, and that is the normal path.
func TestResumeBeyondTheWindowResyncs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "one"))
	h.waitForSeq(ch.ID, 1)

	c := h.dial(bob, h.store.addFamily(bob.ID))
	// A sequence number this server never issued: a different server
	// lifetime, or a client making something up.
	ok := c.hello(resumedCursor{Chan: ch.ID, Seq: 9000})

	if len(ok.Resync) != 1 || ok.Resync[0] != ch.ID {
		t.Fatalf("hello_ok resync = %v, want [%s]", ok.Resync, ch.ID)
	}
	c.expectNone()
}

// TestResumeWhenCaughtUpReplaysNothing.
func TestResumeWhenCaughtUpReplaysNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "one"))
	h.waitForSeq(ch.ID, 1)

	c := h.dial(bob, h.store.addFamily(bob.ID))
	ok := c.hello(resumedCursor{Chan: ch.ID, Seq: 1})

	if len(ok.Resumed) != 1 || len(ok.Resync) != 0 {
		t.Fatalf("hello_ok resumed %v resync %v, want the channel resumed", ok.Resumed, ok.Resync)
	}
	c.expectNone()
}
