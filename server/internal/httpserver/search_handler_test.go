package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// searchStore is a fakeStore that can also search. SearchMessages is not on
// httpserver.Store (search_handler.go says why), so it is added here by
// embedding rather than by widening the shared fake — which keeps this
// slice's test fixture in this slice's file.
type searchStore struct {
	*fakeStore
	searchMessages func(ctx context.Context, params storage.SearchMessagesParams) (storage.SearchPage, error)
}

var _ httpserver.Store = (*searchStore)(nil)

func (s *searchStore) SearchMessages(
	ctx context.Context, params storage.SearchMessagesParams,
) (storage.SearchPage, error) {
	if s.searchMessages == nil {
		return storage.SearchPage{}, errFakeUnwired
	}
	return s.searchMessages(ctx, params)
}

// searchFixtureTime is every fixture result's timestamp; the cursor a page
// hands back is derived from it.
var searchFixtureTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// searchingStore authenticates fixtureUser and answers every search with
// page, recording the parameters the handler asked for.
func searchingStore(page storage.SearchPage) (*searchStore, *storage.SearchMessagesParams) {
	var seen storage.SearchMessagesParams
	store := &searchStore{fakeStore: authedStore(fixtureUser())}
	store.searchMessages = func(_ context.Context, params storage.SearchMessagesParams) (storage.SearchPage, error) {
		seen = params
		return page, nil
	}
	return store, &seen
}

// searchResultFixture is one hit in a named channel, authored by a user
// carrying everything a search result must never publish.
func searchResultFixture() storage.SearchResult {
	slug := "deploys"
	return storage.SearchResult{
		MessageID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
		Channel: storage.SearchChannelRef{
			ID:   uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001"),
			Kind: storage.ChannelKindPublic,
			Slug: &slug,
		},
		Author: storage.MessageAuthor{
			ID:          uuid.MustParse("cccccccc-0000-0000-0000-000000000001"),
			Username:    "alice",
			DisplayName: "Alice",
		},
		CreatedAt: searchFixtureTime,
		Snippet: []storage.SearchSnippetPart{
			{Text: "before "},
			{Text: "deploy", Match: true},
			{Text: " after"},
		},
	}
}

// decodeSearchPage reads a 200 answer as the contract's page.
func decodeSearchPage(t *testing.T, store httpserver.Store, query string) api.SearchPage {
	t.Helper()

	rec := do(t, store, request(http.MethodGet, "/api/v1/search?"+query, "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var page api.SearchPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not a SearchPage: %v (%s)", err, rec.Body.String())
	}
	return page
}

// TestSearchScopesToTheCallersOwnPrincipal is the handler's half of the IDOR
// defense. The scope storage is asked to search is the authenticated
// principal's id and nothing the request could influence, so no query
// parameter can widen a search to somebody else's conversations.
func TestSearchScopesToTheCallersOwnPrincipal(t *testing.T) {
	t.Parallel()

	store, seen := searchingStore(storage.SearchPage{})
	if rec := do(t, store, request(http.MethodGet,
		"/api/v1/search?q=deploy&user_id=99999999-9999-9999-9999-999999999999", "",
		withSessionCookie("tok"))); rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if seen.UserID != fixtureUser().ID {
		t.Errorf("searched as %s, want the signed-in caller %s", seen.UserID, fixtureUser().ID)
	}
	if seen.Query != "deploy" {
		t.Errorf("searched for %q, want %q", seen.Query, "deploy")
	}
}

// TestSearchServesTheContractPage pins the mapping: results, the counted
// label, the snippet as parts, and a cursor only when another page exists.
func TestSearchServesTheContractPage(t *testing.T) {
	t.Parallel()

	store, _ := searchingStore(storage.SearchPage{
		Results: []storage.SearchResult{searchResultFixture()},
		Total:   4,
	})
	page := decodeSearchPage(t, store, "q=deploy")

	if page.Kind != api.Messages {
		t.Errorf("kind = %q, want messages", page.Kind)
	}
	if page.Total != 4 || page.TotalCapped {
		t.Errorf("total = %d (capped %v), want 4 and not capped", page.Total, page.TotalCapped)
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q although the page is the last one", *page.NextCursor)
	}
	if len(page.Results) != 1 {
		t.Fatalf("page has %d results, want 1", len(page.Results))
	}

	got := page.Results[0]
	want := searchResultFixture()
	if got.MessageId != want.MessageID {
		t.Errorf("message_id = %s, want %s", got.MessageId, want.MessageID)
	}
	if got.Channel.Slug == nil || *got.Channel.Slug != "deploys" || got.Channel.DmPeer != nil {
		t.Errorf("channel label = %+v, want the slug and no dm_peer", got.Channel)
	}
	if got.Author.Username != "alice" {
		t.Errorf("author = %+v, want alice", got.Author)
	}
	if !got.CreatedAt.Equal(searchFixtureTime) {
		t.Errorf("created_at = %s, want %s", got.CreatedAt, searchFixtureTime)
	}

	var text strings.Builder
	matched := []string{}
	for _, part := range got.Snippet.Parts {
		text.WriteString(part.Text)
		if part.Match {
			matched = append(matched, part.Text)
		}
	}
	if text.String() != "before deploy after" {
		t.Errorf("snippet parts concatenate to %q, want the message", text.String())
	}
	if len(matched) != 1 || matched[0] != "deploy" {
		t.Errorf("matched runs = %q, want one run of deploy", matched)
	}
}

// TestSearchServesNoMarkup is the injection test. A message body reaches the
// client as snippet text and nothing else — no HTML is rendered server-side,
// so the angle brackets arrive as the characters they are and the response
// carries no tag the server invented.
func TestSearchServesNoMarkup(t *testing.T) {
	t.Parallel()

	hostile := searchResultFixture()
	hostile.Snippet = []storage.SearchSnippetPart{
		{Text: `<img src=x onerror="`},
		{Text: "deploy", Match: true},
		{Text: `">`},
	}
	store, _ := searchingStore(storage.SearchPage{Results: []storage.SearchResult{hostile}, Total: 1})

	rec := do(t, store, request(http.MethodGet, "/api/v1/search?q=deploy", "", withSessionCookie("tok")))
	body := rec.Body.String()
	for _, markup := range []string{"<mark", "<em", "<b>", "<span"} {
		if strings.Contains(body, markup) {
			t.Errorf("response contains server-rendered markup %q: %s", markup, body)
		}
	}
	// The message's own characters survive, JSON-escaped and nothing more.
	page := decodeSearchPage(t, store, "q=deploy")
	if got := page.Results[0].Snippet.Parts[0].Text; got != `<img src=x onerror="` {
		t.Errorf("snippet text = %q, want the message's own characters", got)
	}
}

// TestSearchServesUserSummariesOnly is the leak test for the author row: a
// result names a person with the same three public fields every other
// endpoint serves, never an address, a role, or password state.
func TestSearchServesUserSummariesOnly(t *testing.T) {
	t.Parallel()

	store, _ := searchingStore(storage.SearchPage{
		Results: []storage.SearchResult{searchResultFixture()}, Total: 1,
	})
	rec := do(t, store, request(http.MethodGet, "/api/v1/search?q=deploy", "", withSessionCookie("tok")))

	var raw struct {
		Results []struct {
			Author map[string]any `json:"author"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not a page of results: %v", err)
	}
	for key := range raw.Results[0].Author {
		switch key {
		case "id", "username", "display_name":
		default:
			t.Errorf("result author carries %q; UserSummary is id, username and display_name only", key)
		}
	}
}

// TestSearchCursorsToTheNextPage pins that a cursor appears exactly when
// storage says another page exists, and that handing it back is what the
// next request is asked to resume from.
func TestSearchCursorsToTheNextPage(t *testing.T) {
	t.Parallel()

	result := searchResultFixture()
	store, seen := searchingStore(storage.SearchPage{
		Results: []storage.SearchResult{result}, Total: 4, HasMore: true,
	})

	first := decodeSearchPage(t, store, "q=deploy&limit=1")
	if first.NextCursor == nil {
		t.Fatal("no next_cursor although storage reported more results")
	}
	if seen.Limit != 1 {
		t.Errorf("asked storage for limit %d, want 1", seen.Limit)
	}

	decodeSearchPage(t, store, "q=deploy&limit=1&cursor="+*first.NextCursor)
	if seen.After == nil {
		t.Fatal("the cursor was not passed through to storage")
	}
	if !seen.After.CreatedAt.Equal(result.CreatedAt) || seen.After.ID != result.MessageID {
		t.Errorf("resumed from %+v, want the last result %s at %s",
			seen.After, result.MessageID, result.CreatedAt)
	}
}

// TestSearchFilesKindIsAnEmptyPage pins the contract's promise for the tab
// Phase 1.3 fills: accepted now, empty until then, and never an error. It
// must also never reach storage — there is nothing there to ask.
func TestSearchFilesKindIsAnEmptyPage(t *testing.T) {
	t.Parallel()

	store := &searchStore{fakeStore: authedStore(fixtureUser())} // searchMessages unwired: any call fails
	page := decodeSearchPage(t, store, "q=deploy&kind=files")

	if page.Kind != api.Files {
		t.Errorf("kind = %q, want files", page.Kind)
	}
	if len(page.Results) != 0 || page.Total != 0 || page.TotalCapped || page.NextCursor != nil {
		t.Errorf("files page = %+v, want an empty page", page)
	}
}

// TestSearchEmptyResultsIsAnArray pins the encoded shape of a page with
// nothing in it: results is [], never null, because the contract makes it
// required and a client mapping over null crashes.
func TestSearchEmptyResultsIsAnArray(t *testing.T) {
	t.Parallel()

	store, _ := searchingStore(storage.SearchPage{})
	rec := do(t, store, request(http.MethodGet, "/api/v1/search?q=nothing", "", withSessionCookie("tok")))
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Errorf("empty page encodes as %s, want results as an empty array", rec.Body.String())
	}
}

// TestSearchRejectsBadRequests pins the bounds the generated router does not
// enforce. Every one of them is 400 invalid_request, and none of them
// reaches storage.
func TestSearchRejectsBadRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{"a missing q", ""},
		{"an empty q", "q="},
		{"a q past the contract's length", "q=" + strings.Repeat("a", 201)},
		{"a limit of zero", "q=deploy&limit=0"},
		{"a limit past the contract's maximum", "q=deploy&limit=51"},
		{"a limit that is not a number", "q=deploy&limit=many"},
		{"an unknown kind", "q=deploy&kind=people"},
		{"a cursor the server cannot decode", "q=deploy&cursor=not-a-cursor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// searchMessages is unwired: reaching storage fails the test by
			// answering 500 instead of the 400 these expect.
			store := &searchStore{fakeStore: authedStore(fixtureUser())}
			rec := do(t, store, request(http.MethodGet, "/api/v1/search?"+tt.query, "",
				withSessionCookie("tok")))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestSearchQueryLengthCountsCharacters pins that the 200-character bound is
// characters and not bytes: a Persian query is two bytes per character, and
// a byte-counting bound would refuse half a legitimate one.
func TestSearchQueryLengthCountsCharacters(t *testing.T) {
	t.Parallel()

	store, seen := searchingStore(storage.SearchPage{})
	// 200 characters, 400 bytes: the Persian letter beh.
	long := strings.Repeat("\u0628", 200) // ARABIC LETTER BEH: one character, two bytes
	decodeSearchPage(t, store, "q="+long)
	if seen.Query != long {
		t.Errorf("a 200-character Persian query did not reach storage intact")
	}
}

// TestSearchReportsTheCappedTotal pins the flag the results column reads
// when there were more matches than anybody counts.
func TestSearchReportsTheCappedTotal(t *testing.T) {
	t.Parallel()

	store, _ := searchingStore(storage.SearchPage{Total: storage.SearchTotalCap, Capped: true})
	page := decodeSearchPage(t, store, "q=deploy")
	if page.Total != storage.SearchTotalCap || !page.TotalCapped {
		t.Errorf("total = %d (capped %v), want %d and capped",
			page.Total, page.TotalCapped, storage.SearchTotalCap)
	}
}

// TestSearchIsBudgetedPerAccount pins the 429 the contract reserves for this
// endpoint and nothing emitted until now.
//
// It is not decoration. A needle shorter than three characters cannot use the
// trigram index and falls back to a sequential scan over every message the
// caller can reach — measured at 240ms across 60,000 rows — and the contract
// allows a query that short deliberately, because one- and two-character
// words are ordinary in Persian. Cheap to ask for, occasionally expensive to
// serve, from any signed-in account, in a loop.
func TestSearchIsBudgetedPerAccount(t *testing.T) {
	t.Parallel()

	store, _ := searchingStore(storage.SearchPage{})
	// One handler across every attempt: the budget lives on the server, so a
	// fresh one per request would spend nothing.
	handler := httpserver.Handler(store)

	spend := func(t *testing.T) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet, "/api/v1/search?q=ab", "",
			withSessionCookie("tok")))
		return rec
	}

	for attempt := range httpserver.SearchRateLimit {
		if rec := spend(t); rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status %d, want 200 while the budget stands (body %s)",
				attempt+1, rec.Code, rec.Body.String())
		}
	}

	rec := spend(t)
	wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
	// Every 429 this server sends names its own wait; a client that has to
	// guess retries into the same refusal.
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the refusal carries no Retry-After")
	}
}
