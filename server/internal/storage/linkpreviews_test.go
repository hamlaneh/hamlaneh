package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// TestLinkPreviewsIntegration exercises the 0008 table against real
// PostgreSQL: store, replace, read a page's worth in one round trip, remove,
// and the two ways a card can legitimately be missing.
func TestLinkPreviewsIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("previewauthor"))
	channelID := seedMessagesChannel(ctx, t, conn, "previews")

	withLink, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Content:     "look at https://example.test/post",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	plain, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channelID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Content:     "no links here",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	blobID := uuid.New()
	preview := storage.LinkPreview{
		MessageID:   withLink.ID,
		URL:         "https://example.test/post",
		Title:       "عنوان",
		Description: "A description",
		ImageBlobID: blobID,
	}

	t.Run("save then read back", func(t *testing.T) {
		if saveErr := store.SaveLinkPreview(ctx, preview); saveErr != nil {
			t.Fatalf("SaveLinkPreview: %v", saveErr)
		}

		got, readErr := store.LinkPreviewsByMessage(ctx, []uuid.UUID{withLink.ID, plain.ID})
		if readErr != nil {
			t.Fatalf("LinkPreviewsByMessage: %v", readErr)
		}
		if len(got) != 1 {
			t.Fatalf("read %d previews, want 1 — a message with no card must be absent, not empty", len(got))
		}
		stored := got[withLink.ID]
		if stored.URL != preview.URL || stored.Title != preview.Title ||
			stored.Description != preview.Description || stored.ImageBlobID != blobID {
			t.Errorf("stored = %+v, want %+v", stored, preview)
		}
		if stored.FetchedAt.IsZero() {
			t.Error("fetched_at was not stamped")
		}
	})

	t.Run("re-enrichment replaces the card rather than adding one", func(t *testing.T) {
		replacement := preview
		replacement.Title = "A new title"
		replacement.ImageBlobID = uuid.Nil
		if saveErr := store.SaveLinkPreview(ctx, replacement); saveErr != nil {
			t.Fatalf("SaveLinkPreview: %v", saveErr)
		}

		got, readErr := store.LinkPreviewsByMessage(ctx, []uuid.UUID{withLink.ID})
		if readErr != nil {
			t.Fatalf("LinkPreviewsByMessage: %v", readErr)
		}
		if len(got) != 1 {
			t.Fatalf("read %d previews, want 1", len(got))
		}
		if got[withLink.ID].Title != "A new title" {
			t.Errorf("title = %q, want the replacement's", got[withLink.ID].Title)
		}
		// The empty halves came back empty rather than as an empty string
		// pretending to be a value the page offered.
		if got[withLink.ID].ImageBlobID != uuid.Nil {
			t.Errorf("image blob = %s, want none", got[withLink.ID].ImageBlobID)
		}
	})

	t.Run("empty title and description store as NULL", func(t *testing.T) {
		bare := storage.LinkPreview{MessageID: plain.ID, URL: "https://example.test/bare"}
		if saveErr := store.SaveLinkPreview(ctx, bare); saveErr != nil {
			t.Fatalf("SaveLinkPreview: %v", saveErr)
		}

		var titleIsNull, descriptionIsNull, imageIsNull bool
		queryErr := conn.QueryRow(ctx,
			`SELECT title IS NULL, description IS NULL, image_blob_id IS NULL
			 FROM link_previews WHERE message_id = $1`, plain.ID,
		).Scan(&titleIsNull, &descriptionIsNull, &imageIsNull)
		if queryErr != nil {
			t.Fatalf("read nullability: %v", queryErr)
		}
		if !titleIsNull || !descriptionIsNull || !imageIsNull {
			t.Errorf("nulls = (%t, %t, %t), want all true", titleIsNull, descriptionIsNull, imageIsNull)
		}
	})

	t.Run("delete reports whether there was a card", func(t *testing.T) {
		removed, delErr := store.DeleteLinkPreview(ctx, plain.ID)
		if delErr != nil {
			t.Fatalf("DeleteLinkPreview: %v", delErr)
		}
		if !removed {
			t.Error("removing an existing card reported false")
		}

		removed, delErr = store.DeleteLinkPreview(ctx, plain.ID)
		if delErr != nil {
			t.Fatalf("second DeleteLinkPreview: %v", delErr)
		}
		if removed {
			t.Error("removing nothing reported true")
		}
	})

	t.Run("no ids is no query", func(t *testing.T) {
		got, readErr := store.LinkPreviewsByMessage(ctx, nil)
		if readErr != nil || len(got) != 0 {
			t.Fatalf("LinkPreviewsByMessage(nil) = %v, %v", got, readErr)
		}
	})

	t.Run("a card for a message that does not exist is ErrNotFound", func(t *testing.T) {
		err := store.SaveLinkPreview(ctx, storage.LinkPreview{
			MessageID: uuid.New(),
			URL:       "https://example.test/gone",
			Title:     "gone",
		})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("SaveLinkPreview for a missing message: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("hard-deleting the message takes the card with it", func(t *testing.T) {
		doomed, _, createErr := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID:   channelID,
			AuthorID:    author.ID,
			ClientMsgID: uuid.New(),
			Content:     "https://example.test/doomed",
		})
		if createErr != nil {
			t.Fatalf("CreateMessage: %v", createErr)
		}
		if saveErr := store.SaveLinkPreview(ctx, storage.LinkPreview{
			MessageID: doomed.ID, URL: "https://example.test/doomed", Title: "doomed",
		}); saveErr != nil {
			t.Fatalf("SaveLinkPreview: %v", saveErr)
		}

		if _, execErr := conn.Exec(ctx, `DELETE FROM messages WHERE id = $1`, doomed.ID); execErr != nil {
			t.Fatalf("hard delete message: %v", execErr)
		}

		var count int
		if scanErr := conn.QueryRow(ctx,
			`SELECT count(*) FROM link_previews WHERE message_id = $1`, doomed.ID,
		).Scan(&count); scanErr != nil {
			t.Fatalf("count cards: %v", scanErr)
		}
		if count != 0 {
			t.Errorf("%d cards survived the message, want 0 — ON DELETE CASCADE", count)
		}
	})
}
