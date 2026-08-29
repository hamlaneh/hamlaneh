package httpserver

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Conference rooms (ADR 005): a link that lets somebody with no account on
// this instance into one media room. It is the widest door in the product and
// it landed last, after every gate it must respect was built and tested.
//
// Four properties this file is responsible for. Each is a rule, not a habit:
//
//   - **Unknown, expired and revoked are one answer.** Both public routes
//     resolve a presented link through liveConference and refuse through
//     writeConferenceNotFound, and the three failures are already collapsed
//     into ErrNotFound by the query (storage/conferences.go) — so they cost
//     the same work as well as reading the same. A visitor learns whether
//     their link works, never why it does not.
//   - **A guest becomes a member of nothing.** The join mints a ticket and
//     writes nothing: no session, no cookie, no account created or read, and
//     no other endpoint in this server honours the link. An instance with
//     registration closed stays exactly as closed.
//   - **Revocation ends the meeting.** The room is closed before the link is
//     killed, and a close that fails is reported rather than papered over. A
//     revocation that let the current meeting run on would not be one.
//   - **Refusing to revoke is the same 404 as not existing.** A caller who is
//     neither the owner nor an administrator learns nothing, because a
//     distinct refusal would confirm the conference is there.
//
// Conference creation, revocation and guest joins are audited — unlike
// channel call activity, which is not. These are the instance's only
// anonymous-access events, and they are what an administrator investigating
// abuse needs.

const (
	// maxConferenceTitleLen is the contract's bound on
	// CreateConferenceRequest.title, and the column's own CHECK.
	maxConferenceTitleLen = 120
	// The contract's bounds on JoinConferenceRequest.display_name. It is not
	// the account display-name bound: this name is never an account's, it is
	// stored nowhere, and the contract states its own.
	maxGuestNameLen = 64

	// conferencePagePrefix is the webapp's unauthenticated join screen, and
	// the token is a PATH segment on it — unlike an invitation's, which rides
	// in the fragment so it never reaches a server.
	//
	// The difference is deliberate and it is the webapp's route that decides
	// it (webapp.go): this is not a credential emailed to one person, it is a
	// standing link pasted into a calendar entry, and it has to survive being
	// handled as an ordinary URL. What it costs is that the raw token appears
	// in this instance's own access log — a cost the contract already accepts
	// on the two /api/v1/meet/{token} routes, which take it in the path as
	// well, and which the invitation pair pays too on redemption.
	conferencePagePrefix = "/meet/"
)

// ListConferences returns the conferences the caller may see: their own, or
// every one on the instance if they administer it — an administrator must be
// able to find what they may revoke.
func (s *apiServer) ListConferences(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("list conferences reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	// Scope is a permission question, so it goes through the choke point
	// rather than reading is_admin here.
	all := authz.Can(r.Context(), &prin.user, authz.ConferenceListAll, nil)
	conferences, err := store.ListConferences(r.Context(), prin.user.ID, all)
	if err != nil {
		internalError(w, r, err)
		return
	}

	active, err := s.activeConferences(r, conferences)
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.ConferencePage{Conferences: make([]api.Conference, 0, len(conferences))}
	for _, conf := range conferences {
		page.Conferences = append(page.Conferences, apiConference(conf, active[conf.ID]))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// CreateConference mints a conference link and answers with it once. Only the
// digest is stored, exactly as invitation links and provisioning tokens are,
// so a stolen database yields nothing presentable and nothing can redisplay a
// link somebody closed the dialog on.
//
// The link does not expire unless the caller asks it to (ADR 005): the
// dominant use is a standing weekly meeting, and a link that dies unannounced
// drives a fresh one per meeting, pasted into more places than the last. It
// is always revocable, always visible to an administrator, and its creation
// and revocation are audited.
func (s *apiServer) CreateConference(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("create conference reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	if !s.calls.Enabled() {
		// A link to a room this instance cannot open is worse than a refusal.
		// Saying so to a signed-in caller leaks nothing: GET /api/v1/instance
		// reports the same capability to anybody.
		writeError(w, r, http.StatusServiceUnavailable, codeCallsUnavailable,
			"calls are not configured on this instance")
		return
	}

	var req api.CreateConferenceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	title, expiresAt, valid := conferenceCreation(w, r, req)
	if !valid {
		return
	}

	raw, tokenHash := session.NewToken()
	conf, err := store.CreateConference(r.Context(), prin.user.ID, tokenHash, title, expiresAt)
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "conference.created",
		TargetID:    conf.ID,
		TargetLabel: title,
		Detail:      map[string]any{"expires_at": conf.ExpiresAt},
	})
	// Nobody can be in a room that did not exist a moment ago, so `active` is
	// false without asking the media server.
	writeJSONValue(w, r, http.StatusCreated, api.CreatedConference{
		Conference: apiConference(conf, false),
		Url:        s.publicURL + conferencePagePrefix + raw,
	})
}

// RevokeConference kills a link and the meeting behind it.
//
// The room is closed FIRST and the link killed second, which is the order the
// two failure modes decide. Closing then failing to revoke leaves a live link
// to an ended meeting and answers 500, so the caller's retry works. Revoking
// then failing to close would leave a dead link over a meeting still running
// — the exact thing this endpoint exists to prevent — and the retry would
// then find nothing to revoke and answer 404.
//
// What makes the closing stick is not this handler but `auto_create: false`
// in the deploy stack. LiveKit tokens are stateless: DeleteRoom invalidates
// none of the tickets already out, and a participant who is connected when
// the room dies holds a rolling ten-minute token their client reconnects
// with. Were a join able to instantiate a room, the guest an owner revokes
// would be disconnected once, reconnect, rebuild the room and stay for as
// long as they kept reconnecting. Because only this server may create a room,
// their ticket now names nothing and the join is refused.
//
// So the exposure is the re-entry window, not the gap between these two
// calls. What remains is a ticket minted before the revocation and not yet
// used, which is dead the moment the room is: bounded by the room's absence
// rather than by the ticket's two minutes.
func (s *apiServer) RevokeConference(w http.ResponseWriter, r *http.Request, conferenceID uuid.UUID) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("revoke conference reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	conf, err := store.ConferenceByID(r.Context(), conferenceID)
	if errors.Is(err, storage.ErrNotFound) {
		writeConferenceNotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	if !authz.Can(r.Context(), &prin.user, authz.ConferenceRevoke,
		authz.NewConference(conf, prin.user.ID)) {
		// The same 404 a conference that does not exist gets. A 403 here
		// would confirm that one does.
		writeConferenceNotFound(w, r)
		return
	}

	if closeErr := s.calls.CloseConference(r.Context(), conferenceID); closeErr != nil {
		internalError(w, r, closeErr)
		return
	}
	err = store.RevokeConference(r.Context(), conferenceID)
	if errors.Is(err, storage.ErrNotFound) {
		// It was live a moment ago and is not now: somebody else revoked it
		// between the two calls. Same answer.
		writeConferenceNotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "conference.revoked",
		TargetID:    conferenceID,
		TargetLabel: conf.Title,
	})
	w.WriteHeader(http.StatusNoContent)
}

// PreviewConference answers what the join screen draws before anybody has an
// account: the title, and whether anybody is in there. Holding the link is
// the entitlement to know that much, and deliberately no more — not who is in
// the room, and not who made it.
func (s *apiServer) PreviewConference(w http.ResponseWriter, r *http.Request, token string) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	conf, ok := s.liveConference(w, r, store, token)
	if !ok {
		return
	}

	active, err := s.activeConferences(r, []storage.Conference{conf})
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.ConferencePreview{
		Title:  conf.Title,
		Active: active[conf.ID],
	})
}

// JoinConference mints a join ticket for this conference's room and nothing
// else.
//
// No session is created, no cookie is set, no account is created or read, and
// no other endpoint honours the link. Somebody with no account on this
// instance may join — that is what a conference link is for — and an instance
// with registration closed stays exactly as closed, because what the guest
// holds is a ticket to one room rather than a way in.
//
// The link is resolved BEFORE the instance's media configuration is
// consulted, so a caller guessing at links gets the same 404 whatever this
// instance is running. It is the ordering the channel token endpoint uses,
// for the same reason.
func (s *apiServer) JoinConference(w http.ResponseWriter, r *http.Request, token string) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.JoinConferenceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	conf, ok := s.liveConference(w, r, store, token)
	if !ok {
		return
	}
	if !s.calls.Enabled() {
		writeError(w, r, http.StatusServiceUnavailable, codeCallsUnavailable,
			"calls are not configured on this instance")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if msg := guestNameRefusal(displayName); msg != "" {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, msg)
		return
	}

	ticket, err := s.calls.GuestToken(r.Context(), conf.ID, displayName)
	if err != nil {
		internalError(w, r, err)
		return
	}

	// No actor: the route is public and no session exists, so record fills in
	// the client address and nothing else. That address, the conference, and
	// the name the guest chose are exactly what an administrator
	// investigating abuse has to work with — this is the instance's only
	// anonymous-access event, which is why it is audited when channel call
	// activity is not (ADR 005).
	s.record(r, AuditEvent{
		Action:      "conference.joined",
		TargetID:    conf.ID,
		TargetLabel: conf.Title,
		Detail:      map[string]any{"display_name": displayName},
	})
	// A ticket and nothing else. Nothing above set a cookie, and nothing
	// here adds a field the channel token endpoint does not have.
	writeJSONValue(w, r, http.StatusCreated, api.CallToken{
		Token:     ticket.Token,
		Room:      ticket.Room,
		ExpiresAt: ticket.ExpiresAt,
	})
}

// liveConference resolves a presented link and answers the contract's single
// 404 when it is anything other than live. It is the one call site both
// public conference routes go through, which is what makes "unknown, expired
// and revoked answer identically" a property of the code rather than of two
// handlers that happen to agree today.
//
// There is no shape check before the lookup, deliberately. One would save a
// query on obvious nonsense, and it would also give a malformed token a
// measurably faster refusal than a well-formed one. The lookup is a single
// indexed probe against a digest, the public routes are rate limited per
// address, and one uniform path for every presented value is worth more than
// the query it would save.
func (s *apiServer) liveConference(w http.ResponseWriter, r *http.Request, store Store,
	token string,
) (storage.Conference, bool) {
	conf, err := store.LiveConferenceByTokenHash(r.Context(), session.HashToken(token))
	if errors.Is(err, storage.ErrNotFound) {
		writeConferenceNotFound(w, r)
		return storage.Conference{}, false
	}
	if err != nil {
		internalError(w, r, err)
		return storage.Conference{}, false
	}
	return conf, true
}

// writeConferenceNotFound is the single source of every refusal a conference
// can get — a presented link that is not live, an id that names nothing, and
// an id the caller may not act on. One call site is what keeps them
// byte-identical.
func writeConferenceNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeConferenceNotFound, "no such conference")
}

// activeConferences asks the media server which of these conferences somebody
// is in right now.
//
// A failed read is an error rather than a page of `active: false`: whether a
// meeting is running is the one live fact these views carry, and "I could not
// ask" must not be served as "nobody is there".
func (s *apiServer) activeConferences(r *http.Request, conferences []storage.Conference) (map[uuid.UUID]bool, error) {
	ids := make([]uuid.UUID, 0, len(conferences))
	for _, conf := range conferences {
		ids = append(ids, conf.ID)
	}
	return s.calls.ActiveConferences(r.Context(), ids)
}

// conferenceCreation enforces every CreateConferenceRequest bound.
//
// An expiry already in the past is refused rather than stored: it would mint
// a link that never worked, which cannot be what the caller meant, and it
// would sit in their list looking live.
func conferenceCreation(w http.ResponseWriter, r *http.Request, req api.CreateConferenceRequest) (
	title string, expiresAt *time.Time, valid bool,
) {
	if req.Title != nil {
		title = strings.TrimSpace(*req.Title)
		if utf8.RuneCountInString(title) > maxConferenceTitleLen {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"title must be at most 120 characters")
			return "", nil, false
		}
		if !storableLine(title) {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"title carries characters that cannot be stored")
			return "", nil, false
		}
	}
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(time.Now()) {
			writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
				"expires_at must be in the future")
			return "", nil, false
		}
		expiresAt = req.ExpiresAt
	}
	return title, expiresAt, true
}

// guestNameRefusal reports why a guest's chosen name cannot be accepted, or
// "" if it can.
//
// It is not displayNameRefusal: that one is bounded by the users table's
// CHECK because every name it guards ends up in that column, and this name
// ends up in no column at all — it goes into the ticket the media server
// reads and into the audit entry for the join. The contract states its own
// bounds, and they are what this enforces.
//
// What it deliberately does not do is verify anything. A guest can present as
// anyone, that is inherent to anonymous meetings, and ADR 005 names it rather
// than pretending a check here would help.
func guestNameRefusal(name string) string {
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return "display_name must not be empty"
	case n > maxGuestNameLen:
		return "display_name must be at most 64 characters"
	}
	// The same storability rule every other line of client text goes through.
	// This one is never stored, but it is written into an audit entry and
	// read by a person, and a name carrying control characters can only
	// garble what they read.
	if !storableLine(name) {
		return "display_name carries characters that cannot be stored"
	}
	return ""
}

// apiConference maps a stored conference onto the contract's shape. The link
// is not among the fields: it exists in the creation response and nowhere
// else.
func apiConference(conf storage.Conference, active bool) api.Conference {
	out := api.Conference{
		Id:        conf.ID,
		Title:     conf.Title,
		CreatedAt: conf.CreatedAt,
		ExpiresAt: conf.ExpiresAt,
		Active:    active,
	}
	if conf.CreatedBy != nil {
		out.CreatedBy = &api.UserSummary{
			Id:          conf.CreatedBy.ID,
			Username:    conf.CreatedBy.Username,
			DisplayName: conf.CreatedBy.DisplayName,
		}
	}
	return out
}
