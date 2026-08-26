package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The instance user directory: the list behind "Invite people" on a channel
// and the "+" beside Direct messages.
//
// It is session-gated and nothing more — deliberately not admin-gated.
// Inviting somebody requires finding them first, so every signed-in user may
// read it. What keeps that safe is the shape rather than the audience: every
// row is the reduced UserSummary the contract describes (openapi.yaml,
// listUsers), which carries no email, no role and no password state.

// maxDirectoryQueryLen bounds the `q` filter (openapi.yaml listUsers). The
// generated code produces models only — it enforces neither minimum nor
// maximum — so a bound checked here is the only bound there is.
const maxDirectoryQueryLen = 64

// ListUsers returns one page of the user directory in username order.
func (s *apiServer) ListUsers(w http.ResponseWriter, r *http.Request, params api.ListUsersParams) {
	// The route table gates this path with a session (classSession). The
	// answer does not depend on who is asking, so the principal is otherwise
	// unused — but its absence would mean the route had been reclassified,
	// and a directory served to an anonymous caller is a leak of every name
	// on the instance. Fail closed instead.
	if _, ok := principalFrom(r.Context()); !ok {
		internalError(w, r, errors.New("user directory reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	limit, ok := pageLimit(w, r, params.Limit, defaultListLimit, maxListLimit)
	if !ok {
		return
	}
	query, ok := directoryQuery(w, r, params.Q)
	if !ok {
		return
	}
	var after *storage.DirectoryCursor
	if params.Cursor != nil {
		username, id, err := decodeCursor(*params.Cursor)
		if err != nil {
			writeInvalidCursor(w, r)
			return
		}
		after = &storage.DirectoryCursor{Username: username, UserID: id}
	}

	// One row beyond the page tells us whether a next page exists.
	users, err := store.ListDirectory(r.Context(), storage.ListDirectoryParams{
		Query: query,
		After: after,
		Limit: limit + 1,
	})
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.UserSummaryPage{Users: make([]api.UserSummary, 0, min(len(users), limit))}
	if len(users) > limit {
		users = users[:limit]
		last := users[len(users)-1]
		next := encodeCursor(last.Username, last.ID)
		page.NextCursor = &next
	}
	for _, u := range users {
		page.Users = append(page.Users, apiUserSummary(u))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// directoryQuery resolves the optional `q` filter against the contract's
// length bounds; an absent parameter is the empty, unfiltered query. On a
// violation it answers 400 and reports false.
func directoryQuery(w http.ResponseWriter, r *http.Request, requested *string) (string, bool) {
	if requested == nil {
		return "", true
	}
	if !storableText(*requested) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"the filter must be text that can be stored and returned unchanged")
		return "", false
	}
	if n := utf8.RuneCountInString(*requested); n < 1 || n > maxDirectoryQueryLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("q must be between 1 and %d characters", maxDirectoryQueryLen))
		return "", false
	}
	return *requested, true
}
