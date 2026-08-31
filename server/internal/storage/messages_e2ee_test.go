package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The encrypted half of a message, stored and read back (migration 0017).
//
// What these pin is not "the columns round-trip" — that is table stakes — but
// the two invariants everything above them rests on: a deleted message loses
// its ciphertext exactly as it loses its words, and nothing the server can
// search ever held them in the first place.

// e2eeMessageWorld provisions a store, an author, and a channel they are in.
// The raw handle comes back with them because the mention rows below are
// asserted straight from SQL — the table the badge counts, not whatever a
// write reported back.
func e2eeMessageWorld(ctx context.Context, t *testing.T, slug string) (testdb.Store, *testdb.Raw, storage.User, uuid.UUID) {
	t.Helper()

	store, conn := testdb.New(t)
	author := mustCreateUser(ctx, t, store, newUser(slug+"author"))
	ch := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind:      storage.ChannelKindPrivate,
		Slug:      slug,
		E2EE:      true,
		CreatedBy: author.ID,
	})
	return store, conn, author, ch.ID
}

func TestEncryptedMessageRoundTripIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, author, channelID := e2eeMessageWorld(ctx, t, "e2eeround")

	sent, created, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Content:     "",
		Mls:         &storage.MessageMls{Epoch: 7, Ciphertext: []byte("ciphertext-bytes")},
	})
	if err != nil || !created {
		t.Fatalf("CreateMessage: %v (created=%v)", err, created)
	}
	// An encrypted message is not an empty one, even though its searchable
	// column is empty: the ciphertext is the body.
	if sent.Mls == nil || sent.Mls.Epoch != 7 || string(sent.Mls.Ciphertext) != "ciphertext-bytes" {
		t.Fatalf("stored envelope = %+v, want epoch 7 and the ciphertext", sent.Mls)
	}
	if sent.Content != "" {
		t.Errorf("content = %q, want empty on an encrypted message", sent.Content)
	}

	read, err := store.MessageByID(ctx, channelID, sent.ID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	if read.Mls == nil || string(read.Mls.Ciphertext) != "ciphertext-bytes" {
		t.Errorf("read back %+v, want the envelope", read.Mls)
	}

	// An edit replaces the ciphertext and stamps edited_at, exactly as a
	// plaintext edit replaces the words.
	edited, err := store.UpdateMessageContent(ctx, channelID, sent.ID, "",
		&storage.MessageMls{Epoch: 8, Ciphertext: []byte("replacement")})
	if err != nil {
		t.Fatalf("UpdateMessageContent: %v", err)
	}
	if edited.Mls == nil || edited.Mls.Epoch != 8 || string(edited.Mls.Ciphertext) != "replacement" {
		t.Errorf("edited envelope = %+v, want the replacement at epoch 8", edited.Mls)
	}
	if edited.EditedAt == nil {
		t.Error("edited_at is nil; an encrypted edit is still an edit")
	}
}

// TestSoftDeleteErasesCiphertextIntegration is the deletion promise for an
// encrypted conversation.
//
// A deleted message keeps its place and loses its words in both worlds. If
// the delete cleared content and left mls_ciphertext, the server would still
// be holding the message everybody was told was gone — and it is the one
// thing about that message it was never supposed to keep.
func TestSoftDeleteErasesCiphertextIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, author, channelID := e2eeMessageWorld(ctx, t, "e2eedelete")

	sent, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Mls:         &storage.MessageMls{Epoch: 3, Ciphertext: []byte("the secret")},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	deleted, err := store.SoftDeleteMessage(ctx, channelID, sent.ID, author.ID)
	if err != nil {
		t.Fatalf("SoftDeleteMessage: %v", err)
	}
	if deleted.Mls != nil {
		t.Errorf("the delete returned an envelope %+v; want none", deleted.Mls)
	}
	if deleted.Content != "" || deleted.DeletedAt == nil {
		t.Errorf("deleted row = content %q, deleted_at %v", deleted.Content, deleted.DeletedAt)
	}

	// Read back from the database, not from what the delete happened to
	// return: what matters is the row, not the response.
	after, err := store.MessageByID(ctx, channelID, sent.ID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	if after.Mls != nil {
		t.Errorf("the stored row still carries %+v after deletion", after.Mls)
	}
	if after.Content != "" {
		t.Errorf("content = %q after deletion, want empty", after.Content)
	}

	// And a page of history says the same, since that is what a client
	// actually reads.
	page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("history holds %d messages, want the placeholder", len(page.Messages))
	}
	if page.Messages[0].Mls != nil {
		t.Errorf("the placeholder in history still carries %+v", page.Messages[0].Mls)
	}
}

// TestEncryptedMentionsAreDeclaredIntegration is ADR 014: on an encrypted
// channel the mention rows come from the sender's declaration instead of from
// a content parse, because there is no content to parse.
//
// What it pins is that the declaration buys a sender nothing the parse did not
// already give them. The same channel_members join drops a stranger, the same
// primary key collapses a repeat, and an edit rewrites the set — so the one
// guarantee the plaintext path had survives the switch: no mention row can name
// somebody who was not entitled to read the message it points at.
func TestEncryptedMentionsAreDeclaredIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, conn, author, channelID := e2eeMessageWorld(ctx, t, "e2eementions")

	member := mustCreateUser(ctx, t, store, newUser("e2eementionmember"))
	stranger := mustCreateUser(ctx, t, store, newUser("e2eementionstranger"))
	seedMembership(ctx, t, conn, channelID, author.ID)
	seedMembership(ctx, t, conn, channelID, member.ID)

	send := func(t *testing.T, mentions []uuid.UUID) storage.Message {
		t.Helper()
		msg, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID, ClientMsgID: uuid.New(),
			Mls: &storage.MessageMls{Epoch: 1, Ciphertext: []byte("opaque"), Mentions: mentions},
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return msg
	}

	t.Run("a declared member gets a row", func(t *testing.T) {
		msg := send(t, []uuid.UUID{member.ID})
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("a declared stranger is dropped, and does not fail the message", func(t *testing.T) {
		// The whole reason the declaration is safe to trust: the join, not the
		// sender, decides who can end up in the table.
		msg := send(t, []uuid.UUID{stranger.ID, member.ID})
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("an id naming nobody is dropped rather than erroring", func(t *testing.T) {
		msg := send(t, []uuid.UUID{uuid.New()})
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), nil)
	})

	t.Run("a name declared twice writes one row", func(t *testing.T) {
		msg := send(t, []uuid.UUID{member.ID, member.ID})
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("declaring nobody writes nothing", func(t *testing.T) {
		msg := send(t, nil)
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), nil)
	})

	// An edit re-declares, exactly as a plaintext edit re-parses: the rows the
	// new declaration keeps stay, the ones it drops go. Omission is not "leave
	// them alone" — it is "this message now mentions nobody", or an author could
	// never take back a ping.
	t.Run("an edit rewrites the declared set", func(t *testing.T) {
		msg := send(t, []uuid.UUID{member.ID})

		if _, err := store.UpdateMessageContent(ctx, channelID, msg.ID, "",
			&storage.MessageMls{Epoch: 2, Ciphertext: []byte("edited"), Mentions: []uuid.UUID{author.ID}}); err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{author.ID})

		if _, err := store.UpdateMessageContent(ctx, channelID, msg.ID, "",
			&storage.MessageMls{Epoch: 3, Ciphertext: []byte("edited again")}); err != nil {
			t.Fatalf("UpdateMessageContent (clearing): %v", err)
		}
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), nil)
	})

	// The defect this closes, stated as the person experiences it: the badge.
	t.Run("the sidebar badge the rows feed moves", func(t *testing.T) {
		badged := mustCreateChannel(ctx, t, store, storage.NewChannel{
			Kind: storage.ChannelKindPrivate, Slug: "e2eementionbadge",
			E2EE: true, CreatedBy: author.ID,
		})
		seedMembership(ctx, t, conn, badged.ID, author.ID)
		seedMembership(ctx, t, conn, badged.ID, member.ID)

		for _, mentions := range [][]uuid.UUID{nil, {member.ID}} {
			if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
				ChannelID: badged.ID, AuthorID: author.ID, ClientMsgID: uuid.New(),
				Mls: &storage.MessageMls{Epoch: 1, Ciphertext: []byte("opaque"), Mentions: mentions},
			}); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
		}

		seen, err := store.ChannelForUser(ctx, badged.ID, member.ID)
		if err != nil {
			t.Fatalf("ChannelForUser: %v", err)
		}
		if seen.UnreadCount != 2 || seen.MentionCount != 1 {
			t.Errorf("unread=%d mention=%d, want 2/1; the badge is the whole defect",
				seen.UnreadCount, seen.MentionCount)
		}
	})
}

// TestSearchCannotSeeEncryptedContentIntegration pins the invariant the whole
// e2ee boundary exists to produce: nothing the server can search ever held
// the words.
//
// It is written as a search over the ciphertext's own bytes as well as over
// ordinary needles, because the failure this guards against is not "search
// works too well" — it is somebody later deciding to store something readable
// beside the ciphertext for convenience. Any such change makes one of these
// searches return a row, and the build turns red.
func TestSearchCannotSeeEncryptedContentIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, author, channelID := e2eeMessageWorld(ctx, t, "e2eesearch")

	// The "plaintext" the client encrypted. It never reaches the server, and
	// this test's job is to prove that stays true.
	const secret = "the merger closes on friday"
	if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Mls:         &storage.MessageMls{Epoch: 1, Ciphertext: []byte(secret)},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// A plaintext message in a different channel, so the search is known to
	// work at all — a test where every query returns nothing would pass just
	// as well against a broken search.
	plainChannel := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind: storage.ChannelKindPrivate, Slug: "e2eesearchplain", CreatedBy: author.ID,
	})
	if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   plainChannel.ID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Content:     "the picnic is on saturday",
	}); err != nil {
		t.Fatalf("CreateMessage (plaintext): %v", err)
	}

	search := func(t *testing.T, needle string) int {
		t.Helper()
		page, err := store.SearchMessages(ctx, storage.SearchMessagesParams{
			UserID: author.ID, Query: needle, Limit: 20,
		})
		if err != nil {
			t.Fatalf("SearchMessages(%q): %v", needle, err)
		}
		return len(page.Results)
	}

	if got := search(t, "saturday"); got != 1 {
		t.Fatalf("the plaintext message is unfindable (%d results); this test proves nothing until it is", got)
	}

	// Every needle drawn from the encrypted message must find nothing: the
	// whole ciphertext, a word of it, and the empty-ish fragments a scan
	// would match if the column held anything at all.
	for _, needle := range []string{secret, "merger", "friday", "closes on"} {
		if got := search(t, needle); got != 0 {
			t.Errorf("searching %q found %d encrypted messages; the server must hold nothing it can read", needle, got)
		}
	}
}
