package storage_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// newChannel builds a NewChannel fixture for a named (non-DM) channel.
func newChannel(slug string, kind storage.ChannelKind, createdBy uuid.UUID) storage.NewChannel {
	return storage.NewChannel{
		Kind:      kind,
		Slug:      slug,
		CreatedBy: createdBy,
	}
}

func mustCreateChannel(ctx context.Context, t *testing.T, store *storage.Store, nc storage.NewChannel) storage.Channel {
	t.Helper()

	ch, err := store.CreateChannel(ctx, nc)
	if err != nil {
		t.Fatalf("CreateChannel(%s): %v", nc.Slug, err)
	}
	return ch
}

func mustOpenDM(ctx context.Context, t *testing.T, store *storage.Store, caller, peer uuid.UUID) storage.Channel {
	t.Helper()

	ch, _, err := store.OpenDirectMessage(ctx, caller, peer)
	if err != nil {
		t.Fatalf("OpenDirectMessage(%s, %s): %v", caller, peer, err)
	}
	return ch
}

func mustListChannels(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID) []storage.Channel {
	t.Helper()

	channels, err := store.ListChannelsForUser(ctx, userID, storage.ListChannelsParams{Limit: 100})
	if err != nil {
		t.Fatalf("ListChannelsForUser(%s): %v", userID, err)
	}
	return channels
}

func mustListMembers(ctx context.Context, t *testing.T, store *storage.Store, channelID uuid.UUID) []storage.User {
	t.Helper()

	members, err := store.ListChannelMembers(ctx, channelID, storage.ListChannelMembersParams{Limit: 100})
	if err != nil {
		t.Fatalf("ListChannelMembers(%s): %v", channelID, err)
	}
	return members
}

// visibleIDs indexes a channel list by id, so a visibility assertion reads
// as a set-membership question rather than a loop.
func visibleIDs(channels []storage.Channel) map[uuid.UUID]bool {
	ids := make(map[uuid.UUID]bool, len(channels))
	for _, ch := range channels {
		ids[ch.ID] = true
	}
	return ids
}

func usernamesOf(users []storage.User) []string {
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}
	return names
}

// sidebarChannel picks one channel out of the caller's own list — the query
// the sidebar actually runs, rather than a single-row read that could quietly
// compute the counts differently.
func sidebarChannel(
	ctx context.Context, t *testing.T, store *storage.Store, userID, channelID uuid.UUID,
) storage.Channel {
	t.Helper()

	for _, ch := range mustListChannels(ctx, t, store, userID) {
		if ch.ID == channelID {
			return ch
		}
	}
	t.Fatalf("channel %s is not in %s's list", channelID, userID)
	return storage.Channel{}
}

// assertCounts checks the three caller-scoped fields of a Channel, and on
// every call re-checks the invariant the sidebar depends on: the filled "@"
// badge can never exceed the plain unread count, because it counts a subset
// of the same messages.
func assertCounts(t *testing.T, ch storage.Channel, unread, mentions int, lastRead *uuid.UUID) {
	t.Helper()

	if ch.UnreadCount != unread {
		t.Errorf("unread_count = %d, want %d", ch.UnreadCount, unread)
	}
	if ch.MentionCount != mentions {
		t.Errorf("mention_count = %d, want %d", ch.MentionCount, mentions)
	}
	if ch.MentionCount > ch.UnreadCount {
		t.Errorf("mention_count %d exceeds unread_count %d; mentions are a subset of the unread messages",
			ch.MentionCount, ch.UnreadCount)
	}
	switch {
	case lastRead == nil && ch.LastReadMessageID != nil:
		t.Errorf("last_read_message_id = %s, want null", ch.LastReadMessageID)
	case lastRead != nil && ch.LastReadMessageID == nil:
		t.Errorf("last_read_message_id is null, want %s", lastRead)
	case lastRead != nil && *ch.LastReadMessageID != *lastRead:
		t.Errorf("last_read_message_id = %s, want %s", ch.LastReadMessageID, lastRead)
	}
}

// TestChannelUnreadCountsIntegration pins what the sidebar's two badges mean,
// on one channel seeded with every case the counts have to survive:
//
//	t1  bob    "b1"                    unread for alice
//	t2  alice  "a1"       mentions bob unread for bob, never for alice
//	t3  bob    "b2"     mentions alice unread for alice, and a mention
//	t4  bob    (deleted) mentions alice counted by nobody, mention included
//	t5  bob    "b3"                    unread for alice
//	t6  alice  "a2"     mentions alice counted by nobody: it is alice's own
//
// Nothing here writes a read position, so the subtests are independent and
// every one of them reads the never-read state.
func TestChannelUnreadCountsIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	alice := mustCreateUser(ctx, t, store, newUser("countalice"))
	bob := mustCreateUser(ctx, t, store, newUser("countbob"))
	channel := mustCreateChannel(ctx, t, store, newChannel("counting", storage.ChannelKindPublic, alice.ID))
	if err := store.AddChannelMember(ctx, channel.ID, bob.ID, alice.ID); err != nil {
		t.Fatalf("AddChannelMember(bob): %v", err)
	}

	seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "b1", seedAt(1))
	mentionsBob := seedMessageAtTime(ctx, t, conn, channel.ID, alice.ID, "a1", seedAt(2))
	seedMention(ctx, t, conn, mentionsBob, bob.ID)
	mentionsAlice := seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "b2", seedAt(3))
	seedMention(ctx, t, conn, mentionsAlice, alice.ID)
	deleted := seedDeletedMessageAt(ctx, t, conn, channel.ID, bob.ID, seedAt(4))
	seedMention(ctx, t, conn, deleted, alice.ID)
	seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "b3", seedAt(5))
	ownMention := seedMessageAtTime(ctx, t, conn, channel.ID, alice.ID, "a2", seedAt(6))
	seedMention(ctx, t, conn, ownMention, alice.ID)

	t.Run("a never-read channel counts every qualifying message", func(t *testing.T) {
		ch := sidebarChannel(ctx, t, store, alice.ID, channel.ID)

		// b1, b2 and b3: six messages, less alice's own two and the deleted
		// one. Of those three, only b2 mentions her — the mention in the
		// deleted message and the one in her own message are already gone.
		assertCounts(t, ch, 3, 1, nil)
		if ch.LastMessageAt == nil || !ch.LastMessageAt.Equal(seedAt(6)) {
			t.Errorf("last_message_at = %v, want the newest message's %s", ch.LastMessageAt, seedAt(6))
		}
	})

	t.Run("each member counts their own view of the same channel", func(t *testing.T) {
		ch := sidebarChannel(ctx, t, store, bob.ID, channel.ID)

		// a1 and a2, less bob's own four. a1 mentions him; a2 mentions alice.
		assertCounts(t, ch, 2, 1, nil)
	})

	t.Run("ChannelForUser agrees with the sidebar list", func(t *testing.T) {
		fromList := sidebarChannel(ctx, t, store, alice.ID, channel.ID)

		ch, err := store.ChannelForUser(ctx, channel.ID, alice.ID)
		if err != nil {
			t.Fatalf("ChannelForUser: %v", err)
		}
		assertCounts(t, ch, fromList.UnreadCount, fromList.MentionCount, fromList.LastReadMessageID)
	})

	t.Run("ChannelForUser reports an unknown channel", func(t *testing.T) {
		_, err := store.ChannelForUser(ctx, uuid.New(), alice.ID)
		if !errors.Is(err, storage.ErrChannelNotFound) {
			t.Errorf("got %v, want ErrChannelNotFound", err)
		}
	})

	// The unscoped reads have nobody to count for. They must not be mistaken
	// for "nothing unread" — this pins the documented contract so a later
	// change cannot start serving those zeros as if they were answers.
	t.Run("the unscoped reads carry no caller's counts", func(t *testing.T) {
		byID, err := store.ChannelByID(ctx, channel.ID)
		if err != nil {
			t.Fatalf("ChannelByID: %v", err)
		}
		assertCounts(t, byID, 0, 0, nil)
		if byID.LastMessageAt == nil || !byID.LastMessageAt.Equal(seedAt(6)) {
			t.Errorf("last_message_at = %v, want %s — it needs no caller", byID.LastMessageAt, seedAt(6))
		}

		updated, err := store.UpdateChannelTopic(ctx, channel.ID, "counting things")
		if err != nil {
			t.Fatalf("UpdateChannelTopic: %v", err)
		}
		assertCounts(t, updated, 0, 0, nil)
		if updated.LastMessageAt == nil || !updated.LastMessageAt.Equal(seedAt(6)) {
			t.Errorf("last_message_at = %v, want %s", updated.LastMessageAt, seedAt(6))
		}
	})

	t.Run("an empty channel has nothing to count", func(t *testing.T) {
		empty := mustCreateChannel(ctx, t, store, newChannel("silence", storage.ChannelKindPrivate, alice.ID))

		assertCounts(t, empty, 0, 0, nil)
		if empty.LastMessageAt != nil {
			t.Errorf("last_message_at = %v, want null in an empty channel", empty.LastMessageAt)
		}

		fromList := sidebarChannel(ctx, t, store, alice.ID, empty.ID)
		assertCounts(t, fromList, 0, 0, nil)
		if fromList.LastMessageAt != nil {
			t.Errorf("last_message_at = %v, want null in an empty channel", fromList.LastMessageAt)
		}
	})

	// A deleted message keeps its place in history and renders as a
	// placeholder, so it is still the newest thing in the channel even though
	// nobody's unread count includes it.
	t.Run("last_message_at includes a deleted newest message", func(t *testing.T) {
		tombstone := mustCreateChannel(ctx, t, store, newChannel("tombstone", storage.ChannelKindPrivate, alice.ID))
		if err := store.AddChannelMember(ctx, tombstone.ID, bob.ID, alice.ID); err != nil {
			t.Fatalf("AddChannelMember(bob): %v", err)
		}
		seedMessageAtTime(ctx, t, conn, tombstone.ID, bob.ID, "last words", seedAt(1))
		seedDeletedMessageAt(ctx, t, conn, tombstone.ID, bob.ID, seedAt(2))

		ch := sidebarChannel(ctx, t, store, alice.ID, tombstone.ID)
		assertCounts(t, ch, 1, 0, nil)
		if ch.LastMessageAt == nil || !ch.LastMessageAt.Equal(seedAt(2)) {
			t.Errorf("last_message_at = %v, want the deleted message's %s", ch.LastMessageAt, seedAt(2))
		}
	})

	// OpenDirectMessage returns a Channel from a query of its own — the
	// branch that finds an existing pair — so it gets its own check that the
	// counts belong to the caller and not to the other participant.
	t.Run("a direct message counts for whoever opened it", func(t *testing.T) {
		dm := mustOpenDM(ctx, t, store, alice.ID, bob.ID)
		assertCounts(t, dm, 0, 0, nil)

		seedMessageAtTime(ctx, t, conn, dm.ID, bob.ID, "hello", seedAt(1))

		reopened := mustOpenDM(ctx, t, store, alice.ID, bob.ID)
		assertCounts(t, reopened, 1, 0, nil)

		fromBob := mustOpenDM(ctx, t, store, bob.ID, alice.ID)
		assertCounts(t, fromBob, 0, 0, nil)
	})
}

// TestChannelUnreadCountsAfterReadPositionIntegration walks the read position
// forward and watches the counts shrink, then proves the position is scoped
// to exactly one (user, channel): it moves nobody else's numbers, and it says
// nothing about any other conversation.
func TestChannelUnreadCountsAfterReadPositionIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	alice := mustCreateUser(ctx, t, store, newUser("readalice"))
	bob := mustCreateUser(ctx, t, store, newUser("readbob"))
	channel := mustCreateChannel(ctx, t, store, newChannel("reading-along", storage.ChannelKindPublic, alice.ID))
	other := mustCreateChannel(ctx, t, store, newChannel("reading-other", storage.ChannelKindPublic, alice.ID))
	for _, ch := range []storage.Channel{channel, other} {
		if err := store.AddChannelMember(ctx, ch.ID, bob.ID, alice.ID); err != nil {
			t.Fatalf("AddChannelMember(bob, %s): %v", ch.ID, err)
		}
	}

	first := seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "one", seedAt(1))
	second := seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "two", seedAt(2))
	seedMention(ctx, t, conn, second, alice.ID)
	third := seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "three", seedAt(3))
	seedMention(ctx, t, conn, third, alice.ID)
	seedMessageAtTime(ctx, t, conn, other.ID, bob.ID, "elsewhere", seedAt(1))

	aliceHere := func(t *testing.T) storage.Channel {
		t.Helper()
		return sidebarChannel(ctx, t, store, alice.ID, channel.ID)
	}

	t.Run("before reading, everything counts", func(t *testing.T) {
		assertCounts(t, aliceHere(t), 3, 2, nil)
	})

	t.Run("reading forward drops the messages behind the position", func(t *testing.T) {
		mustSetReadPosition(ctx, t, store, channel.ID, alice.ID, first)
		assertCounts(t, aliceHere(t), 2, 2, &first)

		mustSetReadPosition(ctx, t, store, channel.ID, alice.ID, second)
		assertCounts(t, aliceHere(t), 1, 1, &second)

		mustSetReadPosition(ctx, t, store, channel.ID, alice.ID, third)
		assertCounts(t, aliceHere(t), 0, 0, &third)
	})

	t.Run("a stale position does not resurrect the count", func(t *testing.T) {
		if err := store.SetReadPosition(ctx, channel.ID, alice.ID, first); err != nil {
			t.Fatalf("a stale position must not be an error: %v", err)
		}
		assertCounts(t, aliceHere(t), 0, 0, &third)
	})

	t.Run("a message sent after the position counts again", func(t *testing.T) {
		fourth := seedMessageAtTime(ctx, t, conn, channel.ID, bob.ID, "four", seedAt(4))
		seedMention(ctx, t, conn, fourth, alice.ID)

		assertCounts(t, aliceHere(t), 1, 1, &third)
	})

	t.Run("another channel keeps its own count", func(t *testing.T) {
		assertCounts(t, sidebarChannel(ctx, t, store, alice.ID, other.ID), 1, 0, nil)
	})

	t.Run("the other member's counts never moved", func(t *testing.T) {
		// Everything in this channel is bob's own, so he has nothing unread
		// and — crucially — alice's read position is not his.
		assertCounts(t, sidebarChannel(ctx, t, store, bob.ID, channel.ID), 0, 0, nil)
	})
}

func TestCreateChannelIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	creator := mustCreateUser(ctx, t, store, newUser("creator"))

	kinds := []storage.ChannelKind{storage.ChannelKindPublic, storage.ChannelKindPrivate}
	for _, kind := range kinds {
		t.Run("creates a "+string(kind)+" channel whose only member is its creator", func(t *testing.T) {
			nc := newChannel(string(kind)+"-room", kind, creator.ID)
			nc.Topic = "release coordination"
			ch := mustCreateChannel(ctx, t, store, nc)

			if ch.ID == uuid.Nil {
				t.Error("created channel has nil id")
			}
			if ch.Kind != kind {
				t.Errorf("kind = %q, want %q", ch.Kind, kind)
			}
			if ch.Slug == nil || *ch.Slug != nc.Slug {
				t.Errorf("slug = %v, want %q", ch.Slug, nc.Slug)
			}
			if ch.Topic != "release coordination" {
				t.Errorf("topic = %q, want the one passed in", ch.Topic)
			}
			if ch.DMUserA != nil || ch.DMUserB != nil {
				t.Errorf("named channel carries a dm pair: %v / %v", ch.DMUserA, ch.DMUserB)
			}
			if ch.CreatedBy == nil || *ch.CreatedBy != creator.ID {
				t.Errorf("created_by = %v, want %s", ch.CreatedBy, creator.ID)
			}
			if ch.MemberCount != 1 {
				t.Errorf("member count = %d, want 1", ch.MemberCount)
			}
			if ch.CreatedAt.IsZero() || ch.UpdatedAt.IsZero() {
				t.Error("timestamps not populated")
			}

			if got := usernamesOf(mustListMembers(ctx, t, store, ch.ID)); !slices.Equal(got, []string{creator.Username}) {
				t.Errorf("members = %v, want [%s]", got, creator.Username)
			}
			member, err := store.IsChannelMember(ctx, ch.ID, creator.ID)
			if err != nil {
				t.Fatalf("IsChannelMember: %v", err)
			}
			if !member {
				t.Error("the creator is not a member of the channel they created")
			}
		})
	}

	t.Run("a duplicate slug is ErrChannelSlugTaken whatever the kind", func(t *testing.T) {
		mustCreateChannel(ctx, t, store, newChannel("deploys", storage.ChannelKindPublic, creator.ID))

		_, err := store.CreateChannel(ctx, newChannel("deploys", storage.ChannelKindPrivate, creator.ID))
		if !errors.Is(err, storage.ErrChannelSlugTaken) {
			t.Errorf("got %v, want ErrChannelSlugTaken", err)
		}
	})

	t.Run("a direct message cannot be created here", func(t *testing.T) {
		_, err := store.CreateChannel(ctx, newChannel("not-a-dm", storage.ChannelKindDM, creator.ID))
		if err == nil {
			t.Fatal("CreateChannel accepted kind dm; direct messages go through OpenDirectMessage")
		}
	})

	t.Run("an unknown creator is ErrNotFound", func(t *testing.T) {
		_, err := store.CreateChannel(ctx, newChannel("ghost-room", storage.ChannelKindPublic, uuid.New()))
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})
}

func TestChannelByIDIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	creator := mustCreateUser(ctx, t, store, newUser("owner"))
	created := mustCreateChannel(ctx, t, store, newChannel("general", storage.ChannelKindPublic, creator.ID))

	t.Run("reads a channel back", func(t *testing.T) {
		got, err := store.ChannelByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("ChannelByID: %v", err)
		}
		if got.ID != created.ID || got.Slug == nil || *got.Slug != "general" {
			t.Errorf("ChannelByID returned %+v", got)
		}
	})

	t.Run("an unknown id is ErrChannelNotFound", func(t *testing.T) {
		_, err := store.ChannelByID(ctx, uuid.New())
		if !errors.Is(err, storage.ErrChannelNotFound) {
			t.Errorf("got %v, want ErrChannelNotFound", err)
		}
	})
}

func TestUpdateChannelTopicIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	owner := mustCreateUser(ctx, t, store, newUser("owner"))
	peer := mustCreateUser(ctx, t, store, newUser("peer"))
	created := mustCreateChannel(ctx, t, store, newChannel("topical", storage.ChannelKindPublic, owner.ID))

	t.Run("sets and clears the topic", func(t *testing.T) {
		updated, err := store.UpdateChannelTopic(ctx, created.ID, "ship it")
		if err != nil {
			t.Fatalf("UpdateChannelTopic: %v", err)
		}
		if updated.Topic != "ship it" {
			t.Errorf("topic = %q, want %q", updated.Topic, "ship it")
		}
		if updated.MemberCount != 1 {
			t.Errorf("member count = %d, want 1", updated.MemberCount)
		}
		if updated.UpdatedAt.Before(updated.CreatedAt) {
			t.Error("updated_at moved backwards")
		}

		reread, err := store.ChannelByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("ChannelByID: %v", err)
		}
		if reread.Topic != "ship it" {
			t.Errorf("stored topic = %q, want %q", reread.Topic, "ship it")
		}

		cleared, err := store.UpdateChannelTopic(ctx, created.ID, "")
		if err != nil {
			t.Fatalf("UpdateChannelTopic(clear): %v", err)
		}
		if cleared.Topic != "" {
			t.Errorf("topic = %q, want it cleared", cleared.Topic)
		}
	})

	t.Run("an unknown channel is ErrChannelNotFound", func(t *testing.T) {
		_, err := store.UpdateChannelTopic(ctx, uuid.New(), "nowhere")
		if !errors.Is(err, storage.ErrChannelNotFound) {
			t.Errorf("got %v, want ErrChannelNotFound", err)
		}
	})

	t.Run("a direct message has no topic to set", func(t *testing.T) {
		dm := mustOpenDM(ctx, t, store, owner.ID, peer.ID)

		_, err := store.UpdateChannelTopic(ctx, dm.ID, "not allowed")
		if !errors.Is(err, storage.ErrDMHasNoTopic) {
			t.Errorf("got %v, want ErrDMHasNoTopic", err)
		}
	})
}

// TestListChannelsForUserIntegration pins the visibility rule of Phase 1.2:
// membership, and nothing else, decides what a user sees — for a public
// channel exactly as for a private one (openapi.yaml listChannels and
// getChannel, migration 0003's header). Dropping the membership join would
// fail every assertion in the first subtest, which is the point: the scoping
// has to stay in SQL.
func TestListChannelsForUserIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	bob := mustCreateUser(ctx, t, store, newUser("bob"))
	carol := mustCreateUser(ctx, t, store, newUser("carol"))

	alicePublic := mustCreateChannel(ctx, t, store, newChannel("alice-public", storage.ChannelKindPublic, alice.ID))
	alicePrivate := mustCreateChannel(ctx, t, store, newChannel("alice-private", storage.ChannelKindPrivate, alice.ID))
	aliceCarolDM := mustOpenDM(ctx, t, store, alice.ID, carol.ID)
	bobPublic := mustCreateChannel(ctx, t, store, newChannel("bob-public", storage.ChannelKindPublic, bob.ID))

	t.Run("channels the caller is not a member of never appear", func(t *testing.T) {
		visible := visibleIDs(mustListChannels(ctx, t, store, bob.ID))

		if !visible[bobPublic.ID] {
			t.Error("bob cannot see his own channel")
		}
		if visible[alicePrivate.ID] {
			t.Error("a private channel leaked to a non-member")
		}
		if visible[alicePublic.ID] {
			t.Error("a public channel leaked to a non-member; membership is the only visibility rule in Phase 1.2")
		}
		if visible[aliceCarolDM.ID] {
			t.Error("somebody else's direct message leaked")
		}
		if len(visible) != 1 {
			t.Errorf("bob sees %d channels, want exactly the one he belongs to", len(visible))
		}
	})

	t.Run("every member of a channel sees it", func(t *testing.T) {
		if err := store.AddChannelMember(ctx, alicePublic.ID, bob.ID, alice.ID); err != nil {
			t.Fatalf("AddChannelMember(public): %v", err)
		}
		if err := store.AddChannelMember(ctx, alicePrivate.ID, bob.ID, alice.ID); err != nil {
			t.Fatalf("AddChannelMember(private): %v", err)
		}

		visible := visibleIDs(mustListChannels(ctx, t, store, bob.ID))
		if !visible[alicePublic.ID] {
			t.Error("bob was added to a public channel but does not see it")
		}
		if !visible[alicePrivate.ID] {
			t.Error("bob was added to a private channel but does not see it")
		}
	})

	t.Run("both sides of a direct message see it", func(t *testing.T) {
		for _, u := range []storage.User{alice, carol} {
			if !visibleIDs(mustListChannels(ctx, t, store, u.ID))[aliceCarolDM.ID] {
				t.Errorf("%s cannot see their own direct message", u.Username)
			}
		}
	})

	t.Run("leaving a channel hides it again", func(t *testing.T) {
		if err := store.RemoveChannelMember(ctx, alicePrivate.ID, bob.ID); err != nil {
			t.Fatalf("RemoveChannelMember: %v", err)
		}
		if visibleIDs(mustListChannels(ctx, t, store, bob.ID))[alicePrivate.ID] {
			t.Error("a private channel stayed visible after the member left")
		}
	})

	t.Run("a user with no memberships sees nothing", func(t *testing.T) {
		stranger := mustCreateUser(ctx, t, store, newUser("stranger"))

		if channels := mustListChannels(ctx, t, store, stranger.ID); len(channels) != 0 {
			t.Errorf("a stranger sees %d channels, want none", len(channels))
		}
	})
}

// TestListChannelsForUserKeysetIntegration walks one user's conversations
// through the cursor and proves every one comes back exactly once, in
// (created_at, id) order.
func TestListChannelsForUserKeysetIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	owner := mustCreateUser(ctx, t, store, newUser("owner"))

	const total = 7
	want := map[uuid.UUID]bool{}
	for i := range total {
		ch := mustCreateChannel(ctx, t, store, newChannel(fmt.Sprintf("room-%02d", i), storage.ChannelKindPrivate, owner.ID))
		want[ch.ID] = true
	}

	seen := map[uuid.UUID]int{}
	var walked []storage.Channel
	var after *storage.ChannelCursor
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination never terminates")
		}
		page, err := store.ListChannelsForUser(ctx, owner.ID, storage.ListChannelsParams{After: after, Limit: 3})
		if err != nil {
			t.Fatalf("ListChannelsForUser page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		walked = append(walked, page...)
		for _, ch := range page {
			seen[ch.ID]++
		}
		last := page[len(page)-1]
		after = &storage.ChannelCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	if len(seen) != total {
		t.Errorf("walked %d distinct channels, want %d", len(seen), total)
	}
	for id, count := range seen {
		if !want[id] {
			t.Errorf("channel %s is not one of the owner's", id)
		}
		if count != 1 {
			t.Errorf("channel %s appeared %d times, want exactly once", id, count)
		}
	}
	for i := 1; i < len(walked); i++ {
		prev, cur := walked[i-1], walked[i]
		if cur.CreatedAt.Before(prev.CreatedAt) {
			t.Errorf("walk out of order: %s before %s", prev.ID, cur.ID)
		}
		if cur.CreatedAt.Equal(prev.CreatedAt) && cur.ID.String() <= prev.ID.String() {
			t.Errorf("tie not broken by id: %s then %s", prev.ID, cur.ID)
		}
	}
}

func TestOpenDirectMessageIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	bob := mustCreateUser(ctx, t, store, newUser("bob"))

	t.Run("the first open creates the channel", func(t *testing.T) {
		dm, created, err := store.OpenDirectMessage(ctx, alice.ID, bob.ID)
		if err != nil {
			t.Fatalf("OpenDirectMessage: %v", err)
		}
		if !created {
			t.Error("created = false on the first open")
		}
		if dm.Kind != storage.ChannelKindDM {
			t.Errorf("kind = %q, want dm", dm.Kind)
		}
		if dm.Slug != nil {
			t.Errorf("slug = %v, want nil on a direct message", dm.Slug)
		}
		if dm.Topic != "" {
			t.Errorf("topic = %q, want empty on a direct message", dm.Topic)
		}
		if dm.DMUserA == nil || dm.DMUserB == nil {
			t.Fatalf("dm pair not populated: %v / %v", dm.DMUserA, dm.DMUserB)
		}
		pair := []uuid.UUID{*dm.DMUserA, *dm.DMUserB}
		if !slices.Contains(pair, alice.ID) || !slices.Contains(pair, bob.ID) {
			t.Errorf("dm pair = %v, want alice and bob", pair)
		}
		if dm.MemberCount != 2 {
			t.Errorf("member count = %d, want 2", dm.MemberCount)
		}

		if names := usernamesOf(mustListMembers(ctx, t, store, dm.ID)); !slices.Equal(names, []string{"alice", "bob"}) {
			t.Errorf("members = %v, want [alice bob]", names)
		}
	})

	t.Run("opening it again from either side returns the same channel", func(t *testing.T) {
		first, _, err := store.OpenDirectMessage(ctx, alice.ID, bob.ID)
		if err != nil {
			t.Fatalf("OpenDirectMessage(alice, bob): %v", err)
		}

		again, created, err := store.OpenDirectMessage(ctx, alice.ID, bob.ID)
		if err != nil {
			t.Fatalf("OpenDirectMessage(alice, bob) again: %v", err)
		}
		if created {
			t.Error("created = true on a repeat open")
		}
		if again.ID != first.ID {
			t.Errorf("repeat open returned %s, want %s", again.ID, first.ID)
		}

		reversed, created, err := store.OpenDirectMessage(ctx, bob.ID, alice.ID)
		if err != nil {
			t.Fatalf("OpenDirectMessage(bob, alice): %v", err)
		}
		if created {
			t.Error("created = true when the other side opened the same pair")
		}
		if reversed.ID != first.ID {
			t.Errorf("the reversed pair opened %s, want %s", reversed.ID, first.ID)
		}
	})

	t.Run("a direct message with yourself is refused", func(t *testing.T) {
		_, _, err := store.OpenDirectMessage(ctx, alice.ID, alice.ID)
		if !errors.Is(err, storage.ErrDMWithSelf) {
			t.Errorf("got %v, want ErrDMWithSelf", err)
		}
	})

	t.Run("an unknown peer is ErrNotFound", func(t *testing.T) {
		_, _, err := store.OpenDirectMessage(ctx, alice.ID, uuid.New())
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("a direct message's membership is fixed", func(t *testing.T) {
		dm := mustOpenDM(ctx, t, store, alice.ID, bob.ID)
		carol := mustCreateUser(ctx, t, store, newUser("carol"))

		if err := store.AddChannelMember(ctx, dm.ID, carol.ID, alice.ID); !errors.Is(err, storage.ErrDMMembershipFixed) {
			t.Errorf("AddChannelMember on a dm: got %v, want ErrDMMembershipFixed", err)
		}
		if err := store.RemoveChannelMember(ctx, dm.ID, bob.ID); !errors.Is(err, storage.ErrDMMembershipFixed) {
			t.Errorf("RemoveChannelMember on a dm: got %v, want ErrDMMembershipFixed", err)
		}
		if got := mustListMembers(ctx, t, store, dm.ID); len(got) != 2 {
			t.Errorf("the dm has %d members, want 2", len(got))
		}
	})
}

// TestOpenDirectMessageConcurrentIntegration is why the insert leans on the
// partial unique index instead of a read-then-write: several people opening
// the same pair at once must end up with one channel, one creator, and no
// error.
func TestOpenDirectMessageConcurrentIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	bob := mustCreateUser(ctx, t, store, newUser("bob"))

	const openers = 8
	type outcome struct {
		channelID uuid.UUID
		created   bool
		err       error
	}
	outcomes := make([]outcome, openers)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Half the callers open the pair from the other side, so the
			// canonical ordering is exercised under contention too.
			caller, peer := alice.ID, bob.ID
			if i%2 == 1 {
				caller, peer = bob.ID, alice.ID
			}
			<-start
			ch, created, err := store.OpenDirectMessage(ctx, caller, peer)
			outcomes[i] = outcome{channelID: ch.ID, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()

	creations := 0
	ids := map[uuid.UUID]bool{}
	for i, got := range outcomes {
		if got.err != nil {
			t.Errorf("opener %d: %v", i, got.err)
			continue
		}
		if got.created {
			creations++
		}
		ids[got.channelID] = true
	}
	if creations != 1 {
		t.Errorf("%d openers reported creating the channel, want exactly 1", creations)
	}
	if len(ids) != 1 {
		t.Errorf("the openers landed on %d distinct channels, want 1", len(ids))
	}

	pool := assertPool(ctx, t, dsn)
	var rows int
	err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM channels
		 WHERE kind = 'dm'
		   AND dm_user_a = least($1::uuid, $2::uuid)
		   AND dm_user_b = greatest($1::uuid, $2::uuid)`,
		alice.ID, bob.ID,
	).Scan(&rows)
	if err != nil {
		t.Fatalf("count dm rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d dm rows exist for the pair, want 1", rows)
	}
}

func TestChannelMembersIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	owner := mustCreateUser(ctx, t, store, newUser("owner"))
	invitee := mustCreateUser(ctx, t, store, newUser("invitee"))

	t.Run("adding is idempotent", func(t *testing.T) {
		ch := mustCreateChannel(ctx, t, store, newChannel("idempotent", storage.ChannelKindPrivate, owner.ID))

		for attempt := range 2 {
			if err := store.AddChannelMember(ctx, ch.ID, invitee.ID, owner.ID); err != nil {
				t.Fatalf("AddChannelMember attempt %d: %v", attempt+1, err)
			}
		}

		if got := usernamesOf(mustListMembers(ctx, t, store, ch.ID)); !slices.Equal(got, []string{"invitee", "owner"}) {
			t.Errorf("members = %v, want [invitee owner]", got)
		}
		reread, err := store.ChannelByID(ctx, ch.ID)
		if err != nil {
			t.Fatalf("ChannelByID: %v", err)
		}
		if reread.MemberCount != 2 {
			t.Errorf("member count = %d, want 2", reread.MemberCount)
		}
	})

	t.Run("removing is idempotent", func(t *testing.T) {
		ch := mustCreateChannel(ctx, t, store, newChannel("leavable", storage.ChannelKindPrivate, owner.ID))
		if err := store.AddChannelMember(ctx, ch.ID, invitee.ID, owner.ID); err != nil {
			t.Fatalf("AddChannelMember: %v", err)
		}

		for attempt := range 2 {
			if err := store.RemoveChannelMember(ctx, ch.ID, invitee.ID); err != nil {
				t.Fatalf("RemoveChannelMember attempt %d: %v", attempt+1, err)
			}
		}

		member, err := store.IsChannelMember(ctx, ch.ID, invitee.ID)
		if err != nil {
			t.Fatalf("IsChannelMember: %v", err)
		}
		if member {
			t.Error("the removed user is still a member")
		}
	})

	t.Run("IsChannelMember answers for members, non-members and unknown ids", func(t *testing.T) {
		ch := mustCreateChannel(ctx, t, store, newChannel("exists-check", storage.ChannelKindPublic, owner.ID))
		outsider := mustCreateUser(ctx, t, store, newUser("outsider"))

		cases := []struct {
			name      string
			channelID uuid.UUID
			userID    uuid.UUID
			want      bool
		}{
			{"member", ch.ID, owner.ID, true},
			{"non-member", ch.ID, outsider.ID, false},
			{"unknown channel", uuid.New(), owner.ID, false},
			{"unknown user", ch.ID, uuid.New(), false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := store.IsChannelMember(ctx, tc.channelID, tc.userID)
				if err != nil {
					t.Fatalf("IsChannelMember: %v", err)
				}
				if got != tc.want {
					t.Errorf("IsChannelMember = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("an unknown user cannot be added", func(t *testing.T) {
		ch := mustCreateChannel(ctx, t, store, newChannel("no-ghosts", storage.ChannelKindPublic, owner.ID))

		if err := store.AddChannelMember(ctx, ch.ID, uuid.New(), owner.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("an unknown channel is ErrChannelNotFound", func(t *testing.T) {
		if err := store.AddChannelMember(ctx, uuid.New(), invitee.ID, owner.ID); !errors.Is(err, storage.ErrChannelNotFound) {
			t.Errorf("AddChannelMember: got %v, want ErrChannelNotFound", err)
		}
		if err := store.RemoveChannelMember(ctx, uuid.New(), invitee.ID); !errors.Is(err, storage.ErrChannelNotFound) {
			t.Errorf("RemoveChannelMember: got %v, want ErrChannelNotFound", err)
		}
	})

	t.Run("members page in username order", func(t *testing.T) {
		ch := mustCreateChannel(ctx, t, store, newChannel("paged", storage.ChannelKindPrivate, owner.ID))
		for _, name := range []string{"dana", "bruno", "chiara"} {
			u := mustCreateUser(ctx, t, store, newUser(name))
			if err := store.AddChannelMember(ctx, ch.ID, u.ID, owner.ID); err != nil {
				t.Fatalf("AddChannelMember(%s): %v", name, err)
			}
		}

		var walked []string
		var after *storage.ChannelMemberCursor
		for pages := 0; ; pages++ {
			if pages > 4 {
				t.Fatal("pagination never terminates")
			}
			page, err := store.ListChannelMembers(ctx, ch.ID, storage.ListChannelMembersParams{After: after, Limit: 2})
			if err != nil {
				t.Fatalf("ListChannelMembers page %d: %v", pages, err)
			}
			if len(page) == 0 {
				break
			}
			walked = append(walked, usernamesOf(page)...)
			last := page[len(page)-1]
			after = &storage.ChannelMemberCursor{Username: last.Username, UserID: last.ID}
		}

		if want := []string{"bruno", "chiara", "dana", "owner"}; !slices.Equal(walked, want) {
			t.Errorf("walked %v, want %v", walked, want)
		}
	})

	t.Run("listing an unknown channel's members is empty", func(t *testing.T) {
		got, err := store.ListChannelMembers(ctx, uuid.New(), storage.ListChannelMembersParams{Limit: 10})
		if err != nil {
			t.Fatalf("ListChannelMembers: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d members, want none", len(got))
		}
	})
}
