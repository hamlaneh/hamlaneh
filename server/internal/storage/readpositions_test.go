package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// seedEpoch anchors the timestamps these tests seed. Read positions and
// unread counts are entirely questions of order, so every fixture message
// gets the place the test chose for it rather than whatever now() happened
// to be when it was inserted.
var seedEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// seedAt returns the fixture timestamp n minutes into the seeded history.
func seedAt(n int) time.Time {
	return seedEpoch.Add(time.Duration(n) * time.Minute)
}

// seedMessageAtTime inserts one message with an explicit created_at and
// returns its id — the row CreateMessage cannot produce, because the server
// always stamps now(). It uses the raw fixture connection from
// messages_test.go, which is this package's one connection for rows storage
// deliberately cannot write.
func seedMessageAtTime(
	ctx context.Context, t *testing.T, conn *pgx.Conn,
	channelID, authorID uuid.UUID, content string, createdAt time.Time,
) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := conn.QueryRow(ctx,
		`INSERT INTO messages (channel_id, author_id, client_msg_id, content, created_at)
		 VALUES ($1, $2, gen_random_uuid(), $3, $4)
		 RETURNING id`,
		channelID, authorID, content, createdAt,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed message %q: %v", content, err)
	}
	return id
}

// seedMention records that a message mentions a user. Nothing writes
// message_mentions until the mention parser lands in slice 1.2b, so the
// sidebar's "@" badge can only be pinned on rows seeded by hand.
func seedMention(ctx context.Context, t *testing.T, conn *pgx.Conn, messageID, userID uuid.UUID) {
	t.Helper()

	_, err := conn.Exec(ctx,
		`INSERT INTO message_mentions (message_id, mentioned_user_id) VALUES ($1, $2)`,
		messageID, userID,
	)
	if err != nil {
		t.Fatalf("seed mention of %s in %s: %v", userID, messageID, err)
	}
}

// readPosition is a channel_read_positions row as raw SQL sees it. The
// assertions about what was stored must not go through the code that stored
// it, or a write that silently does nothing still passes.
type readPosition struct {
	MessageID uuid.UUID
	At        time.Time
	UpdatedAt time.Time
}

// storedReadPosition reads one (channel, user) row, reporting whether there
// is one at all.
func storedReadPosition(
	ctx context.Context, t *testing.T, conn *pgx.Conn, channelID, userID uuid.UUID,
) (readPosition, bool) {
	t.Helper()

	var pos readPosition
	err := conn.QueryRow(ctx,
		`SELECT last_read_message_id, last_read_at, updated_at
		 FROM channel_read_positions WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	).Scan(&pos.MessageID, &pos.At, &pos.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return readPosition{}, false
	}
	if err != nil {
		t.Fatalf("read stored position: %v", err)
	}
	return pos, true
}

func mustSetReadPosition(
	ctx context.Context, t *testing.T, store testdb.Store, channelID, userID, messageID uuid.UUID,
) {
	t.Helper()

	if err := store.SetReadPosition(ctx, channelID, userID, messageID); err != nil {
		t.Fatalf("SetReadPosition(%s, %s, %s): %v", channelID, userID, messageID, err)
	}
}

// TestSetReadPositionIntegration pins the write side of the read position:
// where it lands, and — the whole reason the column exists — that it only
// ever moves forward.
//
// Every subtest gets its own reader, because a read position is per (channel,
// user); the channels and their messages are shared read-only fixtures.
func TestSetReadPositionIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("rpauthor"))
	channel := mustCreateChannel(ctx, t, store, newChannel("rpchannel", storage.ChannelKindPublic, author.ID))
	elsewhere := mustCreateChannel(ctx, t, store, newChannel("rpelsewhere", storage.ChannelKindPublic, author.ID))

	older := seedMessageAtTime(ctx, t, conn, channel.ID, author.ID, "older", seedAt(1))
	newer := seedMessageAtTime(ctx, t, conn, channel.ID, author.ID, "newer", seedAt(2))
	foreign := seedMessageAtTime(ctx, t, conn, elsewhere.ID, author.ID, "not here", seedAt(1))

	t.Run("stores the position the caller names", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpstores"))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, older)

		pos, ok := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if !ok {
			t.Fatal("no read position was stored")
		}
		if pos.MessageID != older {
			t.Errorf("last_read_message_id = %s, want %s", pos.MessageID, older)
		}
		// last_read_at is the read message's own created_at, copied so unread
		// counting never has to join back to resolve the anchor.
		if !pos.At.Equal(seedAt(1)) {
			t.Errorf("last_read_at = %s, want the message's created_at %s", pos.At, seedAt(1))
		}
	})

	t.Run("a newer position moves it forward", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpforward"))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, older)
		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, newer)

		pos, ok := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if !ok {
			t.Fatal("no read position was stored")
		}
		if pos.MessageID != newer {
			t.Errorf("last_read_message_id = %s, want it moved to %s", pos.MessageID, newer)
		}
		if !pos.At.Equal(seedAt(2)) {
			t.Errorf("last_read_at = %s, want %s", pos.At, seedAt(2))
		}
	})

	// The regression case is the reason the position is monotonic at all: a
	// background tab that has been open since this morning replays the
	// position it remembers, and must not mark the channel unread again.
	t.Run("an older position is accepted and ignored", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpstale"))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, newer)
		before, ok := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if !ok {
			t.Fatal("no read position was stored")
		}

		if err := store.SetReadPosition(ctx, channel.ID, reader.ID, older); err != nil {
			t.Fatalf("a stale position must not be an error: %v", err)
		}

		after, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if after != before {
			t.Errorf("the stored position changed: %+v -> %+v", before, after)
		}
	})

	t.Run("re-setting the same position changes nothing", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpsame"))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, newer)
		before, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, newer)

		after, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if after != before {
			t.Errorf("the stored position changed: %+v -> %+v", before, after)
		}
	})

	// A soft-deleted message keeps its place in history and renders as a
	// placeholder, so it is legitimately the newest thing a client has seen.
	t.Run("a deleted message can anchor a position", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpdeleted"))
		deleted := seedDeletedMessageAt(ctx, t, conn, channel.ID, author.ID, seedAt(3))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, deleted)

		pos, ok := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if !ok {
			t.Fatal("no read position was stored")
		}
		if pos.MessageID != deleted {
			t.Errorf("last_read_message_id = %s, want %s", pos.MessageID, deleted)
		}
	})

	// The 404 is the same whether the message is somebody else's or does not
	// exist, so the endpoint cannot be used to probe for messages elsewhere.
	t.Run("a message outside the channel is ErrNotFound and stores nothing", func(t *testing.T) {
		cases := []struct {
			name      string
			reader    string
			messageID uuid.UUID
		}{
			{"a message of another channel", "rpforeign", foreign},
			{"a message that does not exist", "rpmissing", uuid.New()},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				reader := mustCreateUser(ctx, t, store, newUser(tc.reader))

				err := store.SetReadPosition(ctx, channel.ID, reader.ID, tc.messageID)
				if !errors.Is(err, storage.ErrNotFound) {
					t.Errorf("got %v, want ErrNotFound", err)
				}
				if _, ok := storedReadPosition(ctx, t, conn, channel.ID, reader.ID); ok {
					t.Error("a rejected position was stored anyway")
				}
			})
		}
	})

	t.Run("a stale position on a message outside the channel does not move a stored one", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpkeeps"))
		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, older)
		before, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)

		if err := store.SetReadPosition(ctx, channel.ID, reader.ID, foreign); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}

		after, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if after != before {
			t.Errorf("a rejected position disturbed the stored one: %+v -> %+v", before, after)
		}
	})

	t.Run("an unknown user is ErrNotFound", func(t *testing.T) {
		err := store.SetReadPosition(ctx, channel.ID, uuid.New(), older)
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	// Own-device sync only: one person's position is their own row and is
	// never written, moved or read on behalf of anybody else.
	t.Run("one user's position leaves another's alone", func(t *testing.T) {
		mine := mustCreateUser(ctx, t, store, newUser("rpmine"))
		theirs := mustCreateUser(ctx, t, store, newUser("rptheirs"))

		mustSetReadPosition(ctx, t, store, channel.ID, theirs.ID, older)
		mustSetReadPosition(ctx, t, store, channel.ID, mine.ID, newer)

		theirPos, ok := storedReadPosition(ctx, t, conn, channel.ID, theirs.ID)
		if !ok {
			t.Fatal("the other user's position vanished")
		}
		if theirPos.MessageID != older {
			t.Errorf("the other user's position moved to %s, want %s", theirPos.MessageID, older)
		}
	})

	// The position is per channel: reading one conversation says nothing
	// about any other.
	t.Run("a position in one channel leaves another channel's alone", func(t *testing.T) {
		reader := mustCreateUser(ctx, t, store, newUser("rpperchan"))

		mustSetReadPosition(ctx, t, store, channel.ID, reader.ID, newer)
		mustSetReadPosition(ctx, t, store, elsewhere.ID, reader.ID, foreign)

		here, _ := storedReadPosition(ctx, t, conn, channel.ID, reader.ID)
		if here.MessageID != newer {
			t.Errorf("this channel's position = %s, want %s", here.MessageID, newer)
		}
		there, ok := storedReadPosition(ctx, t, conn, elsewhere.ID, reader.ID)
		if !ok {
			t.Fatal("the other channel has no position")
		}
		if there.MessageID != foreign {
			t.Errorf("the other channel's position = %s, want %s", there.MessageID, foreign)
		}
	})
}
