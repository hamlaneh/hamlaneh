package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Message search: the search column above the conversation list.
//
// There is no authz.Can call here, and that is deliberate rather than
// forgotten. Every other resource read names a resource the caller may or
// may not reach, so the handler asks about it. Search names none: the scope
// IS the caller, joined against channel_members inside the SQL
// (storage/search.go), so a conversation the caller is not in cannot reach
// the results, the count or a snippet — there is nothing left afterwards for
// a permission check to filter. A membership check per hit would be strictly
// weaker: it would be a filter applied to rows the query had already read.
//
// The contract's bounds on q and limit are checked here. The generated
// router produces models only — it enforces neither minimum nor maximum — so
// the bounds checked in this file are the only bounds there are.

const (
	// maxSearchQueryLen bounds `q` (openapi.yaml, search). One character is
	// a legitimate query in Persian, so the minimum is the contract's 1.
	maxSearchQueryLen = 200
	// Search has its own page bounds, smaller than the list endpoints':
	// every result carries a whole message body as its snippet.
	defaultSearchLimit = 20
	maxSearchLimit     = 50
)

// messageSearcher is the storage read behind GET /api/v1/search.
//
// It is declared here rather than added to Store because slice 1.2b touches
// no shared file; the orchestrator can fold it into Store when the branches
// meet. *storage.Store satisfies it, checked below, so production can never
// take the fail-closed path in Search.
type messageSearcher interface {
	SearchMessages(ctx context.Context, params storage.SearchMessagesParams) (storage.SearchPage, error)
}

var _ messageSearcher = (*storage.Store)(nil)

// Search returns one page of message hits from the caller's own
// conversations, newest first.
//
// kind=files is accepted and answers an empty page: the contract promises
// the tab now and the upload pipeline is Phase 1.3 (openapi.yaml,
// SearchKind). An empty page is not an error — the tab exists, it just has
// nothing in it yet.
func (s *apiServer) Search(w http.ResponseWriter, r *http.Request, params api.SearchParams) {
	// The route table gates this path with a session (classSession), and the
	// principal is not incidental here: it is the search's entire scope. A
	// missing one would mean the route had been reclassified, and a search
	// with no scope is every message on the instance. Fail closed.
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("search reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	// Budgeted per account, not per address: what a search costs depends on
	// how many messages this caller can reach, and one person behind a shared
	// office address should not be able to spend everyone else's budget.
	// Spent before the query is validated — a refused request that still runs
	// the expensive part is not a rate limit.
	accountKey := prin.user.ID.String()
	if s.searchLimiter.Limited(accountKey) {
		writeRateLimited(w, r, s.searchLimiter.RetryAfter(accountKey))
		return
	}
	s.searchLimiter.Record(accountKey)

	// Every parameter is validated before the kind is acted on, so a
	// malformed request answers the same 400 on either tab.
	query, ok := searchQuery(w, r, params.Q)
	if !ok {
		return
	}
	kind, ok := searchKind(w, r, params.Kind)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, params.Limit, defaultSearchLimit, maxSearchLimit)
	if !ok {
		return
	}
	var after *storage.MessageCursor
	if params.Cursor != nil {
		if after, ok = messageCursor(w, r, *params.Cursor); !ok {
			return
		}
	}

	if kind == api.Files {
		writeJSONValue(w, r, http.StatusOK, emptySearchPage(kind))
		return
	}

	searcher, ok := store.(messageSearcher)
	if !ok {
		internalError(w, r, errors.New("store cannot search messages"))
		return
	}
	page, err := searcher.SearchMessages(r.Context(), storage.SearchMessagesParams{
		UserID: prin.user.ID,
		Query:  query,
		After:  after,
		Limit:  limit,
	})
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiSearchPage(kind, page))
}

// searchQuery resolves `q` against the contract's length bounds, in
// characters rather than bytes — a 200-character Persian query is twice that
// many bytes, and a limit that counted bytes would refuse half of it.
func searchQuery(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	if n := utf8.RuneCountInString(requested); n < 1 || n > maxSearchQueryLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("q must be between 1 and %d characters", maxSearchQueryLen))
		return "", false
	}
	return requested, true
}

// searchKind resolves the optional tab. An absent kind is the contract's
// default; an unknown one is a 400 rather than a silent fallback to
// messages, which would answer a question nobody asked.
func searchKind(w http.ResponseWriter, r *http.Request, requested *api.SearchKind) (api.SearchKind, bool) {
	if requested == nil {
		return api.Messages, true
	}
	if !requested.Valid() {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"kind must be messages or files")
		return "", false
	}
	return *requested, true
}

// emptySearchPage is a well-formed page with nothing in it — the files tab
// until Phase 1.3, and the shape every empty answer takes. results is an
// empty array rather than null: the contract makes it required.
func emptySearchPage(kind api.SearchKind) api.SearchPage {
	return api.SearchPage{Kind: kind, Results: []api.SearchResult{}}
}

// apiSearchPage maps a storage page onto the contract's SearchPage.
func apiSearchPage(kind api.SearchKind, page storage.SearchPage) api.SearchPage {
	out := emptySearchPage(kind)
	out.Total = page.Total
	out.TotalCapped = page.Capped
	out.Results = make([]api.SearchResult, 0, len(page.Results))
	for _, res := range page.Results {
		out.Results = append(out.Results, apiSearchResult(res))
	}
	if page.HasMore && len(page.Results) > 0 {
		last := page.Results[len(page.Results)-1]
		next := encodeTimeCursor(last.CreatedAt, last.MessageID)
		out.NextCursor = &next
	}
	return out
}

// apiSearchResult maps one hit. The author is the same reduced UserSummary
// every other endpoint serves, and the snippet is parts — never markup, so a
// message body cannot inject anything through a search result.
func apiSearchResult(res storage.SearchResult) api.SearchResult {
	parts := make([]api.SearchSnippetPart, 0, len(res.Snippet))
	for _, part := range res.Snippet {
		parts = append(parts, api.SearchSnippetPart{Text: part.Text, Match: part.Match})
	}
	return api.SearchResult{
		MessageId: res.MessageID,
		Channel:   apiChannelRef(res.Channel),
		Author: api.UserSummary{
			Id:          res.Author.ID,
			Username:    res.Author.Username,
			DisplayName: res.Author.DisplayName,
		},
		CreatedAt: res.CreatedAt,
		Snippet:   api.SearchSnippet{Parts: parts},
	}
}

// apiChannelRef maps the label of a hit: a slug for a named channel, the
// peer for a direct message. It carries no topic, no counts and no member
// list — a search result labels a conversation, it does not describe one.
func apiChannelRef(ref storage.SearchChannelRef) api.ChannelRef {
	out := api.ChannelRef{Id: ref.ID, Kind: api.ChannelKind(ref.Kind), Slug: ref.Slug}
	if peer := ref.DMPeer; peer != nil {
		out.DmPeer = &api.UserSummary{Id: peer.ID, Username: peer.Username, DisplayName: peer.DisplayName}
	}
	return out
}
