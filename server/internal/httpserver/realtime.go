package httpserver

import (
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Realtime is the delivery side of docs/api/ws-protocol.md §4, declared here
// because this package is the one that calls it — the gateway that implements
// it is a separate slice and must be replaceable without touching a handler.
//
// Writes are REST (§3): one write path means one authz choke point, one
// validation, one idempotency key and one rate limiter. A handler that has
// committed a change tells Realtime about it, and Realtime decides who is
// entitled to hear. Handlers therefore pass the whole audience question along
// with the fact, never a list of sockets.
//
// Every method must be non-blocking and must never return an error. The
// socket is a fast path, not a delivery guarantee: correctness comes from
// REST history, which the client reconciles on every resume (§5). A slow or
// dead subscriber must never be able to hold up the request that produced
// the event, and a broadcast that fails is not a reason to fail a write that
// already happened.
type Realtime interface {
	// MessageCreated announces a new message to the channel's members.
	MessageCreated(channelID uuid.UUID, message storage.Message)
	// ChannelCreated announces a channel to the users who can now see it.
	ChannelCreated(userIDs []uuid.UUID, channel storage.Channel)
	// ChannelUpdated announces a topic or member-count change.
	ChannelUpdated(channelID uuid.UUID, channel storage.Channel)
	// MemberAdded announces a new member to the channel's members.
	MemberAdded(channelID uuid.UUID, user storage.User)
	// MemberRemoved announces a departure to the members that remain. The
	// removed user is not one of them and must not receive it.
	MemberRemoved(channelID uuid.UUID, user storage.User)
	// ChannelRemoved tells one user's own sockets that a channel is gone for
	// them. It is the other half of MemberRemoved: the audiences are
	// disjoint, because a membership-scoped event cannot legally reach
	// somebody who is no longer a member.
	ChannelRemoved(userID, channelID uuid.UUID)
	// ReadPosition syncs a read position to the same user's other sockets.
	// There are no cross-user read receipts anywhere in this protocol.
	ReadPosition(userID, channelID, messageID uuid.UUID, readAt time.Time)
}

// noRealtime is the Realtime used when no gateway is attached. It exists so
// handlers can announce unconditionally: a nil check before every event is
// six chances to forget one, and the forgotten one fails silently.
type noRealtime struct{}

func (noRealtime) MessageCreated(uuid.UUID, storage.Message)               {}
func (noRealtime) ChannelCreated([]uuid.UUID, storage.Channel)             {}
func (noRealtime) ChannelUpdated(uuid.UUID, storage.Channel)               {}
func (noRealtime) MemberAdded(uuid.UUID, storage.User)                     {}
func (noRealtime) MemberRemoved(uuid.UUID, storage.User)                   {}
func (noRealtime) ChannelRemoved(uuid.UUID, uuid.UUID)                     {}
func (noRealtime) ReadPosition(uuid.UUID, uuid.UUID, uuid.UUID, time.Time) {}

// WithRealtime attaches the gateway that delivers the events above.
func WithRealtime(rt Realtime) Option {
	return func(s *apiServer) {
		if rt != nil {
			s.realtime = rt
		}
	}
}
