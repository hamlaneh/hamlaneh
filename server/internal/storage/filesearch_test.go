package storage_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The filename fixtures use the same Persian constants the message search
// tests declare (search_test.go), for the same reason: several of these
// characters are visually identical to a different code point and one of
// them is invisible.

// seedAttachment inserts one attachment row directly. Uploads are the
// parallel slice's pipeline; these tests are about the search over the rows
// it produces, so they write the rows themselves. messageID nil is an
// orphan — a file uploaded and never sent.
func seedAttachment(
	ctx context.Context, t *testing.T, raw *testdb.Raw,
	channelID, uploaderID uuid.UUID, messageID *uuid.UUID, filename string,
) {
	t.Helper()

	// A nil *uuid.UUID is not a NULL either driver recognises, so the
	// optional message is unwrapped to an untyped nil here.
	var message any
	if messageID != nil {
		message = *messageID
	}
	raw.Exec(ctx, t,
		`INSERT INTO attachments
		     (id, channel_id, uploader_id, message_id, filename, content_type, size_bytes, created_at)
		 VALUES (?, ?, ?, ?, ?, 'application/pdf', 1024, ?)`,
		uuid.New(), channelID, uploaderID, message, filename, time.Now().UTC())
}

// mustSearchFiles runs one page of filename search for a caller.
func mustSearchFiles(
	ctx context.Context, t *testing.T, store testdb.Store,
	userID uuid.UUID, query string, limit int, after *storage.MessageCursor,
) storage.FileSearchPage {
	t.Helper()

	page, err := store.SearchFiles(ctx, storage.SearchFilesParams{
		UserID: userID, Query: query, After: after, Limit: limit,
	})
	if err != nil {
		t.Fatalf("SearchFiles(%q): %v", query, err)
	}
	return page
}

// fileNames is every result of a page as its attachment's filename.
func fileNames(page storage.FileSearchPage) []string {
	names := make([]string, 0, len(page.Results))
	for _, res := range page.Results {
		names = append(names, res.Attachment.Filename)
	}
	return names
}

// TestFileSearchScopeIntegration is the leak test for the files tab, and the
// reason SearchFiles is one membership-joined statement rather than a read
// plus a filter.
//
// All three files carry a token the needle matches, so matching excludes
// none of them: the only thing that can keep the two foreign ones out of the
// page, out of the total and out of a snippet is the join against
// channel_members inside the statement.
func TestFileSearchScopeIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	mallory := mustCreateUser(ctx, t, store, newUser("mallory"))
	carol := mustCreateUser(ctx, t, store, newUser("carol"))

	// Alice's own channel, and two conversations she is in no part of.
	mine := mustCreateChannel(ctx, t, store, newChannel("mine", storage.ChannelKindPrivate, alice.ID))
	theirs := mustCreateChannel(ctx, t, store, newChannel("theirs", storage.ChannelKindPrivate, mallory.ID))
	dm := mustOpenDM(ctx, t, store, mallory.ID, carol.ID)

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	mineMsg := searchSeedMessage(ctx, t, conn, mine.ID, alice.ID, "here it is", at)
	theirsMsg := searchSeedMessage(ctx, t, conn, theirs.ID, mallory.ID, "hers", at)
	dmMsg := searchSeedMessage(ctx, t, conn, dm.ID, mallory.ID, "theirs", at)

	seedAttachment(ctx, t, conn, mine.ID, alice.ID, &mineMsg, "zqxjkv-mine.pdf")
	seedAttachment(ctx, t, conn, theirs.ID, mallory.ID, &theirsMsg, "zqxjkv-channel.pdf")
	seedAttachment(ctx, t, conn, dm.ID, mallory.ID, &dmMsg, "zqxjkv-dm.pdf")

	page := mustSearchFiles(ctx, t, store, alice.ID, "zqxjkv", 50, nil)

	if got := fileNames(page); !slices.Equal(got, []string{"zqxjkv-mine.pdf"}) {
		t.Errorf("results = %q, want only the file in alice's own channel", got)
	}
	if page.Total != 1 || page.Capped {
		t.Errorf("total = %d (capped %v), want 1 and not capped — the count is scoped too",
			page.Total, page.Capped)
	}
	// Belt and braces: nothing from either foreign conversation may appear
	// anywhere in what came back — filename, snippet, channel label or
	// author. Marshalled rather than formatted, so what sits behind every
	// pointer is part of what is searched for a leak.
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page for the leak check: %v", err)
	}
	rendered := string(encoded)
	for _, leak := range []string{"zqxjkv-channel", "zqxjkv-dm", "theirs", "mallory", "carol"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("file search page leaks %q: %s", leak, rendered)
		}
	}

	// The other side of the same fact: Mallory, who IS in both, sees two.
	if got := mustSearchFiles(ctx, t, store, mallory.ID, "zqxjkv", 50, nil).Total; got != 2 {
		t.Errorf("mallory's total = %d, want 2 — scoping must exclude, not just hide", got)
	}
}

// TestFileSearchMatchingIntegration pins that the filename half folds
// exactly as the message half does, and that a snippet over a filename obeys
// the same contract: concatenating its parts reproduces the name.
func TestFileSearchMatchingIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("files", storage.ChannelKindPrivate, alice.ID))

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	msg := searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "have some files", at)

	// The Persian name is spelled the way Persian actually is: with a
	// zero-width non-joiner inside the first word.
	faName := faNewBooks + ".pdf"
	for _, filename := range []string{"Quarterly-REPORT.pdf", faName} {
		seedAttachment(ctx, t, conn, ch.ID, alice.ID, &msg, filename)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"finds english inside a word", "report", []string{"Quarterly-REPORT.pdf"}},
		{"is case-insensitive both ways", "QUARTERLY", []string{"Quarterly-REPORT.pdf"}},
		{"matches the extension", ".pdf", []string{"Quarterly-REPORT.pdf", faName}},
		// The invisible character: the name carries a ZWNJ, the query does
		// not, and the fold must make them one needle.
		{"finds a persian name typed without the zero-width non-joiner", faBooksNoZWNJ, []string{faName}},
		// The other keyboard: Arabic kaf against a Persian keheh.
		{"folds the arabic keyboard's kaf", faBookArabicKaf, []string{faName}},
		// The limitation, pinned as a test rather than discovered by a user.
		{"does NOT stem", "reporting", nil},
		// The underscore is a character, not a LIKE wildcard.
		{"treats like syntax as text", "Quarterly_REPORT", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := fileNames(mustSearchFiles(ctx, t, store, alice.ID, tc.query, 50, nil))
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("SearchFiles(%q) = %q, want %q", tc.query, got, want)
			}
		})
	}

	t.Run("the snippet reconstructs the filename and flags the match", func(t *testing.T) {
		t.Parallel()

		page := mustSearchFiles(ctx, t, store, alice.ID, faBooksNoZWNJ, 50, nil)
		if len(page.Results) != 1 {
			t.Fatalf("got %d results, want the one persian filename", len(page.Results))
		}
		parts := page.Results[0].Snippet
		if got := snippetText(parts); got != faName {
			t.Errorf("snippet parts concatenate to %q, want the filename %q", got, faName)
		}
		// The matched run spans the invisible character: the needle has no
		// ZWNJ, the name does, and the highlight covers the name's own
		// characters — which is the whole point of folding with spans.
		want := []string{faBook + zwnj + faPluralSuffix}
		if got := matchedRuns(parts); !slices.Equal(got, want) {
			t.Errorf("matched runs = %q, want %q", got, want)
		}
	})
}

// TestFileSearchExcludesUnreachableFilesIntegration covers the two rows that
// exist in the table but are not files anybody can see: an orphan that was
// never sent, and one whose message has been deleted. Both carry the same
// needle as the visible file, so only the statement's shape can exclude them.
func TestFileSearchExcludesUnreachableFilesIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("files", storage.ChannelKindPrivate, alice.ID))

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	live := searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "sent", at)
	doomed := searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "about to go", at)

	seedAttachment(ctx, t, conn, ch.ID, alice.ID, &live, "wqfbzt-sent.pdf")
	seedAttachment(ctx, t, conn, ch.ID, alice.ID, nil, "wqfbzt-orphan.pdf")
	seedAttachment(ctx, t, conn, ch.ID, alice.ID, &doomed, "wqfbzt-deleted.pdf")

	// Soft delete is what the product does: the content is erased, the
	// attachments rows stay attached, and the cards must go with the message.
	conn.Exec(ctx, t,
		`UPDATE messages SET deleted_at = ?, content = '' WHERE id = ?`, time.Now().UTC(), doomed)

	page := mustSearchFiles(ctx, t, store, alice.ID, "wqfbzt", 50, nil)
	if got := fileNames(page); !slices.Equal(got, []string{"wqfbzt-sent.pdf"}) {
		t.Errorf("results = %q, want only the file on a live message", got)
	}
	if page.Total != 1 {
		t.Errorf("total = %d, want 1 — the count excludes them too", page.Total)
	}
}

// TestFileSearchPagingIntegration pins the one place the files keyset had to
// differ from the message half's: several files can ride on ONE message, so
// a cursor keyed on the message's id would drop every card after the one a
// page boundary fell on. All three files here share a message and a
// timestamp; paging must still see each exactly once.
func TestFileSearchPagingIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("files", storage.ChannelKindPrivate, alice.ID))

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	msg := searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "three at once", at)
	want := []string{"hbnmrt-a.pdf", "hbnmrt-b.pdf", "hbnmrt-c.pdf"}
	for _, filename := range want {
		seedAttachment(ctx, t, conn, ch.ID, alice.ID, &msg, filename)
	}

	first := mustSearchFiles(ctx, t, store, alice.ID, "hbnmrt", 2, nil)
	if len(first.Results) != 2 || !first.HasMore {
		t.Fatalf("first page = %q (has more %v), want 2 results and another page",
			fileNames(first), first.HasMore)
	}
	if first.Total != 3 {
		t.Errorf("total = %d, want 3 — the count is of the whole result set", first.Total)
	}

	last := first.Results[len(first.Results)-1]
	second := mustSearchFiles(ctx, t, store, alice.ID, "hbnmrt", 2, &storage.MessageCursor{
		CreatedAt: last.CreatedAt, ID: last.Attachment.ID,
	})
	if len(second.Results) != 1 || second.HasMore {
		t.Fatalf("second page = %q (has more %v), want the last result and no more",
			fileNames(second), second.HasMore)
	}

	got := append(fileNames(first), fileNames(second)...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("paging saw %q, want each of %q exactly once", got, want)
	}
}

// explainPlan returns the plan PostgreSQL produces for one statement, with
// sequential scans disabled on the connection so a fixture too small to be
// worth an index does not hide whether the index is usable at all.
func explainPlan(ctx context.Context, t *testing.T, conn *pgx.Conn, query string, args ...any) string {
	t.Helper()

	rows, err := conn.Query(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			t.Fatalf("read plan line: %v", scanErr)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return plan.String()
}

// TestFileSearchUsesTrigramIndexIntegration is the guard on the failure
// nothing else would notice: normalizedFilename and migration 0007's index
// expression drifting apart. Search keeps working — it just stops being able
// to use the index and becomes a scan over every attachment on the instance.
//
// It asserts the two halves of that separately and on purpose. WHETHER the
// planner picks the trigram index for the whole statement is a cost decision
// against the message_id index that moves with how many attachments the
// table holds, so a test that pinned the choice would be pinning the planner
// on fixture size rather than catching drift. What must never drift is the
// expression, so the expression is EXPLAINed on its own — and the statement
// is checked to be built from that same constant.
//
// The membership lookup is asserted in the real statement's plan, because
// "the fold is indexable" and "the scope is joined" are the two facts this
// query has to keep at once.
func TestFileSearchUsesTrigramIndexIntegration(t *testing.T) {
	testdb.RequiresPostgres(t, "EXPLAIN against migration 0007's GIN pg_trgm index, which the SQLite tree deliberately does not build")
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("planned", storage.ChannelKindPrivate, alice.ID))
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	msg := searchSeedMessage(ctx, t, raw, ch.ID, alice.ID, "one file", at)
	seedAttachment(ctx, t, raw, ch.ID, alice.ID, &msg, "quarterly-report.pdf")
	conn := explainConn(ctx, t, raw.DSN())

	// The fold, alone: does migration 0007's index answer this predicate?
	probe := `SELECT 1 FROM attachments a WHERE ` + storage.NormalizedFilename + ` ILIKE $1 ESCAPE '\'`
	if plan := explainPlan(ctx, t, conn, probe, "%report%"); !strings.Contains(plan, "attachments_filename_search_idx") {
		t.Errorf("the search fold cannot use migration 0007's index; plan was:\n%s", plan)
	}

	// And is the statement actually built from that fold?
	if !strings.Contains(storage.FileSearchPageQuery, storage.NormalizedFilename) {
		t.Errorf("the file search statement does not use the indexed fold:\n%s", storage.FileSearchPageQuery)
	}

	// The scope, in the real statement's plan.
	plan := explainPlan(ctx, t, conn, storage.FileSearchPageQuery, alice.ID, "%report%", 21)
	if !strings.Contains(plan, "channel_members") {
		t.Errorf("the file search plan does not look up channel membership; plan was:\n%s", plan)
	}
}
