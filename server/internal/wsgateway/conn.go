package wsgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/authz"
	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Per-socket frame budget for the two operations §8 names: subscribe (a
// subscribe-storm is a cheap way to hammer membership checks) and typing.
// The budget is per socket rather than per family because a socket is what
// spends it, and exceeding it costs an error frame, never the connection.
const (
	frameRateLimit  = 120
	frameRateWindow = time.Minute
)

// resumeRequest is one channel the client asked to resume, already checked
// against its membership right now.
type resumeRequest struct {
	channelID uuid.UUID
	seq       uint64
}

// socket is one live connection.
//
// The write pump is the only goroutine that writes to conn, including the
// close frame. That is what makes "close this socket with 4401" a single
// well-defined act rather than a race between the sweeper, the heartbeat and
// whatever the read loop just decided.
type socket struct {
	g        *Gateway
	conn     *websocket.Conn
	user     storage.User
	familyID uuid.UUID

	// ctx bounds this socket's storage reads. It is never handed to the
	// WebSocket itself: cancelling a coder/websocket read tears the
	// connection down without a close frame, and every close in this
	// protocol carries a code.
	ctx    context.Context
	cancel context.CancelFunc

	out  chan []byte
	done chan struct{}

	closeOnce   sync.Once
	closeCode   websocket.StatusCode
	closeReason string

	// lastRX is the unix-nano time of the last inbound frame, read by the
	// heartbeat check on the write pump.
	lastRX atomic.Int64

	frames *ratelimit.Limiter

	mu     sync.Mutex
	subs   map[uuid.UUID]struct{}
	resync map[uuid.UUID]struct{}
}

// serve upgrades the connection and runs the socket to completion.
func (g *Gateway) serve(w http.ResponseWriter, r *http.Request, user storage.User, familyID uuid.UUID) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The caller has ALREADY refused every Origin but these, and that
		// ordering is what makes this safe -- not agreement between the two
		// checks, which is what this comment used to claim.
		//
		// They genuinely disagree. OriginPatterns are matched with path.Match
		// (coder/websocket v1.8.15, accept.go), so the home-mode loopback
		// alias "http://[::1]:8080" reads as a character class and this check
		// also admits "http://1:8080" and "http://::8080".
		// Gateway.OriginAllowed, in wsgateway.go, is whole-string EqualFold
		// with no wildcard at all, so it refuses those first and the looser
		// second check never sees them.
		//
		// Anything that moves the gateway's own check after this one, or drops
		// it, hands over a wildcard nobody wrote.
		OriginPatterns: g.origins,
		// §1: no permessage-deflate. A shared compression context mixing one
		// user's text with another's is a length side-channel this protocol
		// refuses to have rather than reason about per frame. This is also
		// the library default, stated because it is a security property.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept has already written the failure response.
		slog.Debug("ws upgrade rejected", "error", err)
		return
	}

	// The 64 KiB cap is enforced in readFrame, not here: the library's own
	// limit closes with 1009 and §8 requires 4413. readFrame never buffers
	// more than the cap plus one byte, so nothing is unbounded.
	conn.SetReadLimit(-1)

	ctx, cancel := context.WithCancel(g.ctx)
	s := &socket{
		g:        g,
		conn:     conn,
		user:     user,
		familyID: familyID,
		ctx:      ctx,
		cancel:   cancel,
		out:      make(chan []byte, outboundBuffer),
		done:     make(chan struct{}),
		frames:   ratelimit.New(frameRateLimit, frameRateWindow),
		subs:     make(map[uuid.UUID]struct{}),
		resync:   make(map[uuid.UUID]struct{}),
	}
	s.lastRX.Store(time.Now().UnixNano())

	if !g.track(s) {
		// The gateway started closing between the handshake and here.
		if err := conn.Close(closeGoingAway, "server shutting down"); err != nil {
			slog.Debug("ws close on a shutting-down gateway", "error", err)
		}
		cancel()
		return
	}
	defer g.untrack(s)

	s.run()
}

// run drives the socket until it closes and releases everything it owned.
func (s *socket) run() {
	defer s.cancel()
	defer func() {
		if err := s.conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Debug("ws close", "error", err)
		}
	}()

	var pump sync.WaitGroup
	pump.Add(1)
	go func() {
		defer pump.Done()
		s.writePump()
	}()

	s.readLoop()
	// The read loop may have ended on a network error rather than a decision;
	// shutdown is idempotent, so this only names a code nobody else did.
	s.shutdown(closeNormal, "")
	pump.Wait()

	s.g.deregister(s)
}

// readLoop reads client frames until the socket ends.
func (s *socket) readLoop() {
	// §1: a socket that sends nothing within ten seconds of opening is
	// closed. The timer is stopped by the hello that satisfies it.
	greeting := time.AfterFunc(s.g.helloTimeout, func() {
		s.shutdown(closeProtocolError, "no hello")
	})
	defer greeting.Stop()

	greeted := false
	for {
		raw, ok := s.readFrame()
		if !ok {
			return
		}
		s.lastRX.Store(time.Now().UnixNano())

		f, err := parseFrame(raw)
		if err != nil {
			slog.Debug("ws malformed frame", "user_id", s.user.ID, "error", err)
			s.shutdown(closeProtocolError, "malformed frame")
			return
		}

		if !greeted {
			if f.Type != typeHello {
				s.shutdown(closeProtocolError, "first frame must be hello")
				return
			}
			greeting.Stop()
			if !s.handleHello(f) {
				return
			}
			greeted = true
			continue
		}
		if f.Type == typeHello {
			// The version is negotiated once and never renegotiated on a live
			// socket (§1), so a second hello is a client bug, not a request.
			s.shutdown(closeProtocolError, "hello already negotiated")
			return
		}
		if !s.handleFrame(f) {
			return
		}
	}
}

// readFrame reads one text frame, enforcing the §2 size cap itself.
//
// It reads at most one byte past the cap and then stops: an oversize frame
// closes the socket with 4413 and the rest of it is never pulled into
// memory. It reports false once the socket is finished, having named the
// close code when the reason was ours.
func (s *socket) readFrame() ([]byte, bool) {
	// context.Background, deliberately: the socket is stopped by closing it,
	// which unblocks this read and still delivers a close code. A cancelled
	// context here would tear the connection down silently instead.
	typ, r, err := s.conn.Reader(context.Background())
	if err != nil {
		return nil, false
	}
	if typ != websocket.MessageText {
		// §2: binary frames are rejected.
		s.shutdown(closeProtocolError, "binary frame")
		return nil, false
	}

	raw, err := io.ReadAll(io.LimitReader(r, maxFrameBytes+1))
	if err != nil {
		return nil, false
	}
	if len(raw) > maxFrameBytes {
		s.shutdown(closeFrameTooLarge, "frame exceeds 64 KiB")
		return nil, false
	}
	return raw, true
}

// handleHello performs the §1 protocol handshake and registers the socket.
// It reports false when the socket must end.
func (s *socket) handleHello(f inFrame) bool {
	var data helloData
	if err := json.Unmarshal(f.Data, &data); err != nil {
		s.shutdown(closeProtocolError, "malformed hello")
		return false
	}
	if data.ProtocolVersion != protocolVersion {
		s.shutdown(closeProtocolError, "unsupported_protocol_version")
		return false
	}

	candidates, refused := s.resolveResume(data.Resume)

	// hello_ok, the replay it promises and the registration all happen under
	// one gateway lock, so nothing this socket is entitled to can slip
	// between the replay and the first live event.
	wasOffline := s.g.registerAndReplay(s, candidates, refused,
		func(resumed []resumedCursor, resync []uuid.UUID) {
			s.queue(encodeOrLog(outFrame{
				Type: typeHelloOK,
				ID:   f.ID,
				TS:   time.Now(),
				Data: helloOKData{
					ProtocolVersion:          protocolVersion,
					UserID:                   s.user.ID,
					SessionFamilyID:          s.familyID,
					HeartbeatIntervalSeconds: int(s.g.heartbeatInterval / time.Second),
					MaxFrameBytes:            maxFrameBytes,
					Resumed:                  resumed,
					Resync:                   resync,
				},
			}))
		})
	if wasOffline {
		s.g.announcePresence(s.user.ID, presenceOnline)
	}
	return true
}

// resolveResume checks the client's resume list against membership and
// returns the channels that may be replayed plus the ones that are refused
// outright. Whether the buffer can actually satisfy a candidate is decided
// later, under the gateway lock.
//
// The list is the client's claim about what it once saw, and membership is
// re-checked for every entry against the present (§5). A channel the user is
// not a member of now — removed while disconnected, or never a member at all
// — is never replayed. It goes in resync, which reveals nothing: the list is
// derived from the client's own request, and the REST backfill answers 404
// exactly as it would for any non-member.
func (s *socket) resolveResume(requests []resumedCursor) (candidates []resumeRequest, refused []uuid.UUID) {
	seen := make(map[uuid.UUID]struct{}, len(requests))
	for _, req := range requests {
		if _, dup := seen[req.Chan]; dup {
			continue
		}
		seen[req.Chan] = struct{}{}

		if len(seen) > maxResumeChannels {
			refused = append(refused, req.Chan)
			continue
		}
		allowed, err := s.g.canRead(s.ctx, s.user, req.Chan)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("ws resume membership check", "chan", req.Chan, "error", err)
			}
			refused = append(refused, req.Chan)
			continue
		}
		if !allowed {
			refused = append(refused, req.Chan)
			continue
		}
		candidates = append(candidates, resumeRequest{channelID: req.Chan, seq: req.Seq})
	}
	return candidates, refused
}

// handleFrame dispatches one post-handshake client operation. It reports
// false when the socket must end.
func (s *socket) handleFrame(f inFrame) bool {
	switch f.Type {
	case typeSubscribe:
		return s.handleSubscribe(f)
	case typeUnsubscribe:
		return s.handleUnsubscribe(f)
	case typeTyping:
		return s.handleTyping(f)
	case typePresence:
		return s.handlePresence(f)
	case typePing:
		s.queue(encodeOrLog(outFrame{Type: typePong, ID: f.ID, TS: time.Now()}))
		return true
	case typePong:
		// The liveness it proves was already recorded by the read loop.
		return true
	default:
		// §2: a receiver that does not recognise a type ignores the frame and
		// keeps the socket open. This is what lets an old client survive a
		// newer server.
		return true
	}
}

func (s *socket) handleSubscribe(f inFrame) bool {
	if s.rateLimited(f) {
		return true
	}
	if !s.requireMember(f) {
		return true
	}

	s.mu.Lock()
	s.subs[*f.Chan] = struct{}{}
	s.mu.Unlock()

	s.queue(encodeOrLog(outFrame{Type: typeSubscribed, ID: f.ID, Chan: f.Chan, TS: time.Now()}))
	return true
}

func (s *socket) handleUnsubscribe(f inFrame) bool {
	if !s.requireMember(f) {
		return true
	}
	s.unsubscribe(*f.Chan)
	s.queue(encodeOrLog(outFrame{Type: typeUnsubscribed, ID: f.ID, Chan: f.Chan, TS: time.Now()}))
	return true
}

func (s *socket) handleTyping(f inFrame) bool {
	if s.rateLimited(f) {
		return true
	}
	if !s.requireMember(f) {
		return true
	}
	s.g.enqueue(event{
		typ:         typeTyping,
		channelID:   *f.Chan,
		subscribed:  true,
		excludeUser: s.user.ID,
		payload:     typingData{UserID: s.user.ID},
	})
	return true
}

func (s *socket) handlePresence(f inFrame) bool {
	var data presenceData
	if err := json.Unmarshal(f.Data, &data); err != nil {
		// A known type carrying an unusable payload is ignored, like an
		// unknown field. Only the frames §2 names close the socket.
		return true
	}
	switch data.State {
	case presenceOnline, presenceAway:
		s.g.reportPresence(s.user.ID, data.State)
	default:
		// §3: offline is server-derived and cannot be claimed, and anything
		// else is not a state.
	}
	return true
}

// requireMember answers a channel-scoped operation's authorization question
// and reports whether the caller may proceed.
//
// A refusal is always channel_not_found, never forbidden: an unknown channel
// and one the caller is not in must be indistinguishable, or the socket
// becomes the one place a private channel's existence leaks. The socket
// stays open either way — a wrong subscribe is a client bug, not an attack
// worth dropping the connection over.
func (s *socket) requireMember(f inFrame) bool {
	allowed, err := s.g.canRead(s.ctx, s.user, *f.Chan)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("ws membership check", "chan", *f.Chan, "type", f.Type, "error", err)
		}
		s.sendError(f, codeInternalError, "could not check channel membership")
		return false
	}
	if !allowed {
		s.sendError(f, codeChannelNotFound, "no such channel")
		return false
	}
	return true
}

// canRead loads the facts a channel decision needs and asks the authz
// package. Membership is read now, for this frame, never taken from anything
// captured at connect (§3).
func (g *Gateway) canRead(ctx context.Context, user storage.User, channelID uuid.UUID) (bool, error) {
	ch, err := g.store.ChannelForUser(ctx, channelID, user.ID)
	if errors.Is(err, storage.ErrChannelNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	member, err := g.store.IsChannelMember(ctx, channelID, user.ID)
	if err != nil {
		return false, err
	}
	return authz.Can(ctx, &user, authz.ChannelRead, authz.NewChannel(ch, user.ID, member)), nil
}

// rateLimited spends one unit of this socket's frame budget and reports
// whether the frame must be refused. Exceeding the budget costs an error
// frame, not the connection (§8).
func (s *socket) rateLimited(f inFrame) bool {
	if s.frames.Limited(f.Type) {
		s.sendError(f, codeRateLimited, "too many frames, slow down")
		return true
	}
	s.frames.Record(f.Type)
	return false
}

func (s *socket) sendError(f inFrame, code, message string) {
	s.queue(encodeOrLog(outFrame{
		Type: typeError,
		ID:   f.ID,
		Chan: f.Chan,
		TS:   time.Now(),
		Data: errorData{Code: code, Message: message},
	}))
}

// writePump owns the write side of the connection: outbound frames, the
// heartbeat, the resyncs owed for dropped events, and the close frame.
func (s *socket) writePump() {
	ticker := time.NewTicker(s.g.heartbeatInterval)
	defer ticker.Stop()

	for {
		if !s.flushResync() {
			s.abort()
			return
		}

		select {
		case <-s.done:
			s.closeConn()
			return
		case frame := <-s.out:
			if !s.write(frame) {
				s.abort()
				return
			}
		case <-ticker.C:
			if time.Since(time.Unix(0, s.lastRX.Load())) > s.g.heartbeatTimeout {
				// §6: two consecutive missed pings. Silence is not health.
				s.shutdown(closeHeartbeat, "heartbeat timeout")
				continue
			}
			if !s.write(encodeOrLog(outFrame{Type: typePing, TS: time.Now()})) {
				s.abort()
				return
			}
		}
	}
}

// write sends one frame and reports success. A write that cannot complete
// within writeWait is a dead peer, not a slow one.
func (s *socket) write(frame []byte) bool {
	if frame == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()

	if err := s.conn.Write(ctx, websocket.MessageText, frame); err != nil {
		slog.Debug("ws write", "user_id", s.user.ID, "error", err)
		return false
	}
	return true
}

// flushResync tells the client to backfill the channels whose events this
// socket was too far behind to receive. The frames are written straight to
// the connection rather than queued, because the queue being full is exactly
// what produced them.
func (s *socket) flushResync() bool {
	s.mu.Lock()
	if len(s.resync) == 0 {
		s.mu.Unlock()
		return true
	}
	owed := make([]uuid.UUID, 0, len(s.resync))
	for channelID := range s.resync {
		owed = append(owed, channelID)
	}
	clear(s.resync)
	s.mu.Unlock()

	for _, channelID := range owed {
		id := channelID
		if !s.write(encodeOrLog(outFrame{
			Type: typeResync, Chan: &id, TS: time.Now(), Data: chanData{Chan: id},
		})) {
			return false
		}
	}
	return true
}

// closeConn sends the close frame for the code somebody named.
func (s *socket) closeConn() {
	if err := s.conn.Close(s.closeCode, s.closeReason); err != nil &&
		!errors.Is(err, net.ErrClosed) {
		slog.Debug("ws close handshake", "user_id", s.user.ID, "error", err)
	}
}

// abort ends the socket without a close frame, for the case where writing
// one is what just failed.
func (s *socket) abort() {
	s.shutdown(closeNormal, "")
	if err := s.conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Debug("ws abort", "user_id", s.user.ID, "error", err)
	}
}

// shutdown names the close code and signals the write pump to send it. The
// first caller wins: a socket closes once, with one reason, whoever noticed
// first — the sweeper, the heartbeat, or the read loop.
func (s *socket) shutdown(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		s.closeCode = code
		s.closeReason = reason
		close(s.done)
	})
}

// queue hands this socket one of its own replies. A full queue means the
// client has stopped reading; the reply is dropped rather than stalling the
// read loop that produced it, and the heartbeat will end the socket if the
// client really is gone.
func (s *socket) queue(frame []byte) {
	if frame == nil {
		return
	}
	select {
	case s.out <- frame:
	default:
		s.g.dropped.Add(1)
		slog.Warn("ws socket queue full, reply dropped", "user_id", s.user.ID)
	}
}

// deliver hands this socket one broadcast frame.
//
// A full queue drops the event and remembers the channel: the socket is a
// fast path, not a delivery guarantee (§5), and the client is told to
// backfill that channel over REST instead of being stalled, disconnected, or
// left silently short a message.
func (s *socket) deliver(channelID uuid.UUID, frame []byte) {
	if frame == nil {
		return
	}
	select {
	case s.out <- frame:
	default:
		s.g.dropped.Add(1)
		s.mu.Lock()
		s.resync[channelID] = struct{}{}
		s.mu.Unlock()
	}
}

// subscribed reports whether this socket opted into a channel's ephemeral
// events.
func (s *socket) subscribed(channelID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.subs[channelID]
	return ok
}

// unsubscribe drops a subscription, whether the client asked or the server
// decided (removal does not wait for the client to be polite).
func (s *socket) unsubscribe(channelID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, channelID)
}
