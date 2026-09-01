package httpserver

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The dashboard half of SCIM provisioning: minting, listing and revoking the
// bearer tokens an identity provider's sync engine authenticates with
// (docs/api/scim.md §3). The provisioning surface itself is internal/scim,
// mounted outside this router's security middleware — these three are
// ordinary admin contract endpoints and go through every gate.

// maxScimNoteLen is the contract bound on CreateScimTokenRequest.note.
const maxScimNoteLen = 200

// ListScimTokens returns every provisioning token that has not been revoked.
// Never the tokens themselves — only their metadata, exactly as the invite
// list never carries a link.
func (s *apiServer) ListScimTokens(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	tokens, err := store.ListScimTokens(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.ScimTokenPage{Tokens: make([]api.ScimToken, 0, len(tokens))}
	for _, tok := range tokens {
		page.Tokens = append(page.Tokens, apiScimToken(tok))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// CreateScimToken mints a provisioning credential and answers with it once.
// Only the SHA-256 digest is stored, exactly as invitation links and
// password-reset tokens are, so a stolen database yields no usable
// credential and nothing can redisplay a value somebody closed the dialog
// on.
//
// Several live tokens at once is deliberate: rotating a credential an
// external system holds needs an overlap — mint, reconfigure the provider,
// revoke — and forcing one at a time would mean an outage on every rotation.
func (s *apiServer) CreateScimToken(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("create scim token reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.CreateScimTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var note string
	if req.Note != nil {
		note = strings.TrimSpace(*req.Note)
		if utf8.RuneCountInString(note) > maxScimNoteLen {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"note must be at most 200 characters")
			return
		}
	}

	raw, tokenHash := session.NewToken()
	tok, err := store.CreateScimToken(r.Context(), prin.user.ID, tokenHash, note)
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "scim.token.created",
		TargetID:    tok.ID,
		TargetLabel: note,
	})
	writeJSONValue(w, r, http.StatusCreated, api.CreatedScimToken{
		Token: raw,
		Scim:  apiScimToken(tok),
	})
}

// RevokeScimToken kills one provisioning credential. It takes effect
// immediately: the provider's next sync fails authentication, which is the
// intended way to cut off a system that should no longer be provisioning.
//
// Unlike invitation revocation, an id naming no live token is a 404 rather
// than a silent success — the contract reserves one, and an administrator
// cutting off a credential needs to know when they named the wrong one.
func (s *apiServer) RevokeScimToken(w http.ResponseWriter, r *http.Request, tokenID uuid.UUID) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	err := store.RevokeScimToken(r.Context(), tokenID)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeScimTokenNotFound, "no such provisioning token")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{Action: "scim.token.revoked", TargetID: tokenID})
	w.WriteHeader(http.StatusNoContent)
}

// apiScimToken maps a stored token onto the table's row shape. The token
// value is not among the fields: it exists in the creation response and
// nowhere else.
func apiScimToken(tok storage.ScimToken) api.ScimToken {
	out := api.ScimToken{
		Id: tok.ID,
		CreatedBy: api.UserSummary{
			Id:          tok.CreatedBy.ID,
			Username:    tok.CreatedBy.Username,
			DisplayName: tok.CreatedBy.DisplayName,
		},
		CreatedAt:  tok.CreatedAt,
		LastUsedAt: tok.LastUsedAt,
	}
	if tok.Note != "" {
		note := tok.Note
		out.Note = &note
	}
	return out
}
