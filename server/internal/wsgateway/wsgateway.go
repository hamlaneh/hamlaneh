// Package wsgateway implements the Hamlaneh realtime WebSocket gateway
// specified in docs/api/ws-protocol.md: the socket that puts a message on
// somebody else's screen without a refresh.
//
// # What owns what
//
// A Gateway owns the socket registry, the per-channel sequence counters and
// replay buffers, and two goroutines: a dispatcher that turns one event into
// its audience, and a sweeper that runs the three periodic jobs (session
// revocation, presence expiry, replay pruning). Everything those two touch
// is guarded by Gateway.mu.
//
// A socket owns one connection and two goroutines: the read loop (the HTTP
// handler's own goroutine) and a write pump. The write pump is the only
// writer of the WebSocket, which is what makes "close with code X" a single
// well-defined act rather than a race between whoever noticed first.
//
// Lock order is Gateway.mu -> socket.mu, and nothing takes a lock while it
// waits on I/O: the dispatcher reads membership from storage first and only
// then takes Gateway.mu to assign a sequence number, buffer the frame and
// hand it to the sockets.
//
// # What this package refuses to do
//
// Writes are REST (§3). Nothing here creates, edits or deletes anything; the
// socket is a delivery channel, and the only inbound frames it honours are
// subscribe, unsubscribe, typing, presence, ping and pong.
//
// Membership is re-read from storage for every channel-scoped decision and
// every delivery, never captured at connect (§3, §4). A user removed from a
// channel mid-socket stops receiving its events and stops being able to act
// on it immediately, and a resume list is a client's claim about the past
// that is re-checked against the present (§5).
package wsgateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Protocol constants from docs/api/ws-protocol.md. maxFrameBytes matches the
// REST body cap from slice 1.1a; the heartbeat values are advertised in
// hello_ok so they can move without a protocol version bump.
const (
	protocolVersion = 1

	// maxFrameBytes is the §2 cap. A larger frame closes the socket with
	// 4413 and is never fully read.
	maxFrameBytes = 64 << 10

	// heartbeatInterval is how often the server sends ping (§6), and
	// heartbeatTimeout is two missed pings' worth of silence.
	heartbeatInterval = 30 * time.Second
	heartbeatTimeout  = 75 * time.Second

	// helloTimeout closes a socket that opens and then says nothing (§1).
	helloTimeout = 10 * time.Second

	// writeWait bounds one outbound frame. A write that cannot complete in
	// this long is a dead peer, not a slow one.
	writeWait = 10 * time.Second
)

// Tuning that is not in the protocol.
const (
	// defaultSweepInterval paces the revocation sweep. §7 gives a hard
	// ten-second budget for closing the sockets of a revoked family, and
	// the sweep is what spends it: a tick every five seconds leaves the
	// whole second half of the budget for the query and the close.
	defaultSweepInterval = 5 * time.Second

	// defaultPresenceGrace is how long a user with no sockets stays online
	// before the sweep announces them offline (§4). It exists so a reload or
	// a tunnel blip does not flash the peer offline.
	defaultPresenceGrace = 10 * time.Second

	// outboundBuffer is how many frames one socket may fall behind by
	// before the gateway starts dropping its events. See socket.deliver:
	// a drop is answered with a resync for that channel, never with a
	// stalled sender.
	outboundBuffer = 256

	// eventQueue is how many announced events may be waiting for the
	// dispatcher. Realtime methods run on HTTP handler goroutines serving
	// other users' requests and must never block, so a full queue drops.
	eventQueue = 1024

	// memberPage is one page of a channel's member list.
	memberPage = 200

	// channelPage is one page of a user's channel list.
	channelPage = 200

	// maxResumeChannels bounds the membership re-checks one hello can ask
	// for. Channels past the cap are answered with resync, which reveals
	// nothing and costs the client only a REST backfill (§5).
	maxResumeChannels = 64
)

// Store is everything the gateway reads from persistent storage.
// *storage.Store satisfies it; tests substitute a fake.
//
// It is declared here rather than imported because this package is the
// consumer: the gateway needs five reads and no writes, and saying so is
// what keeps a delivery path from ever mutating anything.
type Store interface {
	// IsChannelMember is the membership fact every channel-scoped decision
	// turns on, asked afresh every time (§3).
	IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error)
	// ChannelForUser loads the channel a decision is about. It applies no
	// visibility check of its own — that is what IsChannelMember and
	// authz.Can are for.
	ChannelForUser(ctx context.Context, channelID, userID uuid.UUID) (storage.Channel, error)
	// ListChannelMembers resolves a membership-scoped event's audience at
	// send time.
	ListChannelMembers(ctx context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error)
	// ListChannelsForUser finds the DM channels a presence announcement may
	// legally reach (§4: presence is DM-scoped).
	ListChannelsForUser(ctx context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error)
	// ListSessionFamilies returns the caller's live session families. The
	// revocation sweep asks it whether a socket's family is still one of
	// them; a family that has been logged out, revoked, password-changed
	// away or simply expired is not.
	ListSessionFamilies(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error)
}

// Option configures a Gateway.
type Option func(*Gateway)

// WithSweepInterval overrides the revocation sweep's period. It exists for
// tests that need the mechanism without the wait; the §7 budget test
// deliberately runs on the production default.
func WithSweepInterval(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.sweepInterval = d
		}
	}
}

// WithHeartbeat overrides the §6 ping period and the silence budget. The
// protocol advertises the interval in hello_ok precisely so it can move
// without a version bump; tests use it so proving the timeout does not cost
// them seventy-five seconds.
func WithHeartbeat(interval, timeout time.Duration) Option {
	return func(g *Gateway) {
		if interval > 0 && timeout > 0 {
			g.heartbeatInterval = interval
			g.heartbeatTimeout = timeout
		}
	}
}

// WithHelloTimeout overrides how long a socket may stay silent after opening
// before §1's 4400 closes it.
func WithHelloTimeout(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.helloTimeout = d
		}
	}
}

// WithConnectClock replaces the clock the §8 connect budget's windows run on.
// It exists for tests that need a minute of sliding window without a minute of
// waiting, and for the one test that has to replay ten minutes of a client's
// reconnect backoff against the production limits.
func WithConnectClock(now func() time.Time) Option {
	return func(g *Gateway) {
		if now != nil {
			g.connectNow = now
		}
	}
}

// WithPresenceGrace overrides how long a user with no sockets stays online
// before the sweep announces them offline (§4).
func WithPresenceGrace(d time.Duration) Option {
	return func(g *Gateway) {
		if d > 0 {
			g.presenceGrace = d
		}
	}
}

// Gateway is the realtime gateway: an httpserver.Realtime implementation
// plus the upgrade endpoint's socket handling.
type Gateway struct {
	store  Store
	origin string

	sweepInterval     time.Duration
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	helloTimeout      time.Duration
	presenceGrace     time.Duration

	// The §8 connect budget. See connectbudget.go for the numbers and the
	// reconnect arithmetic behind them. connectNow is the clock both windows
	// run on; only tests replace it.
	connectNow      func() time.Time
	connectByFamily *ratelimit.Limiter
	connectByIP     *ratelimit.Limiter

	events chan event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	stopOnce sync.Once
	// stopped is the cheap check ServeWebSocket makes before upgrading
	// anything; closing is the authoritative one, taken under mu together
	// with the socket admission it guards.
	stopped atomic.Bool
	closing bool

	// dropped counts events the gateway threw away because a queue was
	// full. It is a fast-path health signal, not a delivery record.
	dropped atomic.Int64

	// conns counts sockets currently being served, so Close can wait for
	// them. It is only ever incremented under mu, while stopped is false,
	// which is what makes waiting on it safe.
	conns sync.WaitGroup

	mu sync.Mutex
	// live is every socket this gateway is serving, including the ones that
	// have not said hello yet. Shutdown works from this set; delivery works
	// from sockets below, which only holds the ones past the handshake.
	live map[*socket]struct{}
	// sockets indexes live sockets by user id. A user with no sockets has
	// no entry, so len(sockets) is the connected-user count.
	sockets map[uuid.UUID]map[*socket]struct{}
	// channels holds the per-channel sequence counter and replay buffer.
	channels map[uuid.UUID]*replayBuffer
	// presence holds each user's last announced presence state.
	presence map[uuid.UUID]*presenceState
}

// presenceState is one user's presence as the gateway last announced it.
// goneAt is the zero time while the user still has a socket.
type presenceState struct {
	state  string
	goneAt time.Time
}

// New returns a running gateway. origin is the instance's configured public
// origin ("https://chat.example.com"); every WebSocket handshake must carry
// exactly that Origin (§1). An empty origin allows nothing: with no origin
// to compare against, no handshake can be shown to be same-site, and
// refusing is the only fail-closed answer.
//
// The caller owns the gateway and must Close it.
func New(store Store, origin string, opts ...Option) *Gateway {
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		store:             store,
		origin:            normalizeOrigin(origin),
		sweepInterval:     defaultSweepInterval,
		heartbeatInterval: heartbeatInterval,
		heartbeatTimeout:  heartbeatTimeout,
		helloTimeout:      helloTimeout,
		presenceGrace:     defaultPresenceGrace,
		connectNow:        time.Now,
		events:            make(chan event, eventQueue),
		ctx:               ctx,
		cancel:            cancel,
		live:              make(map[*socket]struct{}),
		sockets:           make(map[uuid.UUID]map[*socket]struct{}),
		channels:          make(map[uuid.UUID]*replayBuffer),
		presence:          make(map[uuid.UUID]*presenceState),
	}
	for _, opt := range opts {
		opt(g)
	}
	// After the options, so a replaced clock reaches the windows.
	g.connectByFamily, g.connectByIP = newConnectLimiters(g.connectNow)

	g.wg.Add(2)
	go g.dispatchLoop()
	go g.sweepLoop()
	return g
}

// normalizeOrigin reduces the configured public URL to the origin a browser
// actually sends: scheme and host, no path, and no port when it is the
// scheme's default.
//
// The instance is configured with a URL, not an origin — HAMLANEH_PUBLIC_URL
// may carry a sub-path (deploy/.env.example) — and an Origin header carries
// neither a path nor a redundant :443. Comparing the raw value would refuse
// every handshake on those installs, and a WebSocket that 403s for a reason
// nobody can see is worse than one that never shipped.
//
// A value that cannot be parsed into a scheme and a host becomes the empty
// origin, which allows nothing. Failing closed on a misconfiguration is the
// only safe direction for the check that stands in for CSRF here.
func normalizeOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}

	host := u.Host
	switch {
	case strings.EqualFold(u.Scheme, "https") && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	case strings.EqualFold(u.Scheme, "http") && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host
}

// OriginAllowed reports whether an Origin header may open a socket: present,
// not "null", and equal to the configured public origin. Case is ignored
// because scheme and host are case-insensitive; nothing else is. There is no
// wildcard, no substring match and no registrable-domain relaxation, and
// there must never be one — this check stands in for the CSRF header a
// browser cannot send on a WebSocket handshake.
func (g *Gateway) OriginAllowed(origin string) bool {
	if g.origin == "" || origin == "" || origin == "null" {
		return false
	}
	return strings.EqualFold(origin, g.origin)
}

// Close stops the gateway: every socket is closed with 1001 (going away, the
// routine reconnect-with-backoff case), and every goroutine it owned is gone
// by the time it returns. It is safe to call more than once.
//
// Waiting matters on a deploy: http.Server.Shutdown does not wait for
// hijacked connections, so without this the process could exit between the
// close frame being queued and it being written, and every client would see
// a dropped socket instead of the 1001 that tells it to reconnect calmly.
func (g *Gateway) Close() error {
	g.stopOnce.Do(func() {
		g.stopped.Store(true)

		g.mu.Lock()
		g.closing = true
		sockets := make([]*socket, 0, len(g.live))
		for s := range g.live {
			sockets = append(sockets, s)
		}
		g.mu.Unlock()

		for _, s := range sockets {
			s.shutdown(closeGoingAway, "server shutting down")
		}
		g.conns.Wait()

		g.cancel()
		g.wg.Wait()
	})
	return nil
}

// track admits one socket for the gateway to serve, or reports false once
// the gateway is closing. The admission and the wait-group increment share
// one critical section with the closing flag, which is what lets Close wait
// on conns without racing a socket that is arriving at the same moment.
func (g *Gateway) track(s *socket) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.closing {
		return false
	}
	g.live[s] = struct{}{}
	g.conns.Add(1)
	return true
}

// untrack releases a socket the gateway was serving.
func (g *Gateway) untrack(s *socket) {
	g.mu.Lock()
	delete(g.live, s)
	g.mu.Unlock()
	g.conns.Done()
}

// registerAndReplay decides which of the client's resume candidates the
// buffer can actually satisfy, sends hello_ok, replays what it promised, and
// adds the socket to the registry. It reports whether the user was
// previously offline.
//
// All of it happens under one lock hold, and so does every delivery, so an
// event is either already in the replay this socket just received or
// delivered to it afterwards. There is no window between the two where an
// event could fall through, and hello_ok cannot promise a replay that the
// window drops a microsecond later.
//
// sendHelloOK is called under the lock. It encodes one frame and queues it;
// it must not do I/O.
func (g *Gateway) registerAndReplay(
	s *socket,
	candidates []resumeRequest,
	extraResync []uuid.UUID,
	sendHelloOK func(resumed []resumedCursor, resync []uuid.UUID),
) (wasOffline bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	resumed := []resumedCursor{}
	resync := append([]uuid.UUID{}, extraResync...)
	replay := make([]resumeRequest, 0, len(candidates))
	for _, req := range candidates {
		buf := g.channels[req.channelID]
		if buf == nil || !buf.canResume(req.seq) {
			resync = append(resync, req.channelID)
			continue
		}
		resumed = append(resumed, resumedCursor{Chan: req.channelID, Seq: req.seq})
		replay = append(replay, req)
	}

	sendHelloOK(resumed, resync)

	for _, req := range replay {
		for _, frame := range g.channels[req.channelID].after(req.seq) {
			s.deliver(req.channelID, frame)
		}
	}

	set := g.sockets[s.user.ID]
	if set == nil {
		set = make(map[*socket]struct{})
		g.sockets[s.user.ID] = set
		wasOffline = true
	}
	set[s] = struct{}{}

	if wasOffline {
		// A user who was away, went offline entirely and came back is online
		// again. Leaving the old state here would announce online while
		// recording away, and the next away report would then look like no
		// change at all and never reach the peer.
		g.presence[s.user.ID] = &presenceState{state: presenceOnline}
	} else if p := g.presence[s.user.ID]; p != nil {
		p.goneAt = time.Time{}
	}
	return wasOffline
}

// deregister removes a socket and, if it was the user's last one, starts
// their presence grace period. The user is not announced offline here: §4
// gives that short grace so a reload does not flash the peer offline, and
// the sweep is what spends it.
func (g *Gateway) deregister(s *socket) {
	g.mu.Lock()
	defer g.mu.Unlock()

	set := g.sockets[s.user.ID]
	if set == nil {
		return
	}
	delete(set, s)
	if len(set) > 0 {
		return
	}
	delete(g.sockets, s.user.ID)
	if p := g.presence[s.user.ID]; p != nil {
		p.goneAt = time.Now()
	}
}

// socketsFor returns one user's live sockets.
func (g *Gateway) socketsFor(userID uuid.UUID) []*socket {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.socketsForLocked(userID)
}

func (g *Gateway) socketsForLocked(userID uuid.UUID) []*socket {
	set := g.sockets[userID]
	if len(set) == 0 {
		return nil
	}
	out := make([]*socket, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// connectedUsers reports whether anybody is connected at all. An idle
// instance must not run a membership query per announced event.
func (g *Gateway) connectedUsers() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sockets)
}

// sweepLoop runs the three periodic jobs. One ticker, because all three are
// cheap and none of them wants its own goroutine: closing the sockets of
// revoked session families (§7), announcing users offline once their grace
// period has passed (§4), and dropping replay buffers nothing can resume
// from any more (§5).
func (g *Gateway) sweepLoop() {
	defer g.wg.Done()

	ticker := time.NewTicker(g.sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-g.ctx.Done():
			return
		case <-ticker.C:
			g.closeRevoked()
			g.expirePresence()
			g.pruneReplay()
		}
	}
}

// closeRevoked closes every socket whose session family is no longer live.
//
// It runs on a timer rather than on the next inbound frame on purpose: an
// idle socket sends nothing, and a revoked session that survives because
// nobody typed is exactly the hole §7's ten-second budget exists to close.
// The sockets are snapshotted BEFORE the families are read, and only the
// snapshot is judged. Reading the families first would let a socket that
// signed in on a brand new family in between be measured against a list
// taken before that family existed — and closed with 4401, which tells a
// client to stop reconnecting and go back to sign-in. A socket that appears
// after the snapshot is simply this sweep's business next tick, well inside
// the budget.
func (g *Gateway) closeRevoked() {
	g.mu.Lock()
	byUser := make(map[uuid.UUID][]*socket, len(g.sockets))
	for userID := range g.sockets {
		byUser[userID] = g.socketsForLocked(userID)
	}
	g.mu.Unlock()

	for userID, sockets := range byUser {
		// uuid.Nil marks no family as "current": the flag is for the
		// settings screen, and the sweep only wants the live set.
		families, err := g.store.ListSessionFamilies(g.ctx, userID, uuid.Nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("ws revocation sweep", "user_id", userID, "error", err)
			}
			// Fail open for one tick rather than closing every socket of a
			// user because one query failed. The next tick retries; a
			// database that stays down takes the REST API with it anyway.
			continue
		}

		live := make(map[uuid.UUID]struct{}, len(families))
		for _, fam := range families {
			live[fam.FamilyID] = struct{}{}
		}
		for _, s := range sockets {
			if _, ok := live[s.familyID]; !ok {
				s.shutdown(closeUnauthorized, "session revoked")
			}
		}
	}
}

// expirePresence announces offline for the users whose grace period has run
// out.
func (g *Gateway) expirePresence() {
	now := time.Now()

	g.mu.Lock()
	var gone []uuid.UUID
	for userID, p := range g.presence {
		if p.goneAt.IsZero() || now.Sub(p.goneAt) < g.presenceGrace {
			continue
		}
		gone = append(gone, userID)
		delete(g.presence, userID)
	}
	g.mu.Unlock()

	for _, userID := range gone {
		g.announcePresence(userID, presenceOffline)
	}
}

// pruneReplay drops buffered events older than the §5 window. The channels
// themselves are kept, because their sequence counters are — see
// replayBuffer.
func (g *Gateway) pruneReplay() {
	cutoff := time.Now().Add(-replayMaxAge)

	g.mu.Lock()
	defer g.mu.Unlock()
	for _, buf := range g.channels {
		buf.prune(cutoff)
	}
}

// ServeWebSocket upgrades an already-authenticated request and serves the
// socket until it closes. The caller has verified the session and the Origin
// (see httpserver.ConnectWebSocket); this method assumes both.
func (g *Gateway) ServeWebSocket(w http.ResponseWriter, r *http.Request, user storage.User, familyID uuid.UUID) {
	if g.stopped.Load() {
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	g.serve(w, r, user, familyID)
}
