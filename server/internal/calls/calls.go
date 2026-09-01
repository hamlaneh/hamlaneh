// Package calls is the media plane's server half: the only thing in this
// instance that holds the LiveKit API key, mints join tickets against it,
// reads live room state back, and ejects a participant whose entitlement has
// ended. docs/adr/005-calls-and-meetings.md is the design.
//
// # Three facts shape everything here
//
// **There is no calls table.** LiveKit's live room state is the truth, and a
// copy in Postgres would be a cache to invalidate for nothing. Every read in
// this package is an RPC, and "there is no call" is an empty answer rather
// than a missing row.
//
// **A room is derived from a channel, not stored.** RoomFor is a pure
// function of a channel id, so minting is deterministic and two people
// starting a call at the same moment land in the same room. That is only
// safe because no ticket this package mints can enumerate rooms — see
// JoinToken.
//
// **Two token shapes, and they must never be confused.** A *join ticket* goes
// to a browser and carries the least privilege that lets somebody talk:
// roomJoin for one room, publish and subscribe, and nothing else. A guest's
// is the same ticket with a random identity (GuestToken), because the
// absences that matter for a member matter more for somebody with no account.
// An *admin token* never leaves this process — it is minted per RoomService
// call, lives seconds, and is the only place roomAdmin, roomList or
// roomCreate ever appears.
package calls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	"github.com/twitchtv/twirp"
)

// The environment this service is configured from. All three together turn
// calls on; none at all is a supported install with calls off; a partial set
// stops startup (FromEnv), exactly as a half-configured mail transport does.
const (
	EnvAPIKey    = "HAMLANEH_LIVEKIT_API_KEY"    // #nosec G101 -- the variable's name, not a key
	EnvAPISecret = "HAMLANEH_LIVEKIT_API_SECRET" // #nosec G101 -- the variable's name, not a secret
	EnvURL       = "HAMLANEH_LIVEKIT_URL"
)

// roomPrefix names a channel's room, and conferencePrefix a conference's. The
// prefixes are distinct so a channel id can never be spelled as a conference
// and the two namespaces cannot collide (ADR 005).
const (
	roomPrefix       = "chan-"
	conferencePrefix = "conf-"
)

// guestPrefix names a conference guest's participant identity, and is the
// one thing that keeps the two identity spaces apart.
//
// Everything that resolves a participant back to a person parses the
// identity as an account id: stateOf, and both ejection hooks. A guest has no
// account, so their identity must be something uuid.Parse can never accept —
// "guest-" plus a UUID is 42 characters, which is none of the four lengths it
// takes. That is what makes a guest unable to be mistaken for a member by
// anything downstream, rather than a convention downstream code has to
// remember.
const guestPrefix = "guest-"

// JoinTTL is how long a join ticket lives. Two minutes, because a ticket has
// no business outliving the click that asked for it, and because it costs a
// long call nothing:
//
// LiveKit re-mints a participant's token over the signal channel
// (SignalResponse.refresh_token) the moment the RTC session worker starts,
// and every five minutes after that — livekit-server v1.13.6,
// pkg/service/roommanager.go: rtcSessionWorker calls refreshToken once before
// starting its ticker, precisely "for cases when client token is close to
// expiring". Each refreshed token is good for at least ten minutes
// (tokenDefaultTTL). So a participant holds a fresh ten-minute token from the
// instant they join, and this TTL bounds only the window between minting and
// joining — which is what ADR 005 says it bounds.
//
// The verification matters because the answer decides the number: if the
// refresh only began at the first five-minute tick, a two-minute ticket would
// leave three minutes in which a reconnect (which re-verifies the token, like
// every /rtc request) could not succeed, and the TTL would have to rise.
const JoinTTL = 2 * time.Minute

// adminTTL is how long one of our own RoomService tokens lives. It is minted,
// used on the next line, and thrown away; seconds are generous. It never
// leaves this process.
const adminTTL = 30 * time.Second

// rpcTimeout bounds one RoomService call. LiveKit is a container on the same
// compose network; a call that has not answered in this long is a media plane
// that is down, and every caller here has an honest empty answer to give.
const rpcTimeout = 5 * time.Second

// ejectRetryDelay is the pause before the single retry of a failed ejection.
// One retry, not a queue: the participant is gone at call end regardless, and
// a queue of ejections outliving the calls they were for is a worse thing to
// own than a logged failure (ADR 005).
const ejectRetryDelay = 2 * time.Second

// maxWebhookBytes bounds a webhook body. The largest legitimate one is a room
// plus one participant; 64 KiB is the bound every other body in this server
// gets, and the verifier reads the whole body to hash it, so the cap has to be
// applied before it is handed over.
const maxWebhookBytes = 64 << 10

// ErrNotConfigured is what a nil service answers. It exists so a caller that
// forgot to check Enabled fails loudly rather than panicking.
var ErrNotConfigured = errors.New("calls: no media server configured")

// Service is the media plane as the rest of the server sees it. A nil
// *Service is a fully valid "calls are not configured on this instance": every
// method below is nil-safe and answers the honest empty or ErrNotConfigured.
type Service struct {
	apiKey    string
	apiSecret string
	rooms     livekit.RoomService
	// retryDelay is the pause before the single ejection retry. It is a field
	// only so the test that has to sit through one does not; nothing
	// configures it.
	retryDelay time.Duration

	// announced records the channels this process has already announced a
	// live call for. It is a de-duplicator, not state: it only decides
	// whether a fresh read is spelled call_started or call_updated. Losing it
	// (a restart) costs one redundant call_started, which clients reconcile
	// away against GET /channels/{id}/call like every other event (ADR 005:
	// the events are hints, REST is the truth).
	mu        sync.Mutex
	announced map[uuid.UUID]bool
}

// FromEnv builds the service from the environment, or reports nil when calls
// are not configured at all.
//
// All three variables set is a configured media plane. None set is a
// supported install — a development server without the stack — and calls are
// off. Any partial set is an error, and startup stops: a server that mints
// tokens against half a credential, or reads room state from an address
// nobody set, fails at somebody's first call instead of at boot.
func FromEnv() (*Service, error) {
	key := strings.TrimSpace(os.Getenv(EnvAPIKey))
	secret := strings.TrimSpace(os.Getenv(EnvAPISecret))
	url := strings.TrimSpace(os.Getenv(EnvURL))

	set := 0
	for _, v := range []string{key, secret, url} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil, nil
	case 3:
		return New(key, secret, url, nil), nil
	default:
		return nil, fmt.Errorf("calls: set all of %s, %s and %s, or none of them",
			EnvAPIKey, EnvAPISecret, EnvURL)
	}
}

// New builds a service against one media server. client overrides the HTTP
// client the RoomService calls ride on; nil takes a bounded default.
func New(apiKey, apiSecret, url string, client livekit.HTTPClient) *Service {
	if client == nil {
		client = &http.Client{Timeout: rpcTimeout}
	}
	return &Service{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		// Protobuf rather than JSON: the wire shape is then the generated
		// contract itself, with no field-naming convention to get right.
		rooms:      livekit.NewRoomServiceProtobufClient(url, client),
		retryDelay: ejectRetryDelay,
		announced:  make(map[uuid.UUID]bool),
	}
}

// Enabled reports whether this instance has a media plane. It is what the
// instance document's `calls` flag and every handler's 503 are decided from,
// and it is nil-safe so an unconfigured install needs no nil check anywhere
// else.
func (s *Service) Enabled() bool { return s != nil }

// RoomFor is a channel's room name. Deterministic, so two people starting a
// call at the same moment land in the same room, and safe to hand back to a
// member because no join ticket can enumerate rooms.
func RoomFor(channelID uuid.UUID) string { return roomPrefix + channelID.String() }

// ChannelFor is the inverse, for the webhook: it reports the channel a room
// name belongs to, and false for anything that is not one of ours.
func ChannelFor(room string) (uuid.UUID, bool) { return idFor(room, roomPrefix) }

// ConferenceRoomFor is a conference's room name. Same shape as a channel's
// and a different namespace, so no id can be spelled as both.
func ConferenceRoomFor(conferenceID uuid.UUID) string {
	return conferencePrefix + conferenceID.String()
}

// ConferenceFor is the inverse of ConferenceRoomFor.
func ConferenceFor(room string) (uuid.UUID, bool) { return idFor(room, conferencePrefix) }

// idFor reports the id behind a prefixed room name, and false for anything
// that is not one of ours.
func idFor(room, prefix string) (uuid.UUID, bool) {
	rest, found := strings.CutPrefix(room, prefix)
	if !found {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// Ticket is one minted join ticket.
type Ticket struct {
	Token     string
	Room      string
	ExpiresAt time.Time
}

// JoinToken mints a join ticket for one user in one channel's call.
//
// The grant set is the whole security story, and the absences carry as much
// weight as the presences (ADR 005):
//
//   - roomJoin, scoped to this one room, so the ticket opens one door.
//   - canPublish and canSubscribe, because that is what being in a call is.
//   - canPublishData explicitly FALSE. This is the one grant that cannot be
//     denied by omission: LiveKit reads an absent canPublishData as
//     canPublish (auth.VideoGrant.GetCanPublishData), so leaving it out would
//     GRANT the data channel. Chat stays on the one write path with the one
//     authz choke point, which is also what keeps message encryption on the
//     MLS path in Phase 3.
//   - roomList absent, so no participant can enumerate rooms — which is what
//     makes deriving a room name from a channel id safe.
//   - roomAdmin absent, so no participant can eject another. Ejection is this
//     server's alone.
//   - roomCreate, roomRecord, ingressAdmin, hidden, recorder and agent all
//     absent. None of them is a thing a person in a chat call does.
//
// TestJoinTokenGrantsNothingElse decodes a real minted token and asserts each
// absence, because a grant nobody notices creeping in is the failure mode
// this whole design guards against.
func (s *Service) JoinToken(ctx context.Context, channelID, userID uuid.UUID, displayName string) (Ticket, error) {
	if s == nil {
		return Ticket{}, ErrNotConfigured
	}
	// The identity is the account id and nothing else. It is what the
	// ejection hooks name a participant by, and what GET .../call resolves
	// back into a person — so it must be the one identifier that cannot be
	// chosen by the person holding the ticket.
	return s.ticket(ctx, RoomFor(channelID), userID.String(), displayName)
}

// GuestToken mints a join ticket for one conference guest.
//
// It is JoinToken's path, its grant set and its TTL, differing in exactly two
// things — and the sameness is the point, because every absence that matters
// for a member matters more for somebody with no account at all. There is one
// minter here, not two that agree today.
//
// What differs:
//
//   - The room is a conference's, so the ticket opens that one room. A guest
//     ticket cannot name a channel: the room comes from the conference id the
//     server resolved out of a live link, never from the request.
//   - The identity is a fresh random guest id rather than an account. Nothing
//     downstream can mistake it for a member (see guestPrefix), and it is
//     fresh per join so two guests are two participants and neither can be
//     addressed by anyone who saw the other's ticket.
//
// The display name is whatever the guest typed. It is unverifiable by
// construction and ADR 005 names that rather than hiding it: a guest can
// present as anyone, and the people in the room are the mitigation.
func (s *Service) GuestToken(ctx context.Context, conferenceID uuid.UUID, displayName string) (Ticket, error) {
	if s == nil {
		return Ticket{}, ErrNotConfigured
	}
	return s.ticket(ctx, ConferenceRoomFor(conferenceID), guestPrefix+uuid.NewString(), displayName)
}

// ticket mints one join ticket. Both callers above go through it, so the
// grant set — and the room's existence — is decided once.
//
// The room is created here because the deploy stack runs with
// `auto_create: false`, and that is what makes ending a meeting durable
// rather than momentary. LiveKit tokens are stateless: DeleteRoom does not
// invalidate the ones already out, and a connected participant is re-minted a
// ten-minute token every five minutes which their client reconnects with. If
// a join could instantiate a room, a revoked guest would be disconnected once
// and simply rebuild it. So the server is the only thing that may create one,
// and a join ticket deliberately carries no roomCreate grant.
//
// CreateRoom is idempotent — livekit-server v1.13.6 RoomManager.getOrCreateRoom
// returns the room that is already there and resolves the concurrent-create
// race under its own lock — so two people starting a call at the same moment
// both succeed, which is what ADR 005's stable room names are for.
func (s *Service) ticket(ctx context.Context, room, identity, displayName string) (Ticket, error) {
	if err := s.createRoom(ctx, room); err != nil {
		return Ticket{}, err
	}

	grant := &auth.VideoGrant{RoomJoin: true, Room: room}
	grant.SetCanPublish(true)
	grant.SetCanSubscribe(true)
	grant.SetCanPublishData(false)

	expiresAt := time.Now().Add(JoinTTL)
	token, err := auth.NewAccessToken(s.apiKey, s.apiSecret).
		SetIdentity(identity).
		SetName(displayName).
		SetValidFor(JoinTTL).
		SetVideoGrant(grant).
		ToJWT()
	if err != nil {
		return Ticket{}, fmt.Errorf("mint join ticket: %w", err)
	}
	return Ticket{Token: token, Room: room, ExpiresAt: expiresAt}, nil
}

// Participant is one person in a call, as the media server reports them.
type Participant struct {
	// UserID is the account behind the participant identity. A participant
	// whose identity is not one of ours is skipped before this type exists.
	UserID        uuid.UUID
	JoinedAt      time.Time
	ScreenSharing bool
}

// State is a channel's call as the media server has it right now. Active is
// false when nobody is in the room — including when the room does not exist,
// which is the ordinary case and not an error.
type State struct {
	Active       bool
	StartedAt    time.Time
	Participants []Participant
}

// StartedBy names who is treated as having started the call: the participant
// who joined first. It is derived rather than stored, for the same reason
// nothing else here is stored.
func (st State) StartedBy() (uuid.UUID, bool) {
	if len(st.Participants) == 0 {
		return uuid.Nil, false
	}
	return st.Participants[0].UserID, true
}

// State reads a channel's live call.
//
// An unconfigured instance answers "no call" rather than an error: there is
// no call, which is the truth about that install, and GET .../call reserves
// no 503 in the contract.
func (s *Service) State(ctx context.Context, channelID uuid.UUID) (State, error) {
	if s == nil {
		return State{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	ctx, err := s.authorize(ctx, &auth.VideoGrant{RoomAdmin: true, Room: RoomFor(channelID)})
	if err != nil {
		return State{}, err
	}
	resp, err := s.rooms.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: RoomFor(channelID)})
	if err != nil {
		if isNotFound(err) {
			// No room means no call. Rooms are created on demand and reaped
			// when the last participant leaves, so this is the answer most of
			// the time and it is not a failure.
			return State{}, nil
		}
		return State{}, fmt.Errorf("list participants: %w", err)
	}
	return stateOf(resp.GetParticipants()), nil
}

// stateOf maps the media server's participant list onto a State, dropping
// anything that is not one of our users.
func stateOf(infos []*livekit.ParticipantInfo) State {
	var st State
	for _, p := range infos {
		userID, err := uuid.Parse(p.GetIdentity())
		if err != nil {
			// Not an identity this server minted. Nothing else can obtain a
			// token for one of our rooms, so this is a media server serving
			// somebody else's traffic; it is not ours to describe.
			slog.Warn("call participant with an unknown identity",
				"identity", p.GetIdentity())
			continue
		}
		st.Participants = append(st.Participants, Participant{
			UserID:        userID,
			JoinedAt:      time.Unix(p.GetJoinedAt(), 0).UTC(),
			ScreenSharing: screenSharing(p),
		})
	}
	if len(st.Participants) == 0 {
		return State{}
	}

	// Oldest first, so StartedBy is the first person in and started_at is
	// when the call actually began rather than when this read happened.
	sort.Slice(st.Participants, func(i, j int) bool {
		if st.Participants[i].JoinedAt.Equal(st.Participants[j].JoinedAt) {
			// A stable tiebreak, so two people who joined in the same second
			// do not swap places between two reads of the same call.
			return st.Participants[i].UserID.String() < st.Participants[j].UserID.String()
		}
		return st.Participants[i].JoinedAt.Before(st.Participants[j].JoinedAt)
	})
	st.Active = true
	st.StartedAt = st.Participants[0].JoinedAt
	return st
}

// screenSharing reports whether this participant is publishing a screen.
func screenSharing(p *livekit.ParticipantInfo) bool {
	for _, track := range p.GetTracks() {
		if track.GetSource() == livekit.TrackSource_SCREEN_SHARE && !track.GetMuted() {
			return true
		}
	}
	return false
}

// Eject removes one participant from a channel's call, in the background.
//
// It returns immediately and never reports anything, because every caller is
// after-commit work on a request that has already succeeded: a membership was
// removed, or an account was deactivated, and neither may fail because the
// media server was slow. What it must not do is make the entitlement question
// depend on the ejection — a failed RPC is logged and retried once, and the
// participant is gone at call end regardless (ADR 005).
//
// ctx supplies values and nothing else: the work deliberately outlives the
// request that started it.
func (s *Service) Eject(ctx context.Context, channelID, userID uuid.UUID) {
	if s == nil {
		return
	}
	go s.ejectWithRetry(context.WithoutCancel(ctx), RoomFor(channelID), userID)
}

// EjectEverywhere removes one participant from every call on this instance.
//
// It is the deactivation hook: an offboarded account holds no membership
// anywhere any more, and there is no cheap way to ask which of their channels
// they were in. Asking the media server which rooms are live is that way —
// rooms exist only while somebody is in one, so the list is the instance's
// ongoing calls and nothing else.
func (s *Service) EjectEverywhere(ctx context.Context, userID uuid.UUID) {
	if s == nil {
		return
	}
	// Detached from the request that started it, like Eject above: an
	// offboarding has already committed by the time this runs.
	sweep := context.WithoutCancel(ctx)
	go func() {
		rooms, err := s.liveRooms(sweep)
		if err != nil {
			slog.Error("calls: list rooms for offboarding", "user_id", userID, "error", err)
			return
		}
		for _, room := range rooms {
			s.ejectWithRetry(sweep, room, userID)
		}
	}()
}

// liveRooms names every channel room currently open on the media server.
func (s *Service) liveRooms(ctx context.Context) ([]string, error) {
	rooms, err := s.listRooms(ctx, nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rooms))
	for _, room := range rooms {
		if _, ours := ChannelFor(room.GetName()); ours {
			names = append(names, room.GetName())
		}
	}
	return names, nil
}

// ActiveConferences reports which of the named conferences somebody is in
// right now. It is the whole of what a conference's `active` needs, and it is
// deliberately less than State reads: a conference's participants are guests
// this server cannot name, so there is nothing to resolve and nothing worth
// telling a link-holder beyond whether the meeting is happening.
//
// One round trip for a whole page, scoped to the rooms asked about rather
// than the instance's. An empty ask is answered without one.
//
// The count, not the room's existence, is the question: a room lingers for a
// few minutes after its last participant leaves, and "somebody is in there"
// must not be true of an empty one.
func (s *Service) ActiveConferences(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	active := map[uuid.UUID]bool{}
	if s == nil || len(ids) == 0 {
		// No media plane means no meeting, which is the truth about that
		// install rather than an error — the same answer State gives.
		return active, nil
	}

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, ConferenceRoomFor(id))
	}
	rooms, err := s.listRooms(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, room := range rooms {
		if room.GetNumParticipants() == 0 {
			continue
		}
		if id, ours := ConferenceFor(room.GetName()); ours {
			active[id] = true
		}
	}
	return active, nil
}

// CloseConference ends the meeting in one conference's room.
//
// It is the second half of a revocation, and the half that makes it one: a
// revocation that killed the link and let the current meeting run on would
// not be a revocation (ADR 005). DeleteRoom disconnects everybody in the room
// rather than waiting for them to leave.
//
// A room that is not there is success, not a failure: rooms exist only while
// somebody is in one, so a conference nobody is meeting in is already closed.
func (s *Service) CloseConference(ctx context.Context, conferenceID uuid.UUID) error {
	if s == nil {
		// No media plane, so there is no meeting to end.
		return nil
	}

	room := ConferenceRoomFor(conferenceID)
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	ctx, err := s.authorize(ctx, &auth.VideoGrant{RoomCreate: true, Room: room})
	if err != nil {
		return err
	}
	if _, err := s.rooms.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: room}); err != nil &&
		!isNotFound(err) {
		return fmt.Errorf("delete room: %w", err)
	}
	return nil
}

// createRoom makes sure a room exists before a ticket for it is minted. See
// ticket for why the server, and only the server, may do this.
//
// It is idempotent, so it needs no already-exists handling: LiveKit answers
// with the room that is already there.
func (s *Service) createRoom(ctx context.Context, room string) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	ctx, err := s.authorize(ctx, &auth.VideoGrant{RoomCreate: true, Room: room})
	if err != nil {
		return err
	}
	if _, err := s.rooms.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: room}); err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	return nil
}

// listRooms asks the media server which of the named rooms are open; nil
// names asks about all of them. This is the one call that needs roomList, and
// the token that carries it is minted here, used on the next line, and never
// seen by anybody.
func (s *Service) listRooms(ctx context.Context, names []string) ([]*livekit.Room, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	ctx, err := s.authorize(ctx, &auth.VideoGrant{RoomList: true})
	if err != nil {
		return nil, err
	}
	resp, err := s.rooms.ListRooms(ctx, &livekit.ListRoomsRequest{Names: names})
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	return resp.GetRooms(), nil
}

// ejectWithRetry removes one participant, retrying once.
func (s *Service) ejectWithRetry(ctx context.Context, room string, userID uuid.UUID) {
	err := s.removeParticipant(ctx, room, userID)
	if err == nil || isNotFound(err) {
		// Not in that room, or no such room. Either way the outcome asked for
		// is the outcome that holds.
		return
	}

	slog.Warn("calls: eject failed, retrying once", "room", room, "user_id", userID, "error", err)
	select {
	case <-ctx.Done():
		return
	case <-time.After(s.retryDelay):
	}
	if err = s.removeParticipant(ctx, room, userID); err != nil && !isNotFound(err) {
		// One retry, then stop. The participant is gone at call end
		// regardless, and a queue that outlives the call it was for is a
		// worse thing to own than this log line.
		slog.Error("calls: eject failed after retry, participant stays until the call ends",
			"room", room, "user_id", userID, "error", err)
	}
}

func (s *Service) removeParticipant(ctx context.Context, room string, userID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	ctx, err := s.authorize(ctx, &auth.VideoGrant{RoomAdmin: true, Room: room})
	if err != nil {
		return err
	}
	_, err = s.rooms.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     room,
		Identity: userID.String(),
	})
	if err != nil {
		return fmt.Errorf("remove participant: %w", err)
	}
	return nil
}

// authorize attaches an admin token to one RoomService call.
//
// The token is minted per call, carries only the grant that call needs, and
// lives adminTTL. This is the only place roomAdmin or roomList is ever
// spelled, and none of these tokens is ever handed to a client.
func (s *Service) authorize(ctx context.Context, grant *auth.VideoGrant) (context.Context, error) {
	token, err := auth.NewAccessToken(s.apiKey, s.apiSecret).
		SetIdentity("hamlaneh-server").
		SetValidFor(adminTTL).
		SetVideoGrant(grant).
		ToJWT()
	if err != nil {
		return nil, fmt.Errorf("mint admin token: %w", err)
	}
	ctx, err = twirp.WithHTTPRequestHeaders(ctx, http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if err != nil {
		return nil, fmt.Errorf("attach admin token: %w", err)
	}
	return ctx, nil
}

// isNotFound reports whether an RPC failed because the room or the
// participant was not there. It is the one failure that is an ordinary
// answer rather than a problem: rooms come and go with the calls in them.
func isNotFound(err error) bool {
	var twerr twirp.Error
	return errors.As(err, &twerr) && twerr.Code() == twirp.NotFound
}

// Transition is which of the three call events a freshly read state is.
type Transition int

const (
	// TransitionNone is nothing to announce: a room that was not active and
	// still is not.
	TransitionNone Transition = iota
	// TransitionStarted is the first participant in a room this process had
	// not yet announced.
	TransitionStarted
	// TransitionUpdated is somebody joining, leaving, or starting a screen
	// share in a call that is already running.
	TransitionUpdated
	// TransitionEnded is the last participant leaving.
	TransitionEnded
)

// Transition records a freshly read state and reports which event it is.
//
// This is the whole reason the webhook is idempotent and order-proof: every
// delivery triggers a read of live state rather than applying the event's own
// content, so a duplicate produces the same answer twice and an event arriving
// out of order still describes the room as it is now. The only thing carried
// across calls is one boolean per channel, and it decides a label rather than
// a fact.
func (s *Service) Transition(channelID uuid.UUID, active bool) Transition {
	if s == nil {
		return TransitionNone
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	was := s.announced[channelID]
	switch {
	case active && !was:
		s.announced[channelID] = true
		return TransitionStarted
	case active:
		return TransitionUpdated
	case was:
		delete(s.announced, channelID)
		return TransitionEnded
	default:
		return TransitionNone
	}
}

// WebhookChannel verifies a LiveKit webhook and reports the channel it
// concerns.
//
// Verification is the whole authentication: the receiver is registered
// outside the contract router, so no session, no CSRF token and no cookie
// reaches it. LiveKit signs the body — an Authorization JWT issued by our own
// API key whose sha256 claim is the digest of the bytes — and anything that
// does not verify against our secret is refused. It fails closed: an
// unconfigured service, a missing header, a wrong signature, a body that does
// not hash to the claim, and a room name that is not one of ours all report
// false.
//
// The event's own content is deliberately not used beyond the room name.
// Delivery is at-least-once and unordered, so an event is a hint that
// something changed and never a fact to apply.
func (s *Service) WebhookChannel(r *http.Request) (uuid.UUID, bool) {
	if s == nil {
		return uuid.Nil, false
	}

	// Bounded before the verifier reads it: verification hashes the whole
	// body, so an unbounded read would be an unauthenticated caller choosing
	// how much memory this server spends. The nil writer is deliberate — the
	// bound is about memory, not about answering 413, and every refusal on
	// this surface is the same 404.
	r.Body = http.MaxBytesReader(nil, r.Body, maxWebhookBytes)

	event, err := webhook.ReceiveWebhookEvent(r, auth.NewSimpleKeyProvider(s.apiKey, s.apiSecret))
	if err != nil {
		slog.Warn("calls: webhook refused", "error", err)
		return uuid.Nil, false
	}
	channelID, ours := ChannelFor(event.GetRoom().GetName())
	if !ours {
		return uuid.Nil, false
	}
	return channelID, true
}
