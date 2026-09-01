package httpserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
)

// Calls: the two channel endpoints, and the webhook receiver that keeps the
// three gateway events honest. docs/adr/005-calls-and-meetings.md is the
// design.
//
// Three properties this file is responsible for:
//
//   - **Live state comes from the media server, never from a table.** There
//     is no calls table and there must not be one: LiveKit's room state is
//     the truth, and a copy in Postgres would be a cache to invalidate for
//     nothing. Both handlers read through internal/calls.
//   - **A stranger to a channel gets the channel's 404, and learns nothing
//     else.** Membership is settled before the media plane is consulted and
//     before the instance's capability is reported, so a non-member cannot
//     tell a channel with no call from a channel they cannot see, and cannot
//     tell either from a 503 — the same rule every channel-scoped path in
//     this package follows, org admins included.
//   - **The webhook is a hint, never a fact.** Delivery is at-least-once and
//     unordered, so a verified delivery triggers a fresh read of live state
//     and the read decides what is announced. That is what makes the handler
//     idempotent and order-proof by construction rather than by bookkeeping.

// WithCalls wires the media plane. Omitting it — the zero-config install and
// any development server without the stack — leaves calls off: the instance
// document reports calls false, the token endpoint answers 503
// calls_unavailable, and GET .../call honestly answers that there is no call.
func WithCalls(svc *calls.Service) Option {
	return func(s *apiServer) { s.calls = svc }
}

// GetChannelCall answers what is happening in this channel's call.
//
// There is no 503 here and the contract reserves none: an instance with no
// media server has no call, which is exactly what `active: false` says. A
// client reconciling on channel open or after a reconnect gets the same
// well-formed answer either way, rather than an error it would have to
// special-case.
func (s *apiServer) GetChannelCall(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.ChannelRead, sc.resource()) {
		sc.deny(w, r)
		return
	}

	state, err := s.calls.State(r.Context(), channelID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, s.apiCall(r.Context(), state))
}

// CreateCallToken mints a join ticket for this channel's call.
//
// Membership is decided first and the media plane's availability second. Both
// orders are non-leaking, but this one keeps the rule the rest of the package
// states without exception: a channel-scoped path answers a stranger with the
// channel's 404 before it says anything at all about the instance.
func (s *apiServer) CreateCallToken(w http.ResponseWriter, r *http.Request, channelID api.ChannelId) {
	sc, ok := s.resolveChannel(w, r, channelID)
	if !ok {
		return
	}
	if !authz.Can(r.Context(), &sc.prin.user, authz.CallJoin, sc.resource()) {
		sc.deny(w, r)
		return
	}
	if !s.calls.Enabled() {
		writeError(w, r, http.StatusServiceUnavailable, codeCallsUnavailable,
			"calls are not configured on this instance")
		return
	}

	// The identity is the account id, taken from the session and never from
	// the request: it is what the ejection hooks name a participant by, and
	// what a call's participant list is resolved back through.
	ticket, err := s.calls.JoinToken(r.Context(), channelID, sc.prin.user.ID, sc.prin.user.DisplayName)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusCreated, api.CallToken{
		Token:     ticket.Token,
		Room:      ticket.Room,
		ExpiresAt: ticket.ExpiresAt,
	})
}

// routeCallWebhook registers LiveKit's webhook receiver on the base mux.
//
// It is outside docs/api/openapi.yaml and outside securityMiddleware, for the
// files origin's reason turned inside out (files_origin.go): there is no
// session here to check, and the credential is a signature over the body
// rather than a cookie. Consequences that must not be "tidied" away:
//
//   - It is not a contract route, so it has no authz-matrix row and no entry
//     in routePolicies or endpointBudgets. Its authorization is
//     calls.Service.WebhookChannel and nothing else, and that is tested in
//     internal/calls.
//   - Every refusal is 404 with no body. LiveKit retries a failure, and a
//     distinguishable answer would tell an unauthenticated caller whether a
//     room name exists.
//   - It is registered unconditionally, even on a server with calls off, so
//     the path answers the same 404 there as it does to a bad signature.
func routeCallWebhook(mux *http.ServeMux, s *apiServer) {
	mux.HandleFunc("POST "+callWebhookPath, s.serveCallWebhook)
}

// callWebhookPath is where LiveKit delivers. It is spelled once, here,
// because the deploy stack's LIVEKIT_CONFIG has to name the same path.
const callWebhookPath = "/livekit/webhook"

func (s *apiServer) serveCallWebhook(w http.ResponseWriter, r *http.Request) {
	channelID, ok := s.calls.WebhookChannel(r)
	if !ok {
		// Unverified, or a room this instance did not name. Fail closed and
		// say nothing.
		http.NotFound(w, r)
		return
	}

	// The event's own content is deliberately unused beyond the room it
	// names. A fresh read is what decides the announcement, so a duplicate
	// delivery announces the same thing twice and an out-of-order one still
	// describes the call as it is now.
	s.announceCall(r.Context(), channelID)
	w.WriteHeader(http.StatusOK)
}

// announceCall reads a channel's live call and announces what changed.
//
// A failed read costs the announcement and nothing else: the socket is a fast
// path rather than a delivery guarantee, and clients reconcile call state
// against REST on channel open and on reconnect (ws-protocol.md §5). Silence
// here degrades to "the banner appears when the channel is next opened",
// which is where this path was before the events existed.
func (s *apiServer) announceCall(ctx context.Context, channelID uuid.UUID) {
	state, err := s.calls.State(ctx, channelID)
	if err != nil {
		slog.Error("call state not announced", "channel", channelID, "error", err)
		return
	}

	switch s.calls.Transition(channelID, state.Active) {
	case calls.TransitionStarted:
		startedBy, ok := state.StartedBy()
		if !ok {
			// Unreachable: an active call has a first participant. Announcing
			// a start nobody started would be worse than announcing nothing.
			return
		}
		s.realtime.CallStarted(channelID, startedBy, s.callParticipants(ctx, state))
	case calls.TransitionUpdated:
		s.realtime.CallUpdated(channelID, s.callParticipants(ctx, state))
	case calls.TransitionEnded:
		s.realtime.CallEnded(channelID)
	case calls.TransitionNone:
	}
}

// apiCall maps live room state onto the contract's ChannelCall.
func (s *apiServer) apiCall(ctx context.Context, state calls.State) api.ChannelCall {
	if !state.Active {
		// The other fields stay absent rather than stale: "nobody is in it"
		// is the whole answer (openapi.yaml, ChannelCall).
		return api.ChannelCall{Active: false}
	}
	participants := s.callParticipants(ctx, state)
	startedAt := state.StartedAt
	return api.ChannelCall{Active: true, StartedAt: &startedAt, Participants: &participants}
}

// callParticipants names the people in a call.
//
// A participant identity is an account id and nothing else, so the username
// and display name the UI draws are this server's to supply — LiveKit never
// sees a user. A lookup that fails drops that one participant rather than the
// whole answer: a call missing a face is a worse picture, an error is no
// picture at all.
func (s *apiServer) callParticipants(ctx context.Context, state calls.State) []api.CallParticipant {
	participants := make([]api.CallParticipant, 0, len(state.Participants))
	if s.store == nil {
		return participants
	}
	for _, p := range state.Participants {
		user, err := s.store.UserByID(ctx, p.UserID)
		if err != nil {
			slog.Warn("call participant not named", "user_id", p.UserID, "error", err)
			continue
		}
		screenSharing := p.ScreenSharing
		participants = append(participants, api.CallParticipant{
			User:          apiUserSummary(user),
			JoinedAt:      p.JoinedAt,
			ScreenSharing: &screenSharing,
		})
	}
	return participants
}
