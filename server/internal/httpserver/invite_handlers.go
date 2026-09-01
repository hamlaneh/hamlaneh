package httpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Contract bounds for invitations (openapi.yaml CreateInviteRequest,
// RedeemInviteRequest). The redemption bounds are deliberately narrower than
// the account policy in internal/uservalidate — the contract states them,
// and a request outside them is a 400 rather than something the database
// decides.
const (
	defaultInviteHours    = 168
	maxInviteHours        = 720
	maxInviteNoteLen      = 200
	maxInvitePasswordLen  = 200
	minInviteTokenLen     = 20
	maxInviteTokenLen     = 128
	inviteRedemptionPath  = "/invite"
	inviteTokenFragmentKV = "token=" // #nosec G101 -- a URL fragment key, not a credential
)

// ListInvites returns one page of invitations that can still be redeemed,
// soonest expiry first. Spent and expired invitations leave the list on
// purpose: the table's question is what is still live, and the audit log is
// where the history lives.
func (s *apiServer) ListInvites(w http.ResponseWriter, r *http.Request, params api.ListInvitesParams) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	limit := defaultListLimit
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > maxListLimit {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "limit must be between 1 and 100")
			return
		}
		limit = *params.Limit
	}

	var after *storage.InviteCursor
	if params.Cursor != nil {
		cursor, err := decodeInviteCursor(*params.Cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "invalid pagination cursor")
			return
		}
		after = cursor
	}

	// One row beyond the page, to learn whether a next page exists.
	invites, err := store.ListOpenInvites(r.Context(), storage.ListInvitesParams{After: after, Limit: limit + 1})
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.InvitePage{Invites: make([]api.Invite, 0, min(len(invites), limit))}
	if len(invites) > limit {
		invites = invites[:limit]
		next := encodeInviteCursor(invites[len(invites)-1])
		page.NextCursor = &next
	}
	for _, inv := range invites {
		page.Invites = append(page.Invites, apiInvite(inv))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// CreateInvite mints a single-use invitation and answers with the link once.
// Only the token's SHA-256 digest is stored, exactly as password-reset
// tokens are, so a stolen database yields no usable invitation and nothing
// can redisplay a link somebody closed the dialog on.
func (s *apiServer) CreateInvite(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("create invite reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.CreateInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	hours := defaultInviteHours
	if req.ExpiresInHours != nil {
		if *req.ExpiresInHours < 1 || *req.ExpiresInHours > maxInviteHours {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"expires_in_hours must be between 1 and 720")
			return
		}
		hours = *req.ExpiresInHours
	}
	var note string
	if req.Note != nil {
		note = strings.TrimSpace(*req.Note)
		if utf8.RuneCountInString(note) > maxInviteNoteLen {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"note must be at most 200 characters")
			return
		}
	}

	raw, tokenHash := session.NewToken()
	invite, err := store.CreateInvite(r.Context(), prin.user.ID, tokenHash, note,
		time.Duration(hours)*time.Hour)
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "invite.created",
		TargetID:    invite.ID,
		TargetLabel: note,
		Detail:      map[string]any{"expires_at": invite.ExpiresAt},
	})
	writeJSONValue(w, r, http.StatusCreated, api.CreatedInvite{
		Id:        invite.ID,
		Url:       s.inviteURL(raw),
		ExpiresAt: invite.ExpiresAt,
	})
}

// RevokeInvite closes an open invitation. It is idempotent — revoking one
// that is already gone answers 204, because the outcome the caller wanted is
// the outcome that holds — so it deliberately reports nothing about whether
// the id named anything.
func (s *apiServer) RevokeInvite(w http.ResponseWriter, r *http.Request, inviteID api.InviteId) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	if err := store.RevokeInvite(r.Context(), inviteID); err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{Action: "invite.revoked", TargetID: inviteID})
	w.WriteHeader(http.StatusNoContent)
}

// PreviewInvite answers what the redemption screen draws before anybody has
// an account: the instance's name, and nothing about who issued the
// invitation or for whom.
//
// Public by necessity — the screen runs before a session exists. An unknown,
// expired, revoked or already-used token answers the same 404, so a guessed
// token cannot be told from a spent one.
func (s *apiServer) PreviewInvite(w http.ResponseWriter, r *http.Request, token string) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	if !s.inviteIsOpen(w, r, store, token) {
		return
	}

	settings, err := store.OrgSettings(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.InvitePreview{OrgName: settings.OrgName})
}

// RedeemInvite creates an account from an invitation. The invitation is
// consumed in the same transaction that creates the account, so a link
// cannot make two people: of two callers racing one token, one gets an
// account and the other gets the same 404 a spent link always gets.
//
// registration_mode is deliberately not consulted. An invitation is a
// capability somebody with authority handed out; whether the instance also
// accepts self-registration is a different question with a different door,
// and closing registration must not retroactively void links already sent.
func (s *apiServer) RedeemInvite(w http.ResponseWriter, r *http.Request, token string) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.RedeemInviteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.inviteIsOpen(w, r, store, token) {
		return
	}

	// Read before the account is created: a new account starts in the
	// instance's default locale, which is what that setting means.
	settings, err := store.OrgSettings(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}

	nu, valid := validateRedemption(w, r, req, settings.DefaultLocale)
	if !valid {
		return
	}
	nu.PasswordHash = password.Hash(req.Password)

	created, err := store.RedeemInvite(r.Context(), session.HashToken(token), nu)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// The token was live a moment ago and is not now: somebody else won
		// the race, or it was revoked between the two calls. Same answer.
		writeInviteNotFound(w, r)
		return
	case errors.Is(err, storage.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, codeUsernameTaken, "username is already taken")
		return
	case errors.Is(err, storage.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, codeEmailTaken, "email is already taken")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	// No actor: the route is public, so there is no signed-in principal for
	// record to fill in, and the account that redeemed the link did not
	// exist when the request started. It is the target, not the actor.
	s.record(r, AuditEvent{
		Action:      "invite.redeemed",
		TargetID:    created.ID,
		TargetLabel: created.Username,
	})
	writeJSONValue(w, r, http.StatusCreated, api.UserSummary{
		Id:          created.ID,
		Username:    created.Username,
		DisplayName: created.DisplayName,
	})
}

// inviteIsOpen resolves a presented token and answers the contract's single
// 404 when it is anything other than live. It is the one call site both
// public invite routes go through, which is what makes "unknown, expired,
// revoked and used answer identically" a property of the code rather than
// two handlers that happen to agree today.
//
// The length bounds are checked here too, and answer the same 404 rather
// than a 400: a token that is too short is a guess, and telling a guesser
// that their guess was malformed rather than wrong is a distinction worth
// nothing to them and something to an attacker.
func (s *apiServer) inviteIsOpen(w http.ResponseWriter, r *http.Request, store Store, token string) bool {
	if n := len(token); n < minInviteTokenLen || n > maxInviteTokenLen {
		writeInviteNotFound(w, r)
		return false
	}
	_, err := store.OpenInviteByTokenHash(r.Context(), session.HashToken(token))
	if errors.Is(err, storage.ErrNotFound) {
		writeInviteNotFound(w, r)
		return false
	}
	if err != nil {
		internalError(w, r, err)
		return false
	}
	return true
}

// writeInviteNotFound is the single source of every refusal a presented
// invite token can get. One call site is what keeps the four failures
// byte-identical.
func writeInviteNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeInviteNotFound, "no such invitation")
}

// validateRedemption enforces every RedeemInviteRequest bound and maps the
// request onto a storage.NewUser (password hash still unset).
//
// The account never starts owing a password change: the person redeeming
// chose this password themselves, so there is nothing for them to replace.
func validateRedemption(w http.ResponseWriter, r *http.Request, req api.RedeemInviteRequest, locale string) (storage.NewUser, bool) {
	fail := func(message string) (storage.NewUser, bool) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, message)
		return storage.NewUser{}, false
	}

	if err := uservalidate.Username(req.Username); err != nil {
		return fail("username " + err.Error())
	}
	if err := uservalidate.Password(req.Password); err != nil {
		return fail("password " + err.Error())
	}
	if utf8.RuneCountInString(req.Password) > maxInvitePasswordLen {
		return fail("password must be 12 to 200 characters")
	}

	nu := storage.NewUser{Username: req.Username, Locale: locale}
	if req.DisplayName != nil {
		if msg := displayNameRefusal(*req.DisplayName); msg != "" {
			return fail(msg)
		}
		nu.DisplayName = *req.DisplayName
	}
	return nu, true
}

// inviteURL builds the link the dashboard shows once. The token rides in the
// URL fragment, exactly as the emailed reset link does and for the same
// reason: a fragment is never sent to a server, so the link cannot land in
// an access log or a Referer header on its way to the redemption screen.
//
// With no configured public origin the link is site-relative. That is the
// honest answer for an install that was never told its own origin — the
// admin's browser still resolves it — and it is never a guess at a Host
// header the client controls.
func (s *apiServer) inviteURL(token string) string {
	return s.publicURL + inviteRedemptionPath + "#" + inviteTokenFragmentKV + token
}

// apiInvite maps a stored invitation onto the table's row shape. The link is
// not among the fields: it exists in the creation response and nowhere else.
func apiInvite(inv storage.Invite) api.Invite {
	out := api.Invite{
		Id: inv.ID,
		CreatedBy: api.UserSummary{
			Id:          inv.CreatedBy.ID,
			Username:    inv.CreatedBy.Username,
			DisplayName: inv.CreatedBy.DisplayName,
		},
		CreatedAt: inv.CreatedAt,
		ExpiresAt: inv.ExpiresAt,
	}
	if inv.Note != "" {
		note := inv.Note
		out.Note = &note
	}
	return out
}

// Invite cursors encode the keyset position (expires_at, id) of the last row
// of a page as base64url("RFC3339Nano|uuid") — the same shape user cursors
// use, and for the same reason: RFC3339Nano preserves PostgreSQL's
// microsecond precision exactly, so the cursor round-trips.
func encodeInviteCursor(inv storage.Invite) string {
	raw := inv.ExpiresAt.UTC().Format(time.RFC3339Nano) + "|" + inv.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeInviteCursor(encoded string) (*storage.InviteCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode cursor: %w", err)
	}
	expiresPart, idPart, found := strings.Cut(string(raw), "|")
	if !found {
		return nil, errors.New("decode cursor: missing separator")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresPart)
	if err != nil {
		return nil, fmt.Errorf("decode cursor timestamp: %w", err)
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return nil, fmt.Errorf("decode cursor id: %w", err)
	}
	return &storage.InviteCursor{ExpiresAt: expiresAt, ID: id}, nil
}
