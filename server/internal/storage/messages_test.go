package storage_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// messagesRawConn opens a direct connection to the scratch database for the
// fixtures storage has no function for (channels) and for the rows storage
// deliberately cannot write: an explicit created_at, and a soft-deleted
// message. It registers citext exactly like the pool's connections, because
// channels.slug is a citext column.
func messagesRawConn(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for fixtures: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := conn.Close(context.Background()); closeErr != nil {
			t.Errorf("close fixture connection: %v", closeErr)
		}
	})
	if err := storage.RegisterCitext(ctx, conn); err != nil {
		t.Fatalf("register citext on fixture connection: %v", err)
	}
	return conn
}

// seedMessagesChannel inserts a public channel and returns its id.
func seedMessagesChannel(ctx context.Context, t *testing.T, conn *pgx.Conn, slug string) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := conn.QueryRow(ctx,
		`INSERT INTO channels (kind, slug) VALUES ('public', $1) RETURNING id`, slug,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed channel %s: %v", slug, err)
	}
	return id
}

// seedMessageAt inserts one message with an explicit created_at — the row
// CreateMessage cannot produce, because the server always stamps now().
func seedMessageAt(
	ctx context.Context, t *testing.T, conn *pgx.Conn,
	channelID, authorID uuid.UUID, content string, createdAt time.Time,
) {
	t.Helper()

	_, err := conn.Exec(ctx,
		`INSERT INTO messages (channel_id, author_id, client_msg_id, content, created_at)
		 VALUES ($1, $2, gen_random_uuid(), $3, $4)`,
		channelID, authorID, content, createdAt,
	)
	if err != nil {
		t.Fatalf("seed message %q: %v", content, err)
	}
}

// seedDeletedMessageAt inserts a soft-deleted message: content erased,
// deleted_at set. It stands in for what slice 1.2b will write, so history
// can be pinned on it today.
func seedDeletedMessageAt(
	ctx context.Context, t *testing.T, conn *pgx.Conn,
	channelID, authorID uuid.UUID, createdAt time.Time,
) uuid.UUID {
	t.Helper()

	var id uuid.UUID
	err := conn.QueryRow(ctx,
		`INSERT INTO messages (channel_id, author_id, client_msg_id, content, created_at, deleted_at, deleted_by)
		 VALUES ($1, $2, gen_random_uuid(), '', $3, $3, $2)
		 RETURNING id`,
		channelID, authorID, createdAt,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed deleted message: %v", err)
	}
	return id
}

// countMessages counts the rows of one channel, straight from SQL, so the
// idempotency tests assert on the table rather than on the API's own answer.
func countMessages(ctx context.Context, t *testing.T, conn *pgx.Conn, channelID uuid.UUID) int {
	t.Helper()

	var n int
	err := conn.QueryRow(ctx, `SELECT count(*) FROM messages WHERE channel_id = $1`, channelID).Scan(&n)
	if err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return n
}

// mentionedIDs reads the message_mentions rows of one message straight from
// SQL, so the assertions below are about the table the sidebar's "@" badge
// counts rather than about anything CreateMessage reports back.
func mentionedIDs(ctx context.Context, t *testing.T, conn *pgx.Conn, messageID uuid.UUID) []uuid.UUID {
	t.Helper()

	rows, err := conn.Query(ctx,
		`SELECT mentioned_user_id FROM message_mentions WHERE message_id = $1`, messageID)
	if err != nil {
		t.Fatalf("read mentions: %v", err)
	}
	defer rows.Close()

	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			t.Fatalf("scan mention: %v", scanErr)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read mentions: %v", err)
	}
	return ids
}

// assertMentionRows checks exactly who a message's mention rows name, in no
// particular order.
func assertMentionRows(t *testing.T, got, want []uuid.UUID) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("message_mentions holds %v, want exactly %v", got, want)
	}
	for _, id := range want {
		if !slices.Contains(got, id) {
			t.Errorf("message_mentions is missing %s; it holds %v", id, got)
		}
	}
}

func TestCreateMessageIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("sender"))
	other := mustCreateUser(ctx, t, store, newUser("othersender"))
	channelID := seedMessagesChannel(ctx, t, conn, "sending")
	otherChannelID := seedMessagesChannel(ctx, t, conn, "elsewhere")

	t.Run("first send creates the message", func(t *testing.T) {
		nm := storage.NewMessage{
			ChannelID:   channelID,
			AuthorID:    author.ID,
			ClientMsgID: uuid.New(),
			Content:     "hello nest",
		}

		msg, created, err := store.CreateMessage(ctx, nm)
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if !created {
			t.Error("first send reported not created")
		}
		if msg.ID == uuid.Nil {
			t.Error("stored message has nil id")
		}
		if msg.ChannelID != channelID || msg.ClientMsgID != nm.ClientMsgID || msg.Content != nm.Content {
			t.Errorf("stored message does not match the request: %+v", msg)
		}
		if msg.Author.ID != author.ID || msg.Author.Username != author.Username ||
			msg.Author.DisplayName != author.DisplayName {
			t.Errorf("author summary = %+v, want %s/%s", msg.Author, author.Username, author.DisplayName)
		}
		if msg.CreatedAt.IsZero() {
			t.Error("created_at not populated")
		}
		if msg.EditedAt != nil || msg.DeletedAt != nil {
			t.Errorf("fresh message carries edited_at=%v deleted_at=%v", msg.EditedAt, msg.DeletedAt)
		}
	})

	t.Run("resend returns the stored message and reports not created", func(t *testing.T) {
		nm := storage.NewMessage{
			ChannelID:   channelID,
			AuthorID:    author.ID,
			ClientMsgID: uuid.New(),
			Content:     "queued while offline",
		}

		first, created, err := store.CreateMessage(ctx, nm)
		if err != nil || !created {
			t.Fatalf("first send: msg=%+v created=%v err=%v", first, created, err)
		}

		// A retry may carry different content — the composer could have been
		// edited before the queue drained. The stored row wins, unmodified.
		resent := nm
		resent.Content = "different text, same key"

		second, created, err := store.CreateMessage(ctx, resent)
		if err != nil {
			t.Fatalf("resend: %v", err)
		}
		if created {
			t.Error("resend reported created; the row already existed")
		}
		if second.ID != first.ID {
			t.Errorf("resend returned id %s, want the existing %s", second.ID, first.ID)
		}
		if second.Content != nm.Content {
			t.Errorf("resend rewrote content to %q, want the stored %q", second.Content, nm.Content)
		}
		if !second.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("resend moved created_at from %v to %v", first.CreatedAt, second.CreatedAt)
		}
	})

	t.Run("the same key from another author is a separate message", func(t *testing.T) {
		shared := uuid.New()
		mine := storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID, ClientMsgID: shared, Content: "mine",
		}
		theirs := storage.NewMessage{
			ChannelID: channelID, AuthorID: other.ID, ClientMsgID: shared, Content: "theirs",
		}

		first, created, err := store.CreateMessage(ctx, mine)
		if err != nil || !created {
			t.Fatalf("author send: created=%v err=%v", created, err)
		}
		second, created, err := store.CreateMessage(ctx, theirs)
		if err != nil {
			t.Fatalf("other author send: %v", err)
		}
		if !created {
			t.Error("another author's identical key was swallowed as a resend")
		}
		if second.ID == first.ID {
			t.Error("two authors collided on one message row")
		}
		if second.Content != "theirs" || second.Author.ID != other.ID {
			t.Errorf("second message = %+v, want other author's own row", second)
		}
	})

	t.Run("the same key in another channel is a separate message", func(t *testing.T) {
		shared := uuid.New()
		here := storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID, ClientMsgID: shared, Content: "here",
		}
		there := storage.NewMessage{
			ChannelID: otherChannelID, AuthorID: author.ID, ClientMsgID: shared, Content: "there",
		}

		first, _, err := store.CreateMessage(ctx, here)
		if err != nil {
			t.Fatalf("send here: %v", err)
		}
		second, created, err := store.CreateMessage(ctx, there)
		if err != nil {
			t.Fatalf("send there: %v", err)
		}
		if !created || second.ID == first.ID {
			t.Errorf("the key leaked across channels: created=%v ids %s/%s", created, first.ID, second.ID)
		}
	})

	t.Run("unknown channel or author is ErrNotFound", func(t *testing.T) {
		_, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: uuid.New(), AuthorID: author.ID, ClientMsgID: uuid.New(), Content: "nowhere",
		})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("unknown channel: got %v, want ErrNotFound", err)
		}

		_, _, err = store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: channelID, AuthorID: uuid.New(), ClientMsgID: uuid.New(), Content: "nobody",
		})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("unknown author: got %v, want ErrNotFound", err)
		}
	})

	t.Run("a resend of a deleted message does not resurrect it", func(t *testing.T) {
		nm := storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID, ClientMsgID: uuid.New(), Content: "to be removed",
		}
		stored, _, err := store.CreateMessage(ctx, nm)
		if err != nil {
			t.Fatalf("first send: %v", err)
		}
		if _, execErr := conn.Exec(ctx,
			`UPDATE messages SET content = '', deleted_at = now(), deleted_by = author_id WHERE id = $1`,
			stored.ID,
		); execErr != nil {
			t.Fatalf("soft delete: %v", execErr)
		}

		again, created, err := store.CreateMessage(ctx, nm)
		if err != nil {
			t.Fatalf("resend after delete: %v", err)
		}
		if created || again.ID != stored.ID {
			t.Errorf("resend created=%v id=%s, want the existing deleted row %s", created, again.ID, stored.ID)
		}
		if again.DeletedAt == nil || again.Content != "" {
			t.Errorf("resend returned a live-looking row: %+v", again)
		}
	})
}

// TestCreateMessageConcurrentIntegration proves the idempotency key holds
// under the race it exists for: a client whose retry overlaps its own first
// attempt. Every caller must get one and the same message, exactly one of
// them may claim to have created it, and the table must hold one row.
func TestCreateMessageConcurrentIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("racer"))
	channelID := seedMessagesChannel(ctx, t, conn, "contended")

	// One round is one message sent by eight concurrent senders; a handful of
	// rounds keeps the window wide without making the test slow.
	const (
		rounds  = 5
		senders = 8
	)
	for round := range rounds {
		nm := storage.NewMessage{
			ChannelID:   channelID,
			AuthorID:    author.ID,
			ClientMsgID: uuid.New(),
			Content:     fmt.Sprintf("resent round %d", round),
		}

		msgs := make([]storage.Message, senders)
		createdFlags := make([]bool, senders)
		errs := make([]error, senders)
		start := make(chan struct{})

		var wg sync.WaitGroup
		wg.Add(senders)
		for i := range senders {
			go func() {
				defer wg.Done()
				<-start
				msgs[i], createdFlags[i], errs[i] = store.CreateMessage(ctx, nm)
			}()
		}
		close(start)
		wg.Wait()

		creations := 0
		for i := range senders {
			if errs[i] != nil {
				t.Fatalf("round %d sender %d: %v", round, i, errs[i])
			}
			if createdFlags[i] {
				creations++
			}
			if msgs[i].ID != msgs[0].ID {
				t.Errorf("round %d: sender %d got id %s, sender 0 got %s",
					round, i, msgs[i].ID, msgs[0].ID)
			}
			if msgs[i].ClientMsgID != nm.ClientMsgID || msgs[i].Content != nm.Content {
				t.Errorf("round %d sender %d returned a foreign row: %+v", round, i, msgs[i])
			}
		}
		if creations != 1 {
			t.Errorf("round %d: %d senders reported creating the message, want exactly 1", round, creations)
		}
	}

	if got := countMessages(ctx, t, conn, channelID); got != rounds {
		t.Errorf("channel holds %d messages after %d contended sends, want %d", got, rounds*senders, rounds)
	}
}

func TestListMessagesIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("historian"))

	t.Run("an empty channel pages cleanly", func(t *testing.T) {
		empty := seedMessagesChannel(ctx, t, conn, "empty")

		page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: empty, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if len(page.Messages) != 0 || page.HasBefore || page.HasAfter {
			t.Errorf("empty channel returned %+v", page)
		}

		// And so does an unknown channel: history is a read, not a lookup.
		page, err = store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: uuid.New(), Limit: 50})
		if err != nil {
			t.Fatalf("ListMessages of unknown channel: %v", err)
		}
		if len(page.Messages) != 0 {
			t.Errorf("unknown channel returned %d messages", len(page.Messages))
		}
	})

	t.Run("the newest page is the tail of the channel, ascending", func(t *testing.T) {
		channelID := seedMessagesChannel(ctx, t, conn, "tail")
		base := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
		for i := range 5 {
			seedMessageAt(ctx, t, conn, channelID, author.ID,
				fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Minute))
		}

		page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 3})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m2", "m3", "m4"}) {
			t.Errorf("newest page = %v, want the last three ascending", got)
		}
		if !page.HasBefore {
			t.Error("HasBefore is false with two older messages left")
		}
		if page.HasAfter {
			t.Error("HasAfter is true at the live edge")
		}

		// The whole channel in one page has nothing on either side.
		page, err = store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if len(page.Messages) != 5 || page.HasBefore || page.HasAfter {
			t.Errorf("full page = %d messages, before=%v after=%v", len(page.Messages), page.HasBefore, page.HasAfter)
		}
	})

	t.Run("another channel's messages never leak in", func(t *testing.T) {
		mine := seedMessagesChannel(ctx, t, conn, "mine")
		theirs := seedMessagesChannel(ctx, t, conn, "theirs")
		base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
		seedMessageAt(ctx, t, conn, mine, author.ID, "ours", base)
		seedMessageAt(ctx, t, conn, theirs, author.ID, "not ours", base.Add(time.Second))

		page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: mine, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"ours"}) {
			t.Errorf("channel page = %v, want only its own message", got)
		}
	})

	t.Run("a soft-deleted message keeps its place", func(t *testing.T) {
		channelID := seedMessagesChannel(ctx, t, conn, "removed")
		base := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
		seedMessageAt(ctx, t, conn, channelID, author.ID, "before", base)
		deletedID := seedDeletedMessageAt(ctx, t, conn, channelID, author.ID, base.Add(time.Minute))
		seedMessageAt(ctx, t, conn, channelID, author.ID, "after", base.Add(2*time.Minute))

		page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 50})
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if len(page.Messages) != 3 {
			t.Fatalf("page holds %d messages, want the deleted one to keep its place", len(page.Messages))
		}
		placeholder := page.Messages[1]
		if placeholder.ID != deletedID {
			t.Errorf("middle row is %s, want the deleted %s", placeholder.ID, deletedID)
		}
		if placeholder.DeletedAt == nil {
			t.Error("deleted message came back with a nil deleted_at; the client cannot draw the placeholder")
		}
		if placeholder.Content != "" {
			t.Errorf("deleted message still carries content %q", placeholder.Content)
		}
	})

	t.Run("before and after together is an error", func(t *testing.T) {
		channelID := seedMessagesChannel(ctx, t, conn, "bothcursors")
		cursor := &storage.MessageCursor{CreatedAt: time.Now(), ID: uuid.New()}

		_, err := store.ListMessages(ctx, storage.ListMessagesParams{
			ChannelID: channelID, Before: cursor, After: cursor, Limit: 10,
		})
		if err == nil {
			t.Error("paging in two directions at once was accepted")
		}
	})
}

// TestListMessagesPagingIntegration walks one channel in both directions
// with a page size that puts cursors inside a run of messages sharing a
// single created_at. That run is what the (created_at, id) tie-break exists
// for: a created_at-only cursor either skips the rest of the run or repeats
// it forever.
func TestListMessagesPagingIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("walker"))
	channelID := seedMessagesChannel(ctx, t, conn, "walking")

	shared := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	const tied = 5
	seedMessageAt(ctx, t, conn, channelID, author.ID, "oldest", shared.Add(-2*time.Minute))
	seedMessageAt(ctx, t, conn, channelID, author.ID, "older", shared.Add(-time.Minute))
	for i := range tied {
		seedMessageAt(ctx, t, conn, channelID, author.ID, fmt.Sprintf("tied%d", i), shared)
	}
	seedDeletedMessageAt(ctx, t, conn, channelID, author.ID, shared.Add(time.Minute))
	seedMessageAt(ctx, t, conn, channelID, author.ID, "newest", shared.Add(2*time.Minute))
	const total = tied + 4

	// Page size 2 across a run of 5 forces every cursor shape: one landing
	// before the run, two landing inside it, one landing after.
	const limit = 2

	backwards := walkBackwards(ctx, t, store, channelID, limit, total)
	forwards := walkForwards(ctx, t, store, channelID, limit, total)

	for _, walk := range []struct {
		name     string
		messages []storage.Message
	}{
		{"backwards", backwards},
		{"forwards", forwards},
	} {
		if len(walk.messages) != total {
			t.Errorf("%s walk returned %d messages, want %d", walk.name, len(walk.messages), total)
		}
		seen := map[uuid.UUID]int{}
		for _, m := range walk.messages {
			seen[m.ID]++
		}
		if len(seen) != total {
			t.Errorf("%s walk saw %d distinct messages, want %d", walk.name, len(seen), total)
		}
		for id, count := range seen {
			if count != 1 {
				t.Errorf("%s walk returned message %s %d times, want once", walk.name, id, count)
			}
		}
		assertAscending(t, walk.name, walk.messages)
	}

	// Both directions describe the same history in the same order.
	for i := range min(len(backwards), len(forwards)) {
		if backwards[i].ID != forwards[i].ID {
			t.Fatalf("walks diverge at %d: backwards %s, forwards %s",
				i, backwards[i].ID, forwards[i].ID)
		}
	}
}

// walkBackwards pages from the live edge into the past with `before`,
// stitching the pages back into one ascending history.
func walkBackwards(
	ctx context.Context, t *testing.T, store *storage.Store,
	channelID uuid.UUID, limit, total int,
) []storage.Message {
	t.Helper()

	var history []storage.Message
	params := storage.ListMessagesParams{ChannelID: channelID, Limit: limit}
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("backwards paging never terminates")
		}
		page, err := store.ListMessages(ctx, params)
		if err != nil {
			t.Fatalf("backwards page %d: %v", pages, err)
		}
		if len(page.Messages) == 0 {
			break
		}
		oldest := page.Messages[0]
		history = append(page.Messages, history...)
		if !page.HasBefore {
			break
		}
		params.Before = &storage.MessageCursor{CreatedAt: oldest.CreatedAt, ID: oldest.ID}
	}
	return history
}

// walkForwards pages from the beginning of history with `after`, the
// reconnect backfill the WS protocol falls back to.
func walkForwards(
	ctx context.Context, t *testing.T, store *storage.Store,
	channelID uuid.UUID, limit, total int,
) []storage.Message {
	t.Helper()

	// The backfill starts from a cursor older than anything in the channel.
	params := storage.ListMessagesParams{
		ChannelID: channelID,
		After:     &storage.MessageCursor{CreatedAt: time.Unix(0, 0).UTC(), ID: uuid.Nil},
		Limit:     limit,
	}
	var history []storage.Message
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("forwards paging never terminates")
		}
		page, err := store.ListMessages(ctx, params)
		if err != nil {
			t.Fatalf("forwards page %d: %v", pages, err)
		}
		if len(page.Messages) == 0 {
			break
		}
		if pages > 0 && !page.HasBefore {
			t.Errorf("forwards page %d claims nothing older exists", pages)
		}
		history = append(history, page.Messages...)
		if !page.HasAfter {
			break
		}
		newest := page.Messages[len(page.Messages)-1]
		params.After = &storage.MessageCursor{CreatedAt: newest.CreatedAt, ID: newest.ID}
	}
	return history
}

// assertAscending checks the (created_at, id) order the whole page contract
// rests on. PostgreSQL orders uuid bytewise, which is the order of their
// lowercase hex strings.
func assertAscending(t *testing.T, name string, messages []storage.Message) {
	t.Helper()

	for i := 1; i < len(messages); i++ {
		prev, cur := messages[i-1], messages[i]
		if cur.CreatedAt.Before(prev.CreatedAt) {
			t.Errorf("%s: %v comes after %v", name, prev.CreatedAt, cur.CreatedAt)
		}
		if cur.CreatedAt.Equal(prev.CreatedAt) && cur.ID.String() <= prev.ID.String() {
			t.Errorf("%s: tie at %v not broken by id (%s then %s)", name, cur.CreatedAt, prev.ID, cur.ID)
		}
	}
}

func contentsOf(messages []storage.Message) []string {
	contents := make([]string, len(messages))
	for i, m := range messages {
		contents[i] = m.Content
	}
	return contents
}

// seedMembership puts a user in a channel. Mentions are member-scoped, so
// tests about them have to say who is actually in the conversation.
func seedMembership(ctx context.Context, t *testing.T, conn *pgx.Conn, channelID, userID uuid.UUID) {
	t.Helper()

	_, err := conn.Exec(ctx,
		`INSERT INTO channel_members (channel_id, user_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, channelID, userID)
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// mentionToken is the contract's literal wire form: <@{user_id}>.
func mentionTokenFor(id uuid.UUID) string { return "<@" + id.String() + ">" }

// TestCreateMessageMentionsIntegration asserts the rows the sidebar's filled
// "@" badge counts, read straight from message_mentions rather than from
// whatever CreateMessage reports back.
//
// The member-only rule is the one worth pinning: a mention row names somebody
// entitled to read the message it points at, so a future "my mentions" view
// cannot surface a conversation its reader was never in.
func TestCreateMessageMentionsIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("mentionauthor"))
	member := mustCreateUser(ctx, t, store, newUser("mentionmember"))
	stranger := mustCreateUser(ctx, t, store, newUser("mentionstranger"))
	channelID := seedMessagesChannel(ctx, t, conn, "mentioning")
	seedMembership(ctx, t, conn, channelID, author.ID)
	seedMembership(ctx, t, conn, channelID, member.ID)

	send := func(t *testing.T, clientMsgID uuid.UUID, content string) storage.Message {
		t.Helper()
		msg, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID,
			ClientMsgID: clientMsgID, Content: content,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return msg
	}

	t.Run("a mentioned member gets a row", func(t *testing.T) {
		msg := send(t, uuid.New(), "morning "+mentionTokenFor(member.ID)+", ready?")
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("naming the same person twice writes one row", func(t *testing.T) {
		token := mentionTokenFor(member.ID)
		msg := send(t, uuid.New(), token+" and again "+token)
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("a non-member is dropped, and does not fail the message", func(t *testing.T) {
		// A stale paste or a hand-typed id. The message must still land.
		msg := send(t, uuid.New(), "cc "+mentionTokenFor(stranger.ID)+" "+mentionTokenFor(member.ID))
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), []uuid.UUID{member.ID})
	})

	t.Run("a token naming nobody is dropped rather than erroring", func(t *testing.T) {
		// No foreign-key violation: the join simply matches nothing.
		msg := send(t, uuid.New(), "who is "+mentionTokenFor(uuid.New())+"?")
		assertMentionRows(t, mentionedIDs(ctx, t, conn, msg.ID), nil)
	})

	t.Run("a resend writes no second set of rows", func(t *testing.T) {
		clientMsgID := uuid.New()
		content := "please look " + mentionTokenFor(member.ID)
		first := send(t, clientMsgID, content)
		again := send(t, clientMsgID, content)

		if again.ID != first.ID {
			t.Fatalf("resend returned message %s, want the existing %s", again.ID, first.ID)
		}
		// The primary key would refuse a duplicate outright, so the real risk
		// is the resend erroring rather than being a lookup. It did not.
		assertMentionRows(t, mentionedIDs(ctx, t, conn, first.ID), []uuid.UUID{member.ID})
	})

	t.Run("the counts the sidebar reads follow the rows", func(t *testing.T) {
		// A fresh channel, so the counts below are only about this subtest.
		countedID := seedMessagesChannel(ctx, t, conn, "mentioncounts")
		seedMembership(ctx, t, conn, countedID, author.ID)
		seedMembership(ctx, t, conn, countedID, member.ID)

		for _, content := range []string{
			"plain unread, nobody named",
			"and now " + mentionTokenFor(member.ID),
		} {
			if _, _, err := store.CreateMessage(ctx, storage.NewMessage{
				ChannelID: countedID, AuthorID: author.ID,
				ClientMsgID: uuid.New(), Content: content,
			}); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
		}

		seen, err := store.ChannelForUser(ctx, countedID, member.ID)
		if err != nil {
			t.Fatalf("ChannelForUser: %v", err)
		}
		if seen.UnreadCount != 2 {
			t.Errorf("unread_count = %d, want 2", seen.UnreadCount)
		}
		if seen.MentionCount != 1 {
			t.Errorf("mention_count = %d, want 1", seen.MentionCount)
		}
		if seen.MentionCount > seen.UnreadCount {
			t.Errorf("mention_count %d exceeds unread_count %d; the badge would outrank its own total",
				seen.MentionCount, seen.UnreadCount)
		}

		// The author's own message is not unread to them, so neither count
		// moves for the person who wrote it.
		mine, err := store.ChannelForUser(ctx, countedID, author.ID)
		if err != nil {
			t.Fatalf("ChannelForUser: %v", err)
		}
		if mine.UnreadCount != 0 || mine.MentionCount != 0 {
			t.Errorf("author sees unread=%d mention=%d, want 0/0", mine.UnreadCount, mine.MentionCount)
		}
	})
}
