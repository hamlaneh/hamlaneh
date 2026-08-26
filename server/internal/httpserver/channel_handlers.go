package httpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	openapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Channels, membership and direct messages — the conversation half of
// slice 1.2a. Message history and read positions live in
// message_handlers.go and share everything below.
//
// Three rules shape every handler in both files.
//
// A non-member gets 404, never 403, on every channel-scoped path — an org
// admin who is not a member included. A channel's existence is itself
// privileged, so a refusal must not distinguish "not yours" from "not
// there" (openapi.yaml components.responses.NotFound). The one 403 in this
// file is removeChannelMember's, and it is only ever shown to somebody who
// can already see the channel.
//
// Every resource-level decision goes through authz.Can (CLAUDE.md security
// non-negotiable). A handler loads the facts the rules turn on — the
// channel, whether the caller is a member of it — and asks; it never
// decides for itself. resolveChannel gathers those facts and
// channelScope.deny turns a refusal into the right status.
//
// A direct message's fixed membership and its lack of a topic are 400s, not
// 403s. They are statements about the channel's shape rather than about who
// is asking, and answering 403 would tell a member they lack a permission
// that does not exist.

// Contract bounds for the conversation surface (openapi.yaml). Message
// bounds live in message_handlers.go; the shared 50/100 page bounds are
// defaultListLimit and maxListLimit in user_handlers.go.
const (
	maxChannelPageLimit = 200
	maxTopicLen         = 250
	minSlugLen          = 2
	maxSlugLen          = 64
)

// codeLastMember answers removeChannelMember's refusal to empty a channel.
// It is the one error code written from a single handler and declared beside
// it rather than in errors.go, because nothing else in the API can produce
// it.
const codeLastMember errorCode = "last_member"

// slugPattern is the contract's channel-name shape: a lowercase
// alphanumeric start, then lowercase alphanumerics, underscore or dash.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// channelScope is one channel-scoped request resolved: who is asking, the
// store to act on, and the channel as that caller sees it.
type channelScope struct {
	prin    principal
	store   Store
	channel storage.Channel
	// member is read from the database for this request and never carried
	// over from an earlier one: membership changes while a client is
	// connected, and a stale copy is an authorization bug.
	member bool
}

// resolveChannel gathers everything a channel-scoped request needs before it
// can be decided: the principal, the store, the channel as the caller sees
// it, and the caller's membership.
//
// The channel is read through ChannelForUser rather than by id because the
// contract makes unread_count and mention_count required on every Channel
// and only a caller-scoped read computes them.
//
// It answers 404 for a channel that does not exist and 500 for a failure,
// reporting false in both cases. It decides no permission — that is the
// handler's own explicit authz.Can call.
func (s *apiServer) resolveChannel(w http.ResponseWriter, r *http.Request, channelID uuid.UUID) (channelScope, bool) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("channel request reached without principal"))
		return channelScope{}, false
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return channelScope{}, false
	}

	ch, err := store.ChannelForUser(r.Context(), channelID, prin.user.ID)
	if errors.Is(err, storage.ErrChannelNotFound) {
		writeChannelNotFound(w, r)
		return channelScope{}, false
	}
	if err != nil {
		internalError(w, r, err)
		return channelScope{}, false
	}

	member, err := store.IsChannelMember(r.Context(), channelID, prin.user.ID)
	if err != nil {
		internalError(w, r, err)
		return channelScope{}, false
	}
	return channelScope{prin: prin, store: store, channel: ch, member: member}, true
}

// resource is the authz resource for this request.
func (sc channelScope) resource() authz.Channel {
	return authz.NewChannel(sc.channel, sc.prin.user.ID, sc.member)
}

// deny answers an action authz refused. A non-member gets the channel's
// 404: its existence never leaks, and an org admin who is not a member sees
// exactly what a stranger sees. A member who simply lacks this one power
// gets 403 — they can already see the channel, so there is nothing left to
// hide from them.
func (sc channelScope) deny(w http.ResponseWriter, r *http.Request) {
	if !sc.member {
		writeChannelNotFound(w, r)
		return
	}
	writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
}

// writeChannelNotFound is the single answer to "no such channel for this
// caller": one call site for the unknown channel and for the channel the
// caller is not in, so the two can never drift into distinguishable
// responses.
func writeChannelNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeChannelNotFound, "no such channel")
}

// ListChannels returns the caller's sidebar: every channel and direct
// message they belong to, each carrying their own unread and mention
// counts.
//
// There is no authz.Can call here because there is no resource to decide
// about: the scope IS the query. ListChannelsForUser joins channel_members,
// so a channel the caller is not in cannot appear in the result at all —
// the membership rule is enforced in SQL rather than filtered afterwards,
// which is the shape a forgotten branch cannot leak through.
func (s *apiServer) ListChannels(w http.ResponseWriter, r *http.Request, params api.ListChannelsParams) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("list channels reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	limit, ok := pageLimit(w, r, params.Limit, maxChannelPageLimit, maxChannelPageLimit)
	if !ok {
		return
	}
	after, ok := channelCursor(w, r, params.Cursor)
	if !ok {
		return
	}

	// Fetch one row beyond the page to learn whether a next page exists.
	channels, err := store.ListChannelsForUser(r.Context(), prin.user.ID,
		storage.ListChannelsParams{After: after, Limit: limit + 1})
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.ChannelPage{Channels: make([]api.Channel, 0, min(len(channels), limit))}
	if len(channels) > limit {
		channels = channels[:limit]
		last := channels[len(channels)-1]
		next := encodeTimeCursor(last.CreatedAt, last.ID)
		page.NextCursor = &next
	}
	for _, ch := range channels {
		page.Channels = append(page.Channels, apiChannel(ch))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// CreateChannel creates a public or private channel with its creator as the
// sole member. Direct messages are opened through OpenDirectMessage — only
// that path can canonicalize a pair — so kind dm is refused here.
func (s *apiServer) CreateChannel(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("create channel reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.CreateChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	nc, ok := validateCreateChannel(w, r, req, prin.user.ID)
	if !ok {
		return
	}

	ch, err := store.CreateChannel(r.Context(), nc)
	switch {
	case errors.Is(err, storage.ErrChannelSlugTaken):
		writeError(w, r, http.StatusConflict, codeChannelSlugTaken, "channel name is already taken")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	// Announced after the insert committed, and only to the creator: nobody
	// else is in it yet. Broadcasting a caller-scoped row is safe here for
	// the reason it is safe almost nowhere else — a channel created a moment
	// ago has nothing unread in it, so its counts are zero for everyone.
	s.realtime.ChannelCreated([]uuid.UUID{prin.user.ID}, ch)
	writeJSONValue(w, r, http.StatusCreated, apiChannel(ch))
}

// validateCreateChannel enforces every CreateChannelRequest contract
// constraint and maps the request onto a storage.NewChannel. On a violation
// it answers 400 and reports false. The generated code produces models
// only, so a bound not checked here is not checked at all.
func validateCreateChannel(w http.ResponseWriter, r *http.Request, req api.CreateChannelRequest, creator uuid.UUID) (storage.NewChannel, bool) {
	fail := func(message string) (storage.NewChannel, bool) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, message)
		return storage.NewChannel{}, false
	}

	if n := utf8.RuneCountInString(req.Slug); n < minSlugLen || n > maxSlugLen {
		return fail(fmt.Sprintf("slug must be %d to %d characters", minSlugLen, maxSlugLen))
	}
	if !slugPattern.MatchString(req.Slug) {
		return fail("slug may contain lowercase letters, digits, '_' and '-', and must start with a letter or digit")
	}

	kind := storage.ChannelKind(req.Kind)
	if kind != storage.ChannelKindPublic && kind != storage.ChannelKindPrivate {
		return fail("kind must be one of: public, private")
	}

	nc := storage.NewChannel{Kind: kind, Slug: req.Slug, CreatedBy: creator}
	if req.Topic != nil {
		if utf8.RuneCountInString(*req.Topic) > maxTopicLen {
			return fail(fmt.Sprintf("topic must be at most %d characters", maxTopicLen))
		}
		nc.Topic = *req.Topic
	}
	return nc, true
}

// OpenDirectMessage opens — or reuses — the 1:1 direct message with one
// user. Exactly one channel exists per pair (ADR 001), so this is
// idempotent: 201 reports one that was just created, 200 the one that was
// already there.
func (s *apiServer) OpenDirectMessage(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("open direct message reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	var req api.OpenDirectMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UserId == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "user_id is required")
		return
	}

	// The caller comes from the session, never from the body: a DM is opened
	// between whoever is asking and whoever they named, and no request can
	// select a pair it is not part of.
	ch, created, err := store.OpenDirectMessage(r.Context(), prin.user.ID, req.UserId)
	switch {
	case errors.Is(err, storage.ErrDMWithSelf):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "a direct message needs two different users")
		return
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeUserNotFound, "no such user")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
		// Both participants can see it from now on — and each is told with the
		// row as THEY see it, not with one shared copy.
		//
		// dm_peer is what makes the difference: a direct message has no slug,
		// so dm_peer is the only thing that names it, and it is caller-scoped
		// by construction — a row read for one participant says the other one
		// is "the other one". Announcing the caller's row to both would leave
		// the peer's sidebar with a conversation named after the peer
		// themselves. The counts are caller-scoped for the same reason, and
		// happen to be zero on a channel opened a moment ago.
		s.realtime.ChannelCreated([]uuid.UUID{prin.user.ID}, ch)
		announceInvitation(r, store, s.realtime, ch.ID, req.UserId)
	}
	writeJSONValue(w, r, status, apiChannel(ch))
}

// GetChannel returns one channel or direct message the caller belongs to —
// a deep link into a conversation, and a reload of one.
func (s *apiServer) GetChannel(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelRead, sc.resource()) {
		sc.deny(w, r)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiChannel(sc.channel))
}

// UpdateChannel sets a channel's topic; any member may. Renaming is not in
// this slice, and a direct message has no topic to set.
func (s *apiServer) UpdateChannel(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelUpdate, sc.resource()) {
		sc.deny(w, r)
		return
	}

	var req api.UpdateChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if utf8.RuneCountInString(req.Topic) > maxTopicLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("topic must be at most %d characters", maxTopicLen))
		return
	}

	updated, err := sc.store.UpdateChannelTopic(r.Context(), channelID, req.Topic)
	switch {
	case errors.Is(err, storage.ErrDMHasNoTopic):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "a direct message has no topic")
		return
	case errors.Is(err, storage.ErrChannelNotFound):
		writeChannelNotFound(w, r)
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	// The broadcast deliberately carries the caller-less row the update
	// returned rather than the one served below: this event reaches every
	// member, and a caller-scoped row would ship the actor's unread and
	// mention counts to all of them. The contract carries no cross-user read
	// state anywhere, and a topic change must not be the first place it
	// leaks.
	s.realtime.ChannelUpdated(channelID, updated)

	// That same row is useless as an answer, for the same reason: its
	// caller-scoped counts are zeros nobody computed. Re-read the channel as
	// the caller sees it (see UpdateChannelTopic's doc comment).
	ch, err := sc.store.ChannelForUser(r.Context(), channelID, sc.prin.user.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, apiChannel(ch))
}

// ListChannelMembers returns one page of a channel's members in username
// order.
func (s *apiServer) ListChannelMembers(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, params api.ListChannelMembersParams) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelRead, sc.resource()) {
		sc.deny(w, r)
		return
	}

	limit, ok := pageLimit(w, r, params.Limit, defaultListLimit, maxListLimit)
	if !ok {
		return
	}
	var after *storage.ChannelMemberCursor
	if params.Cursor != nil {
		username, id, err := decodeCursor(*params.Cursor)
		if err != nil {
			writeInvalidCursor(w, r)
			return
		}
		after = &storage.ChannelMemberCursor{Username: username, UserID: id}
	}

	// One row beyond the page tells us whether a next page exists.
	members, err := sc.store.ListChannelMembers(r.Context(), channelID,
		storage.ListChannelMembersParams{After: after, Limit: limit + 1})
	if err != nil {
		internalError(w, r, err)
		return
	}

	page := api.MemberPage{Members: make([]api.UserSummary, 0, min(len(members), limit))}
	if len(members) > limit {
		members = members[:limit]
		last := members[len(members)-1]
		next := encodeCursor(last.Username, last.ID)
		page.NextCursor = &next
	}
	for _, u := range members {
		page.Members = append(page.Members, apiUserSummary(u))
	}
	writeJSONValue(w, r, http.StatusOK, page)
}

// AddChannelMember invites a user into a channel; any member may invite
// anybody. It is idempotent — inviting somebody who is already a member
// changes nothing and still answers 204.
func (s *apiServer) AddChannelMember(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelMemberAdd, sc.resource()) {
		sc.deny(w, r)
		return
	}

	var req api.AddChannelMemberRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UserId == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "user_id is required")
		return
	}

	err := sc.store.AddChannelMember(r.Context(), channelID, req.UserId, sc.prin.user.ID)
	switch {
	case errors.Is(err, storage.ErrDMMembershipFixed):
		writeError(w, r, http.StatusBadRequest, codeDMMembershipFixed,
			"a direct message's membership is fixed at two")
		return
	case errors.Is(err, storage.ErrChannelNotFound):
		writeChannelNotFound(w, r)
		return
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeUserNotFound, "no such user")
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	// The invited person gets the channel itself first (ws-protocol.md §4:
	// channel_created is "you created it, or somebody invited you", and their
	// sidebar adds the row from it). member_added cannot do that job for them:
	// it names somebody joining a channel their client has never heard of, so
	// on its own an invitation was invisible until the next reload.
	//
	// The row is read as THEY see it, never as the inviter sees it. Every
	// count on a Channel is caller-scoped, and shipping the inviter's copy
	// would publish numbers nobody computed for the recipient.
	announceInvitation(r, sc.store, s.realtime, channelID, req.UserId)

	// Announced after the membership committed, and named: the event carries
	// a UserSummary, so the person has to be read back.
	if user, ok := namedMember(r.Context(), sc.store, channelID, req.UserId, "member_added"); ok {
		s.realtime.MemberAdded(channelID, user)
	}
	w.WriteHeader(http.StatusNoContent)
}

// announceInvitation tells one user that a channel is theirs to see now.
//
// A failed read costs the announcement and nothing else, for the reason
// namedMember gives: the membership is already committed, the socket is a
// fast path rather than a delivery guarantee, and the client reconciles its
// channel list from REST on every resume — so a missed event self-heals into
// "the row appears on the next reload", which is exactly where this path was
// before the event existed.
func announceInvitation(r *http.Request, store Store, rt Realtime, channelID, userID uuid.UUID) {
	ch, err := store.ChannelForUser(r.Context(), channelID, userID)
	if err != nil {
		slog.Error("invitation not announced", "channel", channelID, "user", userID, "error", err)
		return
	}
	rt.ChannelCreated([]uuid.UUID{userID}, ch)
}

// namedMember reads the person a membership event names, reporting false
// when the lookup failed.
//
// A failure costs the announcement and nothing else. The membership change
// is already committed, and the socket is a fast path rather than a delivery
// guarantee (realtime.go): clients reconcile membership from REST on every
// resume, so a missed event self-heals, while answering 500 here would
// report a write that did happen as an error and invite a retry of it. It is
// logged because a store that cannot read a user it just wrote to is worth
// knowing about.
func namedMember(ctx context.Context, store Store, channelID, userID uuid.UUID, event string) (storage.User, bool) {
	user, err := store.UserByID(ctx, userID)
	if err != nil {
		slog.Error("membership event not announced", "event", event,
			"channel", channelID, "user", userID, "error", err)
		return storage.User{}, false
	}
	return user, true
}

// RemoveChannelMember takes a user out of a channel — leaving it, or being
// removed from it. It is idempotent: removing somebody who is not a member
// answers 204.
//
// Emptying a channel is refused with 400 last_member. That refusal is not an
// authorization answer and deliberately does not read as one: it is decided
// after Can has already allowed the removal, it is the same answer for the
// last member leaving as for somebody removing them, and it says something
// about the channel rather than about the caller — the shape
// dm_membership_fixed already has.
func (s *apiServer) RemoveChannelMember(w http.ResponseWriter, r *http.Request, channelID api.ChannelId, userID openapitypes.UUID) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}

	// A direct message's membership is fixed at two, so removal is impossible
	// there for everybody in it — the creator, an admin member, and the plain
	// peer alike. Answering on the channel's shape before asking authz is
	// what stops the three of them getting three different stories about one
	// impossible operation: asked in the other order, the peer alone fails
	// Can and receives a 403 claiming they lack a permission that does not
	// exist in a DM.
	//
	// Non-members are refused first regardless: the shape of a channel they
	// cannot see is not something to tell them about.
	if !sc.member {
		sc.deny(w, r)
		return
	}
	if sc.channel.Kind == storage.ChannelKindDM {
		writeError(w, r, http.StatusBadRequest, codeDMMembershipFixed,
			"a direct message's membership is fixed at two")
		return
	}

	// Leaving is always allowed to a member, so it is asked as a plain read;
	// removing somebody else is the permission the creator and an admin
	// member hold. Both go through Can, so a non-member gets the channel's
	// 404 either way and never learns which of the two they were refused.
	action := authz.ChannelMemberRemove
	if userID == sc.prin.user.ID {
		action = authz.ChannelRead
	}
	if !authz.Can(r.Context(), &sc.prin.user, action, sc.resource()) {
		sc.deny(w, r)
		return
	}

	err := sc.store.RemoveChannelMember(r.Context(), channelID, userID)
	switch {
	case errors.Is(err, storage.ErrDMMembershipFixed):
		writeError(w, r, http.StatusBadRequest, codeDMMembershipFixed,
			"a direct message's membership is fixed at two")
		return
	case errors.Is(err, storage.ErrLastMember):
		writeError(w, r, http.StatusBadRequest, codeLastMember,
			"a channel cannot be left with no members")
		return
	case errors.Is(err, storage.ErrChannelNotFound):
		writeChannelNotFound(w, r)
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	// Two events, because the audiences are disjoint (ws-protocol.md §4).
	// The members who remain get MemberRemoved — the gateway reads that
	// audience from the membership table now that the removal has committed,
	// so the departing user is simply not in it. Their own sockets get
	// ChannelRemoved instead, which names nothing that is no longer their
	// business, and which needs no lookup to send.
	if user, ok := namedMember(r.Context(), sc.store, channelID, userID, "member_removed"); ok {
		s.realtime.MemberRemoved(channelID, user)
	}
	s.realtime.ChannelRemoved(userID, channelID)
	w.WriteHeader(http.StatusNoContent)
}

// apiChannel maps a storage channel onto the contract's Channel schema.
//
// unread_count, mention_count and last_read_message_id are the *caller's*
// view, and only a caller-scoped read fills them (ChannelForUser,
// ListChannelsForUser, CreateChannel, OpenDirectMessage). Handing this
// function a row from any other read would publish a zero nobody computed
// as though it were a fact.
//
// dm_peer is caller-scoped in the same way and for a sharper reason — see
// storage.Channel — so it is absent from exactly the same rows: a direct
// message read without a caller cannot say which of the two participants is
// "the other one". It is absent on every named channel too, which has a slug
// to draw instead. A storage.DMPeer carries the three fields of a
// UserSummary and no others, so there is no email, role or password state
// here to leave out.
func apiChannel(ch storage.Channel) api.Channel {
	out := api.Channel{
		Id:                ch.ID,
		Kind:              api.ChannelKind(ch.Kind),
		Slug:              ch.Slug,
		Topic:             ch.Topic,
		MemberCount:       ch.MemberCount,
		UnreadCount:       ch.UnreadCount,
		MentionCount:      ch.MentionCount,
		LastReadMessageId: ch.LastReadMessageID,
		LastMessageAt:     ch.LastMessageAt,
		CreatedBy:         ch.CreatedBy,
		CreatedAt:         ch.CreatedAt,
	}
	if peer := ch.DMPeer; peer != nil {
		out.DmPeer = &api.UserSummary{Id: peer.ID, Username: peer.Username, DisplayName: peer.DisplayName}
	}
	return out
}

// apiUserSummary maps a user onto the contract's public UserSummary: the
// name row the chat shell draws and nothing else — never email, role, or
// password state.
//
// Presence is left unset. It is a fact about live sockets that the realtime
// gateway owns, and the contract renders an absent presence rather than a
// guessed one.
func apiUserSummary(u storage.User) api.UserSummary {
	return api.UserSummary{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

// pageLimit resolves an optional limit query parameter against the
// contract's bounds. The generated code produces models only — it enforces
// neither minimum nor maximum — so a bound checked here is the only bound
// there is. On a violation it answers 400 and reports false.
func pageLimit(w http.ResponseWriter, r *http.Request, requested *int, def, maxLimit int) (int, bool) {
	if requested == nil {
		return def, true
	}
	if *requested < 1 || *requested > maxLimit {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("limit must be between 1 and %d", maxLimit))
		return 0, false
	}
	return *requested, true
}

// channelCursor decodes the sidebar's optional pagination cursor. On a
// malformed cursor it answers 400 and reports false.
func channelCursor(w http.ResponseWriter, r *http.Request, encoded *string) (*storage.ChannelCursor, bool) {
	if encoded == nil {
		return nil, true
	}
	createdAt, id, err := decodeTimeCursor(*encoded)
	if err != nil {
		writeInvalidCursor(w, r)
		return nil, false
	}
	return &storage.ChannelCursor{CreatedAt: createdAt, ID: id}, true
}

// writeInvalidCursor is the one answer to a cursor the server cannot
// decode. It says nothing about why: a cursor is opaque, and the only
// honest advice is to start the page again.
func writeInvalidCursor(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "invalid pagination cursor")
}

// Keyset cursors are opaque to the client and base64url("<key>|<uuid>")
// underneath — the shape the admin user list already uses. The key is the
// page's first sort column: a timestamp for the sidebar and for history, a
// username for a member list. Neither can contain "|" (the username pattern
// forbids it, RFC3339 has no use for it), so one separator is unambiguous.
func encodeCursor(key string, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key + "|" + id.String()))
}

func decodeCursor(encoded string) (string, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("decode cursor: %w", err)
	}
	key, idPart, found := strings.Cut(string(raw), "|")
	if !found {
		return "", uuid.Nil, errors.New("decode cursor: missing separator")
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("decode cursor id: %w", err)
	}
	return key, id, nil
}

// encodeTimeCursor and decodeTimeCursor carry a (created_at, id) keyset
// position. RFC3339Nano preserves PostgreSQL's microsecond precision
// exactly, so the position round-trips unchanged.
func encodeTimeCursor(t time.Time, id uuid.UUID) string {
	return encodeCursor(t.UTC().Format(time.RFC3339Nano), id)
}

func decodeTimeCursor(encoded string) (time.Time, uuid.UUID, error) {
	key, id, err := decodeCursor(encoded)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, key)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("decode cursor timestamp: %w", err)
	}
	return at, id, nil
}
