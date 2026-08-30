package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// Search fixtures in Persian are written as Unicode escapes with an English
// gloss, for the reason storage/search.go writes its folding constants that
// way: several of these characters are visually identical to a different
// code point and one of them is invisible, so a literal would be
// unreviewable — and the invisible one is precisely what half these tests
// are about.
const (
	// faNewBooks is "the new books", written with a zero-width non-joiner
	// inside the first word, which is how Persian actually spells it.
	faNewBooks = "\u06A9\u062A\u0627\u0628\u200C\u0647\u0627\u06CC" + " " + "\u062A\u0627\u0632\u0647"
	// faBook is "book", spelled with the Persian keheh a Persian keyboard
	// produces.
	faBook = "\u06A9\u062A\u0627\u0628"
	// faBookArabicKaf is the same word spelled with the Arabic kaf an Arabic
	// keyboard produces. Different code point, identical to the eye.
	faBookArabicKaf = "\u0643\u062A\u0627\u0628"
	// faBooksNoZWNJ is "books" written without the zero-width non-joiner —
	// the other way people type the word in faNewBooks.
	faBooksNoZWNJ = "\u06A9\u062A\u0627\u0628\u0647\u0627"
	// faPluralSuffix is the plural ending on its own, used to spell out
	// what a match spanning the invisible character must cover.
	faPluralSuffix = "\u0647\u0627"
	// faIWent and faIGo are two inflections of the same Persian verb. No
	// stemmer is involved anywhere in this design, so neither finds the
	// other; the test below pins that.
	faIWent = "\u0631\u0641\u062A\u0645"
	faIGo   = "\u0645\u06CC\u200C\u0631\u0648\u0645"
	// zwnj is the zero-width non-joiner on its own: a query made only of it
	// folds away to nothing.
	zwnj = "\u200C"
)

// searchSeedMessage inserts one message at an explicit time — the row
// CreateMessage cannot produce, because the server always stamps now() — and
// returns its id.
func searchSeedMessage(
	ctx context.Context, t *testing.T, raw *testdb.Raw,
	channelID, authorID uuid.UUID, content string, at time.Time,
) uuid.UUID {
	t.Helper()

	id := uuid.New()
	raw.Exec(ctx, t,
		`INSERT INTO messages (id, channel_id, author_id, client_msg_id, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, channelID, authorID, uuid.New(), content, at)
	return id
}

// explainConn is the pgx connection the two planner tests below need. EXPLAIN
// is only meaningful next to the SET that precedes it, and testdb.Raw is a
// pool that would be free to answer the two on different sessions — so these
// tests, which are PostgreSQL-only anyway, drive pgx directly.
func explainConn(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for EXPLAIN: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := conn.Close(context.Background()); closeErr != nil {
			t.Errorf("close EXPLAIN connection: %v", closeErr)
		}
	})
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable sequential scans: %v", err)
	}
	return conn
}

// mustSearch runs one page of search for a caller.
func mustSearch(
	ctx context.Context, t *testing.T, store testdb.Store,
	userID uuid.UUID, query string, limit int, after *storage.MessageCursor,
) storage.SearchPage {
	t.Helper()

	page, err := store.SearchMessages(ctx, storage.SearchMessagesParams{
		UserID: userID, Query: query, After: after, Limit: limit,
	})
	if err != nil {
		t.Fatalf("SearchMessages(%q): %v", query, err)
	}
	return page
}

// snippetText concatenates every part of a snippet. The contract's promise
// is that this reproduces the message, so every assertion about a snippet
// starts here.
func snippetText(parts []storage.SearchSnippetPart) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

// matchedRuns lists the runs a snippet flagged as matching.
func matchedRuns(parts []storage.SearchSnippetPart) []string {
	runs := []string{}
	for _, part := range parts {
		if part.Match {
			runs = append(runs, part.Text)
		}
	}
	return runs
}

// resultTexts is every result of a page as its reconstructed message.
func resultTexts(page storage.SearchPage) []string {
	texts := make([]string, 0, len(page.Results))
	for _, res := range page.Results {
		texts = append(texts, snippetText(res.Snippet))
	}
	return texts
}

// TestSearchScopeIntegration is the leak test, and the reason search is
// written as one membership-joined query rather than a read plus a filter.
//
// All three seeded messages carry a token the needle matches, so matching
// excludes none of them: the ONLY thing that can keep the two foreign ones
// out of the page, out of the total, and out of a snippet is the join
// against channel_members inside the statement. Each token names itself, so
// a leak is unmistakable in the failure output rather than a count being off
// by one.
func TestSearchScopeIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	mallory := mustCreateUser(ctx, t, store, newUser("mallory"))
	carol := mustCreateUser(ctx, t, store, newUser("carol"))

	// Alice's own channel, and two conversations she is in no part of: a
	// private channel of Mallory's, and a direct message between the other
	// two people.
	mine := mustCreateChannel(ctx, t, store, newChannel("mine", storage.ChannelKindPrivate, alice.ID))
	theirs := mustCreateChannel(ctx, t, store, newChannel("theirs", storage.ChannelKindPrivate, mallory.ID))
	dm := mustOpenDM(ctx, t, store, mallory.ID, carol.ID)

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	searchSeedMessage(ctx, t, conn, mine.ID, alice.ID, "zqxjkv-mine is alice's own", at)
	searchSeedMessage(ctx, t, conn, theirs.ID, mallory.ID, "zqxjkv-channel is not hers", at)
	searchSeedMessage(ctx, t, conn, dm.ID, mallory.ID, "zqxjkv-dm is not hers either", at)

	page := mustSearch(ctx, t, store, alice.ID, "zqxjkv", 50, nil)

	if got := resultTexts(page); !slices.Equal(got, []string{"zqxjkv-mine is alice's own"}) {
		t.Errorf("results = %q, want only alice's own message", got)
	}
	if page.Total != 1 || page.Capped {
		t.Errorf("total = %d (capped %v), want 1 and not capped — the count is scoped too",
			page.Total, page.Capped)
	}
	// Belt and braces: no snippet, channel label or author name from either
	// foreign conversation may appear anywhere in what came back. Marshalled
	// rather than formatted, so the peer and slug behind every pointer are
	// part of what is searched for a leak.
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal page for the leak check: %v", err)
	}
	rendered := string(encoded)
	for _, leak := range []string{"zqxjkv-channel", "zqxjkv-dm", "theirs", "mallory", "carol"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("search page leaks %q: %s", leak, rendered)
		}
	}

	// The other side of the same fact: Mallory, who IS in both, sees two.
	if got := mustSearch(ctx, t, store, mallory.ID, "zqxjkv", 50, nil).Total; got != 2 {
		t.Errorf("mallory's total = %d, want 2 — scoping must exclude, not just hide", got)
	}
}

// TestSearchMatchingIntegration pins what the trigram design does and,
// just as deliberately, what it does not.
//
// The "not" rows are the point: substring matching has no stemmer in either
// language and no word boundaries in either, and both facts are documented
// here as tests so they are discovered by a reader rather than by a user.
func TestSearchMatchingIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("mixed", storage.ChannelKindPrivate, alice.ID))

	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, content := range []string{
		"deploying the release now",
		"DEPLOYMENT checklist",
		"concatenate the strings",
		faNewBooks,
		faIWent,
		"100% cotton",
		"1000 threads",
		"a_b marker",
		"axb marker",
	} {
		searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, content, at)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		// English: substring, case-insensitive, in both directions of case.
		{"finds english inside a word", "deploy",
			[]string{"DEPLOYMENT checklist", "deploying the release now"}},
		{"is case-insensitive", "DEPLOYING", []string{"deploying the release now"}},
		{"matches a lowercase needle against uppercase text", "deployment",
			[]string{"DEPLOYMENT checklist"}},
		// The English limitation, pinned: no stemmer runs anywhere, so an
		// inflected needle finds nothing even though its stem is present.
		{"does NOT stem english", "deployed", nil},
		// And no word boundaries: a substring is a substring.
		{"crosses word boundaries", "cat", []string{"concatenate the strings"}},

		// Persian: the same substring rule, plus the normalization that makes
		// the two ways of typing these words one needle.
		{"finds persian inside a word", faBook, []string{faNewBooks}},
		{"folds the arabic kaf onto the persian keheh", faBookArabicKaf, []string{faNewBooks}},
		{"ignores the zero-width non-joiner", faBooksNoZWNJ, []string{faNewBooks}},
		// The Persian limitation, pinned in the same shape as the English
		// one: two inflections of one verb are two unrelated strings here.
		{"does NOT stem persian", faIGo, nil},

		// LIKE metacharacters are the caller's literal text, never syntax.
		{"percent is a literal percent", "100%", []string{"100% cotton"}},
		{"underscore is a literal underscore", "a_b", []string{"a_b marker"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resultTexts(mustSearch(ctx, t, store, alice.ID, tt.query, 50, nil))
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("search %q matched %q, want %q", tt.query, got, want)
			}
		})
	}
}

// TestSearchSnippetIntegration pins the contract's snippet: parts that
// reconstruct the message exactly, with the query's occurrences flagged.
//
// Reconstruction is the security property, not a nicety — it is what "the
// server never renders HTML" means concretely. Nothing is added to the text
// and nothing is escaped out of it, so there is no place for a message body
// to inject markup through a search result.
func TestSearchSnippetIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		content     string
		query       string
		wantMatches []string
	}{
		{
			name:        "flags every occurrence",
			content:     "deploy, then deploy again",
			query:       "deploy",
			wantMatches: []string{"deploy", "deploy"},
		},
		{
			name:        "flags a match that opens the message",
			content:     "deploy now",
			query:       "deploy",
			wantMatches: []string{"deploy"},
		},
		{
			name:        "highlights the text's own casing, not the query's",
			content:     "DEPLOY now",
			query:       "deploy",
			wantMatches: []string{"DEPLOY"},
		},
		{
			// Queried with the Arabic kaf; the highlighted run must be the
			// message's own Persian keheh spelling, because the parts are the
			// message and not the query.
			name:        "highlights the persian the message actually contains",
			content:     faNewBooks,
			query:       faBookArabicKaf,
			wantMatches: []string{faBook},
		},
		{
			// The needle has no ZWNJ and the message does; the highlighted
			// run covers the message's characters, invisible one included.
			name:        "a match spanning a zero-width non-joiner keeps it",
			content:     faNewBooks,
			query:       faBooksNoZWNJ,
			wantMatches: []string{faBook + zwnj + faPluralSuffix},
		},
		{
			name:        "an unmatched message would not be here, but markup is still never added",
			content:     `<script>alert("deploy")</script>`,
			query:       "deploy",
			wantMatches: []string{"deploy"},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One channel per case, so a needle can never pick up another
			// case's message and every result is unambiguous.
			channel := mustCreateChannel(ctx, t, store,
				newChannel(fmt.Sprintf("snippet-%d", i), storage.ChannelKindPrivate, alice.ID))
			searchSeedMessage(ctx, t, conn, channel.ID, alice.ID, tt.content, at)

			page := mustSearch(ctx, t, store, alice.ID, tt.query, 50, nil)
			var parts []storage.SearchSnippetPart
			for _, res := range page.Results {
				if res.Channel.ID == channel.ID {
					parts = res.Snippet
				}
			}
			if parts == nil {
				t.Fatalf("no result for %q searching %q", tt.content, tt.query)
			}
			if got := snippetText(parts); got != tt.content {
				t.Errorf("snippet parts concatenate to %q, want the message %q", got, tt.content)
			}
			if got := matchedRuns(parts); !slices.Equal(got, tt.wantMatches) {
				t.Errorf("matched runs = %q, want %q", got, tt.wantMatches)
			}
			for _, part := range parts {
				if part.Text == "" {
					t.Errorf("snippet carries an empty part: %+v", parts)
				}
			}
		})
	}
}

// TestSearchTotalIntegration pins the counted label above the results: exact
// below the cap, and the cap itself with total_capped beyond it.
func TestSearchTotalIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("counted", storage.ChannelKindPrivate, alice.ID))
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	for i := range 5 {
		searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, fmt.Sprintf("few-hit %d", i), at)
	}
	// One past the cap: the point is the count, not the inserts. created_at
	// strictly increases so paging below has an order.
	for i := range storage.SearchTotalCap + 1 {
		searchSeedMessage(ctx, t, conn, ch.ID, alice.ID,
			fmt.Sprintf("many-hit %d", i), at.Add(time.Duration(i+1)*time.Millisecond))
	}

	t.Run("exact below the cap", func(t *testing.T) {
		page := mustSearch(ctx, t, store, alice.ID, "few-hit", 2, nil)
		if page.Total != 5 || page.Capped {
			t.Errorf("total = %d (capped %v), want 5 and not capped", page.Total, page.Capped)
		}
		if len(page.Results) != 2 || !page.HasMore {
			t.Errorf("page has %d results (more %v), want 2 and more — total is not the page size",
				len(page.Results), page.HasMore)
		}
	})

	t.Run("caps beyond it", func(t *testing.T) {
		page := mustSearch(ctx, t, store, alice.ID, "many-hit", 10, nil)
		if page.Total != storage.SearchTotalCap || !page.Capped {
			t.Errorf("total = %d (capped %v), want %d and capped",
				page.Total, page.Capped, storage.SearchTotalCap)
		}
	})

	t.Run("the total labels the query, not what is left of it", func(t *testing.T) {
		first := mustSearch(ctx, t, store, alice.ID, "few-hit", 2, nil)
		last := first.Results[len(first.Results)-1]
		second := mustSearch(ctx, t, store, alice.ID, "few-hit", 2,
			&storage.MessageCursor{CreatedAt: last.CreatedAt, ID: last.MessageID})
		if second.Total != 5 {
			t.Errorf("total on the second page = %d, want 5", second.Total)
		}
	})
}

// TestSearchSoftDeletedIntegration pins what search does with a deleted
// message: nothing at all.
//
// The behavior is not a filter somebody remembered to write — deletion
// erases the content (constraint messages_content_shape), so a deleted row
// has no text left to match. The explicit deleted_at predicate is what makes
// that true even for the one needle that matches empty text.
func TestSearchSoftDeletedIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("deletions", storage.ChannelKindPrivate, alice.ID))
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	doomed := searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "erasable secret", at)
	searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, "a surviving message", at.Add(time.Second))

	if got := mustSearch(ctx, t, store, alice.ID, "erasable", 50, nil).Total; got != 1 {
		t.Fatalf("total before deletion = %d, want 1", got)
	}

	// What slice 1.2b's delete handler will write.
	conn.Exec(ctx, t,
		`UPDATE messages SET content = '', deleted_at = ?, deleted_by = author_id WHERE id = ?`,
		time.Now().UTC(), doomed)

	page := mustSearch(ctx, t, store, alice.ID, "erasable", 50, nil)
	if page.Total != 0 || len(page.Results) != 0 {
		t.Errorf("a deleted message is still searchable: %+v", page)
	}

	// A query of nothing but zero-width non-joiners folds away to the empty
	// needle, which SQL matches against every string — including the erased
	// content of a deleted row. The deleted_at predicate is the only thing
	// keeping that row out, so this is where it earns its place.
	empty := mustSearch(ctx, t, store, alice.ID, zwnj+zwnj, 50, nil)
	if got := resultTexts(empty); !slices.Equal(got, []string{"a surviving message"}) {
		t.Errorf("empty needle matched %q, want only the live message", got)
	}
}

// TestSearchPagingIntegration walks a result set two at a time and checks
// nothing is lost or repeated at a page boundary.
//
// Three of the five messages share one created_at on purpose: chat messages
// land in the same instant routinely, and a cursor made of the timestamp
// alone would either skip the rest of that run or serve it forever.
func TestSearchPagingIntegration(t *testing.T) {
	t.Parallel()

	store, conn := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("paged", storage.ChannelKindPrivate, alice.ID))

	tied := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{
		tied.Add(-time.Minute), tied, tied, tied, tied.Add(time.Minute),
	} {
		searchSeedMessage(ctx, t, conn, ch.ID, alice.ID, fmt.Sprintf("paged-hit %d", i), at)
	}

	var walked []string
	var after *storage.MessageCursor
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination never terminates")
		}
		page := mustSearch(ctx, t, store, alice.ID, "paged-hit", 2, after)
		walked = append(walked, resultTexts(page)...)
		if !page.HasMore {
			break
		}
		last := page.Results[len(page.Results)-1]
		after = &storage.MessageCursor{CreatedAt: last.CreatedAt, ID: last.MessageID}
	}

	slices.Sort(walked)
	want := []string{"paged-hit 0", "paged-hit 1", "paged-hit 2", "paged-hit 3", "paged-hit 4"}
	if !slices.Equal(walked, want) {
		t.Errorf("keyset walk = %q, want each message exactly once %q", walked, want)
	}
}

// TestSearchFoldingMatchesSQLIntegration pins the Go fold against the SQL
// expression it has to agree with.
//
// The two live in different files and different languages — search.go folds
// the needle, migration 0006 folds the indexed column — and a disagreement
// between them is silent: the row matches in SQL and its snippet comes back
// with nothing highlighted. This is the test that would fail instead.
func TestSearchFoldingMatchesSQLIntegration(t *testing.T) {
	testdb.RequiresPostgres(t, "pins the Go fold to migration 0006's translate() index expression, which the SQLite tree has no counterpart for")
	t.Parallel()

	_, raw := testdb.New(t)
	ctx := context.Background()
	conn := explainConn(ctx, t, raw.DSN())

	for _, in := range []string{
		"Deploying THE release",
		faNewBooks,
		faBookArabicKaf,
		faIGo,
		faBook + zwnj + faBook,
		"plain ascii 123",
		"",
	} {
		var got string
		err := conn.QueryRow(ctx,
			`SELECT lower(translate($1, U&'\064A\0643\0629\200C', U&'\06CC\06A9\0647'))`, in).Scan(&got)
		if err != nil {
			t.Fatalf("SQL fold of %q: %v", in, err)
		}
		if want := storage.FoldSearchText(in); got != want {
			t.Errorf("fold of %q: SQL %q, Go %q — the index and the needle disagree", in, got, want)
		}
	}
}

// TestSearchUsesTrigramIndexIntegration is the guard on the one failure this
// design has that nothing else would notice: the search statement and
// migration 0006's index expression drifting apart. Search keeps working —
// it just stops using the index and becomes a sequential scan over every
// message on the instance.
//
// Sequential scans are disabled for the session because the fixture is small
// enough that a scan is genuinely cheaper; what is being asserted is that
// the planner CAN use this index for this statement, not what it prefers on
// three rows.
func TestSearchUsesTrigramIndexIntegration(t *testing.T) {
	testdb.RequiresPostgres(t, "EXPLAIN against migration 0006's GIN pg_trgm index, which the SQLite tree deliberately does not build")
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("alice"))
	ch := mustCreateChannel(ctx, t, store, newChannel("planned", storage.ChannelKindPrivate, alice.ID))
	searchSeedMessage(ctx, t, raw, ch.ID, alice.ID, "deploying the release",
		time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))

	conn := explainConn(ctx, t, raw.DSN())
	rows, err := conn.Query(ctx, "EXPLAIN "+storage.SearchPageQuery, alice.ID, "%deploy%", 21)
	if err != nil {
		t.Fatalf("EXPLAIN the search statement: %v", err)
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

	if !strings.Contains(plan.String(), "messages_content_search_idx") {
		t.Errorf("the search statement cannot use the trigram index; plan was:\n%s", plan.String())
	}
}

// TestSearchSnippet covers the splitter directly, including the cases a
// database fixture cannot conveniently reach. It needs no PostgreSQL, so it
// runs everywhere the integration tests are skipped.
func TestSearchSnippet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		needle  string // already folded, as the splitter requires
		want    []storage.SearchSnippetPart
	}{
		{
			name: "no match is one plain part", content: "nothing here", needle: "deploy",
			want: []storage.SearchSnippetPart{{Text: "nothing here"}},
		},
		{
			name: "an empty needle highlights nothing", content: "anything", needle: "",
			want: []storage.SearchSnippetPart{{Text: "anything"}},
		},
		{
			name: "a whole-message match is one flagged part", content: "deploy", needle: "deploy",
			want: []storage.SearchSnippetPart{{Text: "deploy", Match: true}},
		},
		{
			name: "adjacent matches stay separate parts", content: "aa", needle: "a",
			want: []storage.SearchSnippetPart{
				{Text: "a", Match: true}, {Text: "a", Match: true},
			},
		},
		{
			// Non-overlapping, like every highlighter: "aa" in "aaa" is one
			// match plus a leftover, not two overlapping ones.
			name: "matches do not overlap", content: "aaa", needle: "aa",
			want: []storage.SearchSnippetPart{{Text: "aa", Match: true}, {Text: "a"}},
		},
		{
			name: "an empty message is no parts at all", content: "", needle: "deploy",
			want: []storage.SearchSnippetPart{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := storage.SearchSnippet(tt.content, tt.needle)
			if !slices.Equal(got, tt.want) {
				t.Errorf("SearchSnippet(%q, %q) = %+v, want %+v", tt.content, tt.needle, got, tt.want)
			}
			if text := snippetText(got); text != tt.content {
				t.Errorf("parts concatenate to %q, want the original %q", text, tt.content)
			}
		})
	}
}
