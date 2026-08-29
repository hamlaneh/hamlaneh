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
func e2eeMessageWorld(ctx context.Context, t *testing.T, slug string) (*storage.Store, storage.User, uuid.UUID) {
	t.Helper()

	store, _ := testdb.New(t)
	author := mustCreateUser(ctx, t, store, newUser(slug+"author"))
	ch := mustCreateChannel(ctx, t, store, storage.NewChannel{
		Kind:      storage.ChannelKindPrivate,
		Slug:      slug,
		E2EE:      true,
		CreatedBy: author.ID,
	})
	return store, author, ch.ID
}

func TestEncryptedMessageRoundTripIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, author, channelID := e2eeMessageWorld(ctx, t, "e2eeround")

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
	store, author, channelID := e2eeMessageWorld(ctx, t, "e2eedelete")

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
	store, author, channelID := e2eeMessageWorld(ctx, t, "e2eesearch")

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
