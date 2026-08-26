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

// TestMessageByIDIntegration pins the read the edit and delete handlers make
// before they ask authz anything: the channel is part of the key, so a
// message id from another conversation is indistinguishable from one that
// never existed.
func TestMessageByIDIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("readback"))
	channelID := seedMessagesChannel(ctx, t, conn, "readback")
	other := seedMessagesChannel(ctx, t, conn, "readbackother")

	sent, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID: channelID, AuthorID: author.ID,
		ClientMsgID: uuid.New(), Content: "find me",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	got, err := store.MessageByID(ctx, channelID, sent.ID)
	if err != nil {
		t.Fatalf("MessageByID: %v", err)
	}
	if got.ID != sent.ID || got.Content != "find me" {
		t.Errorf("MessageByID returned %+v, want the stored message", got)
	}
	if got.Author.ID != author.ID || got.Author.Username != author.Username {
		t.Errorf("author = %+v, want %s", got.Author, author.Username)
	}
	if got.EditedAt != nil || got.DeletedAt != nil {
		t.Errorf("a fresh message carries edited_at=%v deleted_at=%v", got.EditedAt, got.DeletedAt)
	}

	if _, err := store.MessageByID(ctx, other, sent.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("reading the message through another channel: %v, want ErrNotFound", err)
	}
	if _, err := store.MessageByID(ctx, channelID, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("reading an unknown message: %v, want ErrNotFound", err)
	}
}

// TestUpdateMessageContentIntegration pins the edit: new content, an
// edited_at the "(edited)" marker renders from, and the message's place in
// history untouched.
func TestUpdateMessageContentIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("editor"))
	channelID := seedMessagesChannel(ctx, t, conn, "editing")
	seedMembership(ctx, t, conn, channelID, author.ID)

	send := func(t *testing.T, content string) storage.Message {
		t.Helper()
		msg, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID,
			ClientMsgID: uuid.New(), Content: content,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return msg
	}

	t.Run("content is replaced and edited_at is stamped", func(t *testing.T) {
		sent := send(t, "frist post")

		edited, err := store.UpdateMessageContent(ctx, channelID, sent.ID, "first post")
		if err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		if edited.Content != "first post" {
			t.Errorf("content = %q, want the edit", edited.Content)
		}
		if edited.EditedAt == nil {
			t.Fatal("edited_at is nil; the client cannot draw the (edited) marker")
		}
		if edited.EditedAt.Before(edited.CreatedAt) {
			t.Errorf("edited_at %v is before created_at %v", edited.EditedAt, edited.CreatedAt)
		}
		if edited.ID != sent.ID || !edited.CreatedAt.Equal(sent.CreatedAt) {
			t.Errorf("the edit moved the message: %s at %v, want %s at %v",
				edited.ID, edited.CreatedAt, sent.ID, sent.CreatedAt)
		}
		if edited.Author.Username != author.Username {
			t.Errorf("author = %+v, want %s", edited.Author, author.Username)
		}
		if edited.DeletedAt != nil {
			t.Errorf("the edit set deleted_at=%v", edited.DeletedAt)
		}
	})

	t.Run("a deleted message cannot be edited", func(t *testing.T) {
		sent := send(t, "about to go")
		if _, err := store.SoftDeleteMessage(ctx, channelID, sent.ID, author.ID); err != nil {
			t.Fatalf("SoftDeleteMessage: %v", err)
		}

		_, err := store.UpdateMessageContent(ctx, channelID, sent.ID, "back from the dead")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("editing a deleted message: %v, want ErrNotFound", err)
		}

		// And the row is still the placeholder it was.
		after, err := store.MessageByID(ctx, channelID, sent.ID)
		if err != nil {
			t.Fatalf("MessageByID: %v", err)
		}
		if after.Content != "" || after.DeletedAt == nil {
			t.Errorf("the refused edit left content=%q deleted_at=%v", after.Content, after.DeletedAt)
		}
	})

	t.Run("another channel's message is never edited", func(t *testing.T) {
		elsewhere := seedMessagesChannel(ctx, t, conn, "editingelsewhere")
		sent := send(t, "not yours to edit")

		if _, err := store.UpdateMessageContent(ctx, elsewhere, sent.ID, "rewritten"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("editing through another channel: %v, want ErrNotFound", err)
		}
		after, err := store.MessageByID(ctx, channelID, sent.ID)
		if err != nil {
			t.Fatalf("MessageByID: %v", err)
		}
		if after.Content != "not yours to edit" || after.EditedAt != nil {
			t.Errorf("the refused edit still changed the row: %+v", after)
		}
	})

	t.Run("mentions follow the edited content", func(t *testing.T) {
		named := mustCreateUser(ctx, t, store, newUser("editnamed"))
		dropped := mustCreateUser(ctx, t, store, newUser("editdropped"))
		stranger := mustCreateUser(ctx, t, store, newUser("editstranger"))
		seedMembership(ctx, t, conn, channelID, named.ID)
		seedMembership(ctx, t, conn, channelID, dropped.ID)

		sent := send(t, "hello "+mentionTokenFor(dropped.ID))
		assertMentionRows(t, mentionedIDs(ctx, t, conn, sent.ID), []uuid.UUID{dropped.ID})

		// The edit names somebody else, drops the first name, and names a
		// stranger to the channel — who must not become a row, exactly as on
		// a send.
		_, err := store.UpdateMessageContent(ctx, channelID, sent.ID,
			"hello "+mentionTokenFor(named.ID)+" and "+mentionTokenFor(stranger.ID))
		if err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		assertMentionRows(t, mentionedIDs(ctx, t, conn, sent.ID), []uuid.UUID{named.ID})

		// An edit that keeps a mention keeps its row rather than churning it.
		if _, err := store.UpdateMessageContent(ctx, channelID, sent.ID,
			"still "+mentionTokenFor(named.ID)); err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		assertMentionRows(t, mentionedIDs(ctx, t, conn, sent.ID), []uuid.UUID{named.ID})

		// And an edit that names nobody leaves none.
		if _, err := store.UpdateMessageContent(ctx, channelID, sent.ID, "quiet now"); err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		assertMentionRows(t, mentionedIDs(ctx, t, conn, sent.ID), nil)
	})
}

// TestSoftDeleteMessageIntegration pins the delete: the row keeps its place
// with its content erased, which is what the dashed placeholder renders, and
// a second delete changes nothing.
func TestSoftDeleteMessageIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("deleter"))
	admin := mustCreateUser(ctx, t, store, newUser("deletemod"))
	channelID := seedMessagesChannel(ctx, t, conn, "deleting")

	send := func(t *testing.T, content string) storage.Message {
		t.Helper()
		msg, _, err := store.CreateMessage(ctx, storage.NewMessage{
			ChannelID: channelID, AuthorID: author.ID,
			ClientMsgID: uuid.New(), Content: content,
		})
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return msg
	}

	t.Run("the content is erased and the row keeps its place", func(t *testing.T) {
		sent := send(t, "regrettable")

		deleted, err := store.SoftDeleteMessage(ctx, channelID, sent.ID, admin.ID)
		if err != nil {
			t.Fatalf("SoftDeleteMessage: %v", err)
		}
		if deleted.Content != "" {
			t.Errorf("deleted message still carries content %q", deleted.Content)
		}
		if deleted.DeletedAt == nil {
			t.Fatal("deleted_at is nil; the client cannot draw the placeholder")
		}
		if deleted.ID != sent.ID || !deleted.CreatedAt.Equal(sent.CreatedAt) {
			t.Errorf("the delete moved the message: %s at %v, want %s at %v",
				deleted.ID, deleted.CreatedAt, sent.ID, sent.CreatedAt)
		}
		if deleted.Author.ID != author.ID {
			t.Errorf("author = %+v, want the message's own author %s", deleted.Author, author.Username)
		}
		if got := deletedByOf(ctx, t, conn, sent.ID); got != admin.ID {
			t.Errorf("deleted_by = %s, want the deleting user %s", got, admin.ID)
		}
	})

	t.Run("deleting twice changes nothing the second time", func(t *testing.T) {
		sent := send(t, "gone once")

		first, err := store.SoftDeleteMessage(ctx, channelID, sent.ID, author.ID)
		if err != nil {
			t.Fatalf("SoftDeleteMessage: %v", err)
		}
		if _, again := store.SoftDeleteMessage(ctx, channelID, sent.ID, admin.ID); !errors.Is(again, storage.ErrNotFound) {
			t.Fatalf("second delete: %v, want ErrNotFound so the handler announces nothing twice", again)
		}

		after, err := store.MessageByID(ctx, channelID, sent.ID)
		if err != nil {
			t.Fatalf("MessageByID: %v", err)
		}
		if !after.DeletedAt.Equal(*first.DeletedAt) {
			t.Errorf("the second delete moved deleted_at from %v to %v", first.DeletedAt, after.DeletedAt)
		}
		if got := deletedByOf(ctx, t, conn, sent.ID); got != author.ID {
			t.Errorf("deleted_by = %s, want the first deleter %s", got, author.ID)
		}
	})

	t.Run("another channel's message is never deleted", func(t *testing.T) {
		elsewhere := seedMessagesChannel(ctx, t, conn, "deletingelsewhere")
		sent := send(t, "not yours to delete")

		if _, err := store.SoftDeleteMessage(ctx, elsewhere, sent.ID, admin.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("deleting through another channel: %v, want ErrNotFound", err)
		}
		after, err := store.MessageByID(ctx, channelID, sent.ID)
		if err != nil {
			t.Fatalf("MessageByID: %v", err)
		}
		if after.DeletedAt != nil {
			t.Errorf("the refused delete still erased the row: %+v", after)
		}
	})
}

// TestDeletedRowsAgreeIntegration is the subtlety this slice owes the one
// before it: TestListMessagesIntegration pinned how a soft-deleted message
// surfaces in history against a hand-seeded row, written before anything
// could set deleted_at. Now that deletion is real, the two must be the same
// row — otherwise the older test pins a shape nothing produces.
func TestDeletedRowsAgreeIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("agreeing"))
	channelID := seedMessagesChannel(ctx, t, conn, "agreeing")
	seedMembership(ctx, t, conn, channelID, author.ID)
	base := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	seedMessageAt(ctx, t, conn, channelID, author.ID, "before", base)
	seededID := seedDeletedMessageAt(ctx, t, conn, channelID, author.ID, base.Add(time.Minute))
	sent, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID: channelID, AuthorID: author.ID,
		ClientMsgID: uuid.New(), Content: "deleted for real",
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if _, delErr := store.SoftDeleteMessage(ctx, channelID, sent.ID, author.ID); delErr != nil {
		t.Fatalf("SoftDeleteMessage: %v", delErr)
	}

	page, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 50})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Messages) != 3 {
		t.Fatalf("page holds %d messages, want both deleted rows in place", len(page.Messages))
	}

	seeded, erased := page.Messages[1], page.Messages[2]
	if seeded.ID != seededID || erased.ID != sent.ID {
		t.Fatalf("page = %s, %s, %s; want the seeded %s then the deleted %s",
			page.Messages[0].ID, seeded.ID, erased.ID, seededID, sent.ID)
	}
	if seeded.Content != erased.Content {
		t.Errorf("seeded content %q, deleted-for-real content %q", seeded.Content, erased.Content)
	}
	if (seeded.DeletedAt == nil) != (erased.DeletedAt == nil) {
		t.Errorf("seeded deleted_at=%v, deleted-for-real deleted_at=%v", seeded.DeletedAt, erased.DeletedAt)
	}
	if erased.Content != "" || erased.DeletedAt == nil {
		t.Errorf("a really-deleted row surfaced as content=%q deleted_at=%v", erased.Content, erased.DeletedAt)
	}

	// The search index is partial on deleted_at, so a deleted message must
	// also stop being findable — the other half of "erased" (migration 0006).
	results, err := store.SearchMessages(ctx, storage.SearchMessagesParams{
		UserID: author.ID, Query: "deleted for real", Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results.Results) != 0 {
		t.Errorf("a deleted message is still searchable: %+v", results.Results)
	}
}

// deletedByOf reads a message's deleted_by straight from SQL: the column is
// deliberately not on storage.Message (nothing renders it before the Phase
// 1.4 audit log), so this is the only way to assert it was recorded.
func deletedByOf(ctx context.Context, t *testing.T, conn *pgx.Conn, messageID uuid.UUID) uuid.UUID {
	t.Helper()

	var id *uuid.UUID
	err := conn.QueryRow(ctx, `SELECT deleted_by FROM messages WHERE id = $1`, messageID).Scan(&id)
	if err != nil {
		t.Fatalf("read deleted_by: %v", err)
	}
	if id == nil {
		t.Fatalf("deleted_by is null on %s; nobody is named for the deletion", messageID)
	}
	return *id
}

// TestListMessagesAroundIntegration pins the permalink page: the anchor
// itself, centred, with the limit split either side of it — and what happens
// when the anchor sits near either end of the channel.
func TestListMessagesAroundIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	conn := messagesRawConn(ctx, t, dsn)

	author := mustCreateUser(ctx, t, store, newUser("permalinker"))
	channelID := seedMessagesChannel(ctx, t, conn, "permalink")
	base := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	const total = 9
	for i := range total {
		seedMessageAt(ctx, t, conn, channelID, author.ID,
			fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Minute))
	}

	// The whole channel, ascending, so a cursor can be taken for any of it.
	all, err := store.ListMessages(ctx, storage.ListMessagesParams{ChannelID: channelID, Limit: 50})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(all.Messages) != total {
		t.Fatalf("fixture holds %d messages, want %d", len(all.Messages), total)
	}
	anchorAt := func(i int) *storage.MessageCursor {
		m := all.Messages[i]
		return &storage.MessageCursor{CreatedAt: m.CreatedAt, ID: m.ID}
	}
	around := func(t *testing.T, anchor *storage.MessageCursor, limit int) storage.MessagePage {
		t.Helper()
		page, pageErr := store.ListMessages(ctx, storage.ListMessagesParams{
			ChannelID: channelID, Around: anchor, Limit: limit,
		})
		if pageErr != nil {
			t.Fatalf("ListMessages around: %v", pageErr)
		}
		assertAscending(t, "around page", page.Messages)
		return page
	}

	t.Run("the page is centred on the anchor", func(t *testing.T) {
		// Limit 5: the anchor takes one place, the odd message goes to the
		// older side, so two older and two newer.
		page := around(t, anchorAt(4), 5)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m2", "m3", "m4", "m5", "m6"}) {
			t.Errorf("around page = %v, want m2..m6", got)
		}
		if !page.HasBefore || !page.HasAfter {
			t.Errorf("before=%v after=%v, want history reported on both sides", page.HasBefore, page.HasAfter)
		}
	})

	t.Run("an even limit gives the extra message to the older side", func(t *testing.T) {
		page := around(t, anchorAt(4), 6)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m1", "m2", "m3", "m4", "m5", "m6"}) {
			t.Errorf("around page = %v, want m1..m6", got)
		}
	})

	t.Run("a limit of one is the anchor alone", func(t *testing.T) {
		page := around(t, anchorAt(4), 1)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m4"}) {
			t.Errorf("around page = %v, want just the anchor", got)
		}
		if !page.HasBefore || !page.HasAfter {
			t.Errorf("before=%v after=%v, want both sides reported", page.HasBefore, page.HasAfter)
		}
	})

	t.Run("at the start of the channel the page is simply shorter", func(t *testing.T) {
		// Nothing is older than m0, and the newer side is NOT widened to
		// make up the shortfall: a short page here means the channel really
		// does start there, which HasBefore reports.
		page := around(t, anchorAt(0), 5)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m0", "m1", "m2"}) {
			t.Errorf("around page = %v, want the anchor and its newer share", got)
		}
		if page.HasBefore {
			t.Error("HasBefore is true at the oldest message in the channel")
		}
		if !page.HasAfter {
			t.Error("HasAfter is false with six newer messages left")
		}
	})

	t.Run("at the live edge the page is shorter the other way", func(t *testing.T) {
		page := around(t, anchorAt(total-1), 5)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m6", "m7", "m8"}) {
			t.Errorf("around page = %v, want the anchor and its older share", got)
		}
		if !page.HasBefore {
			t.Error("HasBefore is false with six older messages left")
		}
		if page.HasAfter {
			t.Error("HasAfter is true at the newest message in the channel")
		}
	})

	t.Run("a whole channel that fits reports nothing either side", func(t *testing.T) {
		page := around(t, anchorAt(4), 50)
		if len(page.Messages) != total {
			t.Errorf("around page holds %d messages, want the whole channel", len(page.Messages))
		}
		if page.HasBefore || page.HasAfter {
			t.Errorf("before=%v after=%v on a page holding everything", page.HasBefore, page.HasAfter)
		}
	})

	t.Run("an anchor naming no message still centres on its position", func(t *testing.T) {
		// A permalink to a message that is gone — or a cursor a client
		// invented — lands between two rows rather than on one.
		between := &storage.MessageCursor{
			CreatedAt: base.Add(4*time.Minute + 30*time.Second),
			ID:        uuid.New(),
		}
		page := around(t, between, 4)
		if got := contentsOf(page.Messages); !slices.Equal(got, []string{"m3", "m4", "m5", "m6"}) {
			t.Errorf("around page = %v, want the messages either side of the gap", got)
		}
	})

	t.Run("an empty channel pages cleanly", func(t *testing.T) {
		empty := seedMessagesChannel(ctx, t, conn, "permalinkempty")
		page, pageErr := store.ListMessages(ctx, storage.ListMessagesParams{
			ChannelID: empty, Around: anchorAt(0), Limit: 50,
		})
		if pageErr != nil {
			t.Fatalf("ListMessages around: %v", pageErr)
		}
		if len(page.Messages) != 0 || page.HasBefore || page.HasAfter {
			t.Errorf("empty channel returned %+v; an empty page has no row to anchor a cursor on", page)
		}
	})

	t.Run("around excludes the other two cursors", func(t *testing.T) {
		for _, params := range []storage.ListMessagesParams{
			{ChannelID: channelID, Around: anchorAt(4), Before: anchorAt(4), Limit: 10},
			{ChannelID: channelID, Around: anchorAt(4), After: anchorAt(4), Limit: 10},
		} {
			if _, err := store.ListMessages(ctx, params); err == nil {
				t.Error("paging around and in a direction at once was accepted")
			}
		}
	})
}
