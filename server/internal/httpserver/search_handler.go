package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
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

// fileSearcher is the other tab's read, declared here for the same reason.
type fileSearcher interface {
	SearchFiles(ctx context.Context, params storage.SearchFilesParams) (storage.FileSearchPage, error)
}

var (
	_ messageSearcher = (*storage.Store)(nil)
	_ fileSearcher    = (*storage.Store)(nil)
)

// fileURLSigner mints the signed, expiring URLs an Attachment carries. The
// files origin is cookie-less, so possession of a fresh URL is the
// credential and every serialization mints its own (openapi.yaml,
// Attachment) — which is why this is a call per result rather than a column.
type fileURLSigner interface {
	// AttachmentURLs returns the download URL and, for a row that has one,
	// the thumbnail URL.
	AttachmentURLs(id uuid.UUID, hasThumbnail bool) (url string, thumbnail *string)
}

// unsignedFileURLs is the fallback for a server wired without a signer: the
// correctly shaped paths on the files origin, and no signature. It is a unit
// -test fixture, never production — an unsigned URL is refused by the files
// origin, which is the honest failure for a server that cannot sign.
type unsignedFileURLs struct{}

func (unsignedFileURLs) AttachmentURLs(id uuid.UUID, hasThumbnail bool) (string, *string) {
	// filesign.Path, not a literal: "correctly shaped" is a claim, and this
	// used to spell the thumbnail "/thumbnail" while the origin serves
	// "/thumb". Nothing caught it, because nothing production reads this.
	url := filesign.Path(id, blobstore.Original)
	if !hasThumbnail {
		return url, nil
	}
	thumbnail := filesign.Path(id, blobstore.Thumbnail)
	return url, &thumbnail
}

// Search returns one page of hits from the caller's own conversations,
// newest first: message bodies on the default tab, filenames on kind=files.
//
// Both tabs answer the same shape and share one budget, because they are one
// search box; what differs is which statement runs and, on files, that each
// result carries its attachment. The scope paragraph above holds for both —
// the files statement is the message statement's membership join with the
// attachments hung off it (storage/filesearch.go).
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

	// The contract's 429 for this endpoint is already spent by the time the
	// handler runs: budgetSearch in ratelimits.go, per account rather than
	// per address, because what a search costs follows how many messages this
	// caller can reach. It is spent before the handler on purpose — a refused
	// request that still runs the expensive part is not a rate limit.

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
		files, canSearchFiles := store.(fileSearcher)
		if !canSearchFiles {
			internalError(w, r, errors.New("store cannot search files"))
			return
		}
		page, err := files.SearchFiles(r.Context(), storage.SearchFilesParams{
			UserID: prin.user.ID,
			Query:  query,
			After:  after,
			Limit:  limit,
		})
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSONValue(w, r, http.StatusOK, s.apiFileSearchPage(kind, page))
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
	if !storableText(requested) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"the search query must be text that can be stored and returned unchanged")
		return "", false
	}
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

// emptySearchPage is a well-formed page with nothing in it — the shape every
// empty answer takes. results is an empty array rather than null: the
// contract makes it required.
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

// apiFileSearchPage maps a storage file page onto the same contract
// SearchPage the messages tab answers with. The cursor is the last result's
// time paired with its ATTACHMENT's id, because that is the keyset the
// statement pages on — one message can carry several files (storage/
// filesearch.go).
func (s *apiServer) apiFileSearchPage(kind api.SearchKind, page storage.FileSearchPage) api.SearchPage {
	out := emptySearchPage(kind)
	out.Total = page.Total
	out.TotalCapped = page.Capped
	out.Results = make([]api.SearchResult, 0, len(page.Results))
	for _, res := range page.Results {
		out.Results = append(out.Results, s.apiFileSearchResult(res))
	}
	if page.HasMore && len(page.Results) > 0 {
		last := page.Results[len(page.Results)-1]
		next := encodeTimeCursor(last.CreatedAt, last.Attachment.ID)
		out.NextCursor = &next
	}
	return out
}

// apiFileSearchResult maps one filename hit: the message half's fields, plus
// the attachment the contract says is present exactly on this tab. The card
// goes through the same apiAttachment a message's own cards do, so a file
// found by search and the same file seen in the conversation are one shape
// with one freshly minted pair of URLs — for a caller the membership join
// that produced the row has already proven entitled to it.
func (s *apiServer) apiFileSearchResult(res storage.FileSearchResult) api.SearchResult {
	out := apiSearchResult(res.SearchResult)
	attachment := api.AttachmentOf(res.Attachment, s.fileSigner)
	out.Attachment = &attachment
	return out
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
