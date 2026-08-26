package wsgateway

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// maxAudiencePages bounds the paging of a membership or channel list. It is
// a runaway guard, not a policy: 50 pages is ten thousand rows, far past any
// channel or sidebar this product designs for.
const maxAudiencePages = 50

// event is one announced fact on its way to an audience. It is a value, not
// a closure, so the dispatcher can decide the audience itself — a handler
// hands over the fact, never a list of sockets.
type event struct {
	typ       string
	channelID uuid.UUID
	payload   any

	// ordered marks the events that carry a seq and enter the channel's
	// replay buffer (the `seq: yes` rows of §4).
	ordered bool
	// subscribed narrows delivery to the sockets that subscribed to
	// channelID — the ephemeral events, which membership alone does not
	// earn you.
	subscribed bool
	// toUsers is an explicit audience. nil means "every member of
	// channelID at send time".
	toUsers []uuid.UUID
	// verifyMembers re-checks each toUsers entry against the membership
	// table. It is set for the membership-scoped events that arrive with an
	// audience already named, and clear for the `self` ones, whose
	// recipient is the subject of the event rather than a member of
	// anything.
	verifyMembers bool
	// dropSubsUser names a user whose subscription to channelID must be
	// dropped before this event goes out. Removal does not wait for the
	// client to be polite about it (§4).
	dropSubsUser uuid.UUID
	// excludeUser is the author of an ephemeral event, who does not need to
	// be told what they just did.
	excludeUser uuid.UUID

	// dmFanout marks a presence announcement, whose audience is the peers of
	// the subject's direct messages and nobody else (§4).
	dmFanout bool
	subject  uuid.UUID
	state    string
}

// The httpserver.Realtime implementation. Every method builds one event and
// hands it to the dispatcher without blocking and without an error: these
// run on HTTP handler goroutines serving other users' requests, and a
// broadcast that fails is not a reason to fail a write that already
// happened.

// MessageCreated announces a new message to the channel's members.
func (g *Gateway) MessageCreated(channelID uuid.UUID, message storage.Message) {
	g.enqueue(event{
		typ:       typeMessageCreated,
		channelID: channelID,
		ordered:   true,
		payload:   messageData{Message: apiMessage(message)},
	})
}

// MessageUpdated announces an edit to the channel's members.
func (g *Gateway) MessageUpdated(channelID uuid.UUID, message storage.Message) {
	g.enqueue(event{
		typ:       typeMessageUpdated,
		channelID: channelID,
		ordered:   true,
		payload:   messageData{Message: apiMessage(message)},
	})
}

// MessageDeleted announces a soft delete to the channel's members.
//
// It carries the whole message, like the other two: the placeholder that
// replaces it keeps the message's position and metadata (§4), and the
// audience is the channel's membership at send time, so a deletion is no
// more visible to a stranger than the message was.
func (g *Gateway) MessageDeleted(channelID uuid.UUID, message storage.Message) {
	g.enqueue(event{
		typ:       typeMessageDeleted,
		channelID: channelID,
		ordered:   true,
		payload:   messageData{Message: apiMessage(message)},
	})
}

// ChannelCreated announces a channel to the users who can now see it. The
// named audience is still checked against the membership table: an event is
// delivered because the database says the recipient is a member, never
// because a caller said so.
func (g *Gateway) ChannelCreated(userIDs []uuid.UUID, channel storage.Channel) {
	g.enqueue(event{
		typ:           typeChannelCreated,
		channelID:     channel.ID,
		toUsers:       slices.Clone(userIDs),
		verifyMembers: true,
		payload:       channelData{Channel: apiChannel(channel)},
	})
}

// ChannelUpdated announces a topic or member-count change.
func (g *Gateway) ChannelUpdated(channelID uuid.UUID, channel storage.Channel) {
	g.enqueue(event{
		typ:       typeChannelUpdated,
		channelID: channelID,
		ordered:   true,
		payload:   channelData{Channel: apiChannel(channel)},
	})
}

// MemberAdded announces a new member to the channel's members.
func (g *Gateway) MemberAdded(channelID uuid.UUID, user storage.User) {
	g.enqueue(event{
		typ:       typeMemberAdded,
		channelID: channelID,
		ordered:   true,
		payload:   memberData{Chan: channelID, User: apiUserSummary(user)},
	})
}

// MemberRemoved announces a departure to the members that remain.
//
// The removed user is not one of them, and nothing here has to remember
// that: the audience is read from the membership table after the removal
// committed, so they are simply not in it. Their subscriptions to the
// channel are dropped at the same time.
func (g *Gateway) MemberRemoved(channelID uuid.UUID, user storage.User) {
	g.enqueue(event{
		typ:          typeMemberRemoved,
		channelID:    channelID,
		ordered:      true,
		dropSubsUser: user.ID,
		payload:      memberData{Chan: channelID, User: apiUserSummary(user)},
	})
}

// ChannelRemoved tells one user's own sockets that a channel is gone for
// them. It is the other half of MemberRemoved and the audiences are
// disjoint, which is why this one is not membership-scoped: the recipient is
// no longer a member, and the frame names nothing but their own state.
func (g *Gateway) ChannelRemoved(userID, channelID uuid.UUID) {
	g.enqueue(event{
		typ:          typeChannelRemoved,
		channelID:    channelID,
		toUsers:      []uuid.UUID{userID},
		dropSubsUser: userID,
		payload:      chanData{Chan: channelID},
	})
}

// ReadPosition syncs a read position to the same user's other sockets.
// There are no cross-user read receipts anywhere in this protocol.
func (g *Gateway) ReadPosition(userID, channelID, messageID uuid.UUID, readAt time.Time) {
	g.enqueue(event{
		typ:       typeReadPosition,
		channelID: channelID,
		toUsers:   []uuid.UUID{userID},
		payload: readPositionData{
			Chan:      channelID,
			MessageID: messageID,
			ReadAt:    readAt.UTC(),
		},
	})
}

// enqueue hands an event to the dispatcher without blocking. A full queue
// drops, because the alternative is holding up somebody's HTTP request on a
// broadcast, and §5 already says where correctness comes from: the REST
// history the client reconciles against on every resume.
func (g *Gateway) enqueue(ev event) {
	select {
	case g.events <- ev:
	default:
		g.dropped.Add(1)
		slog.Warn("ws event queue full, event dropped", "type", ev.typ, "chan", ev.channelID)
	}
}

// dispatchLoop turns events into deliveries, one at a time. It is single
// threaded on purpose: sequence numbers and replay order are per channel,
// and a pool would have to re-serialize them anyway.
//
// ponytail: one dispatcher, so throughput is bounded by one membership query
// per event. Shard by channel id if that ever shows up in a profile.
func (g *Gateway) dispatchLoop() {
	defer g.wg.Done()

	for {
		select {
		case <-g.ctx.Done():
			return
		case ev := <-g.events:
			g.deliver(ev)
		}
	}
}

// deliver resolves one event's audience and sends it.
func (g *Gateway) deliver(ev event) {
	if ev.dropSubsUser != uuid.Nil {
		g.dropSubscriptions(ev.dropSubsUser, ev.channelID)
	}
	if ev.dmFanout {
		g.deliverPresence(ev)
		return
	}

	var recipients []uuid.UUID
	if g.connectedUsers() > 0 {
		var err error
		if recipients, err = g.audience(ev); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			slog.Error("ws resolve audience", "type", ev.typ, "chan", ev.channelID, "error", err)
			if !ev.ordered {
				return
			}
			// An ordered event still has to take its sequence number and
			// enter the replay buffer, or the clients that reconnect after
			// this blip would resume straight past it.
			recipients = nil
		}
	}
	g.publish(ev, recipients)
}

// publish renders the frame once and hands it to every recipient socket.
//
// The whole of it runs under the gateway lock, which is also what register
// holds while it drains the replay buffer: an event is therefore either
// already in the buffer a joining socket read, or delivered to that socket
// afterwards, and never lost between the two.
func (g *Gateway) publish(ev event, recipients []uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := outFrame{Type: ev.typ, TS: time.Now(), Data: ev.payload}
	if ev.channelID != uuid.Nil {
		channelID := ev.channelID
		out.Chan = &channelID
	}

	if !ev.ordered {
		if frame := encodeOrLog(out); frame != nil {
			g.sendLocked(ev, frame, recipients)
		}
		return
	}

	buf := g.channels[ev.channelID]
	if buf == nil {
		buf = &replayBuffer{}
		g.channels[ev.channelID] = buf
	}
	seq := buf.nextSeq()
	out.Seq = &seq
	frame := encodeOrLog(out)
	if frame == nil {
		return
	}
	buf.store(seq, frame, out.TS)
	g.sendLocked(ev, frame, recipients)
}

// sendLocked queues one rendered frame to every socket of every recipient.
// The caller holds g.mu.
func (g *Gateway) sendLocked(ev event, frame []byte, recipients []uuid.UUID) {
	for _, userID := range recipients {
		if userID == ev.excludeUser && ev.excludeUser != uuid.Nil {
			continue
		}
		for _, s := range g.socketsForLocked(userID) {
			if ev.subscribed && !s.subscribed(ev.channelID) {
				continue
			}
			s.deliver(ev.channelID, frame)
		}
	}
}

// audience answers who is entitled to this event, right now.
//
// A membership-scoped event reads the channel's members; an event that
// arrives with a named audience is filtered against the same table unless it
// is one of the `self` events, whose recipient is the subject rather than a
// member. Nothing here consults anything captured at connect.
func (g *Gateway) audience(ev event) ([]uuid.UUID, error) {
	if ev.toUsers == nil {
		return g.channelMembers(ev.channelID)
	}
	if !ev.verifyMembers {
		return ev.toUsers, nil
	}

	members := make([]uuid.UUID, 0, len(ev.toUsers))
	for _, userID := range ev.toUsers {
		member, err := g.store.IsChannelMember(g.ctx, ev.channelID, userID)
		if err != nil {
			return nil, err
		}
		if member {
			members = append(members, userID)
		}
	}
	return members, nil
}

// channelMembers reads a channel's whole membership.
//
// ponytail: one query per page per event, uncached. The upgrade path is a
// per-channel member set invalidated by member_added / member_removed —
// which is worth building only once a profile says this is the cost, because
// a cache is also the classic way a removed member keeps receiving messages.
func (g *Gateway) channelMembers(channelID uuid.UUID) ([]uuid.UUID, error) {
	var (
		out    []uuid.UUID
		cursor *storage.ChannelMemberCursor
	)
	for range maxAudiencePages {
		page, err := g.store.ListChannelMembers(g.ctx, channelID,
			storage.ListChannelMembersParams{After: cursor, Limit: memberPage})
		if err != nil {
			return nil, err
		}
		for _, u := range page {
			out = append(out, u.ID)
		}
		if len(page) < memberPage {
			return out, nil
		}
		last := page[len(page)-1]
		cursor = &storage.ChannelMemberCursor{Username: last.Username, UserID: last.ID}
	}
	return out, nil
}

// announcePresence queues a presence change for fan-out to the subject's DM
// peers.
func (g *Gateway) announcePresence(userID uuid.UUID, state string) {
	g.enqueue(event{typ: typePresence, dmFanout: true, subject: userID, state: state})
}

// reportPresence records a state a client claimed for itself and announces
// it if it changed. `offline` is server-derived and cannot be claimed (§3),
// so a client asking for it is ignored by the caller.
func (g *Gateway) reportPresence(userID uuid.UUID, state string) {
	g.mu.Lock()
	p := g.presence[userID]
	changed := p == nil || p.state != state
	if p == nil {
		g.presence[userID] = &presenceState{state: state}
	} else {
		p.state = state
	}
	g.mu.Unlock()

	if changed {
		g.announcePresence(userID, state)
	}
}

// deliverPresence sends one presence change to the peers of the subject's
// direct messages.
//
// Presence is DM-scoped and nothing else (§4): a member of a busy channel
// learns nothing about who else is online. The peer of a DM is a member of
// it by construction — the pair is fixed at creation and the channel came
// out of the subject's own membership — so the pair itself is the membership
// fact, and no second query asks the database to confirm what it just said.
func (g *Gateway) deliverPresence(ev event) {
	g.mu.Lock()
	connected := len(g.sockets)
	_, subjectConnected := g.sockets[ev.subject]
	g.mu.Unlock()
	if connected == 0 || (connected == 1 && subjectConnected) {
		// Nobody but the subject is here to be told.
		return
	}

	channels, err := g.dmChannelsFor(ev.subject)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("ws presence fan-out", "user_id", ev.subject, "error", err)
		}
		return
	}

	for _, ch := range channels {
		peer := dmPeer(ch, ev.subject)
		if peer == uuid.Nil {
			continue
		}
		g.publish(event{
			typ:       typePresence,
			channelID: ch.ID,
			payload:   presenceEventData{UserID: ev.subject, State: ev.state},
		}, []uuid.UUID{peer})
	}
}

// dmChannelsFor returns the direct messages a user belongs to.
func (g *Gateway) dmChannelsFor(userID uuid.UUID) ([]storage.Channel, error) {
	var (
		out    []storage.Channel
		cursor *storage.ChannelCursor
	)
	for range maxAudiencePages {
		page, err := g.store.ListChannelsForUser(g.ctx, userID,
			storage.ListChannelsParams{After: cursor, Limit: channelPage})
		if err != nil {
			return nil, err
		}
		for _, ch := range page {
			if ch.Kind == storage.ChannelKindDM {
				out = append(out, ch)
			}
		}
		if len(page) < channelPage {
			return out, nil
		}
		last := page[len(page)-1]
		cursor = &storage.ChannelCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, nil
}

// dmPeer returns the other participant of a direct message, or uuid.Nil if
// the channel is not one or the user is not in the pair.
func dmPeer(ch storage.Channel, userID uuid.UUID) uuid.UUID {
	if ch.Kind != storage.ChannelKindDM || ch.DMUserA == nil || ch.DMUserB == nil {
		return uuid.Nil
	}
	switch userID {
	case *ch.DMUserA:
		return *ch.DMUserB
	case *ch.DMUserB:
		return *ch.DMUserA
	default:
		return uuid.Nil
	}
}

// dropSubscriptions removes one user's subscription to a channel from every
// socket they hold.
func (g *Gateway) dropSubscriptions(userID, channelID uuid.UUID) {
	for _, s := range g.socketsFor(userID) {
		s.unsubscribe(channelID)
	}
}
