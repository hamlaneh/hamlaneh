package wsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// How long a test waits for a frame it expects, and how long it waits to
// convince itself a frame it must never see is not coming. The second is
// deliberately generous: a leak test that passes because it gave up early
// proves nothing.
const (
	waitFor = 3 * time.Second
	// presenceWaitFor is longer than waitFor because presence is the slowest
	// path here: a socket drop has to be noticed by the sweeper, a grace
	// timer has to be consulted, and the result fans out to the peer. Under
	// the whole module running with -race on a two-core CI runner, three
	// seconds was not always enough, and the resulting failure said nothing
	// about the state machine these tests exist to pin. Waiting longer for a
	// frame that must arrive is the same claim, not a weaker one.
	presenceWaitFor = 15 * time.Second
	waitNone        = 300 * time.Millisecond
)

// fakeStore is an in-memory stand-in for the five reads the gateway makes.
// Every field is guarded, because tests change membership and revoke
// sessions while sockets are live — which is the whole point.
type fakeStore struct {
	mu       sync.Mutex
	users    map[uuid.UUID]storage.User
	channels map[uuid.UUID]storage.Channel
	members  map[uuid.UUID]map[uuid.UUID]bool
	families map[uuid.UUID]map[uuid.UUID]bool

	// failMembership makes every membership read fail, so a test can prove
	// the gateway fails closed rather than open.
	failMembership bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    make(map[uuid.UUID]storage.User),
		channels: make(map[uuid.UUID]storage.Channel),
		members:  make(map[uuid.UUID]map[uuid.UUID]bool),
		families: make(map[uuid.UUID]map[uuid.UUID]bool),
	}
}

func (f *fakeStore) addUser(username string) storage.User {
	u := storage.User{ID: uuid.New(), Username: username, DisplayName: username}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	return u
}

// addFamily records a live session family for a user.
func (f *fakeStore) addFamily(userID uuid.UUID) uuid.UUID {
	familyID := uuid.New()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.families[userID] == nil {
		f.families[userID] = make(map[uuid.UUID]bool)
	}
	f.families[userID][familyID] = true
	return familyID
}

// revokeFamily is what logout, remote revocation and a password change all
// look like from the gateway's side: the family stops being live.
func (f *fakeStore) revokeFamily(userID, familyID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.families[userID], familyID)
}

func (f *fakeStore) addChannel(kind storage.ChannelKind, members ...uuid.UUID) storage.Channel {
	ch := storage.Channel{
		ID:          uuid.New(),
		Kind:        kind,
		Topic:       "",
		MemberCount: len(members),
		CreatedAt:   time.Now(),
	}
	if kind == storage.ChannelKindDM && len(members) == 2 {
		a, b := members[0], members[1]
		ch.DMUserA, ch.DMUserB = &a, &b
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[ch.ID] = ch
	f.members[ch.ID] = make(map[uuid.UUID]bool, len(members))
	for _, id := range members {
		f.members[ch.ID][id] = true
	}
	return ch
}

// removeMember is what the REST handler's removeChannelMember commits
// before it announces anything.
func (f *fakeStore) removeMember(channelID, userID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.members[channelID], userID)
}

func (f *fakeStore) IsChannelMember(_ context.Context, channelID, userID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMembership {
		return false, context.DeadlineExceeded
	}
	return f.members[channelID][userID], nil
}

func (f *fakeStore) ChannelForUser(_ context.Context, channelID, _ uuid.UUID) (storage.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.channels[channelID]
	if !ok {
		return storage.Channel{}, storage.ErrChannelNotFound
	}
	return ch, nil
}

func (f *fakeStore) ListChannelMembers(_ context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMembership {
		return nil, context.DeadlineExceeded
	}

	out := []storage.User{}
	for userID, member := range f.members[channelID] {
		if member {
			out = append(out, f.users[userID])
		}
	}
	slices.SortFunc(out, func(a, b storage.User) int {
		if c := strings.Compare(a.Username, b.Username); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	if params.After != nil {
		for i, u := range out {
			if u.Username > params.After.Username {
				out = out[i:]
				break
			}
		}
	}
	if len(out) > params.Limit {
		out = out[:params.Limit]
	}
	return out, nil
}

func (f *fakeStore) ListChannelsForUser(_ context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []storage.Channel{}
	for channelID, members := range f.members {
		if members[userID] {
			out = append(out, f.channels[channelID])
		}
	}
	slices.SortFunc(out, func(a, b storage.Channel) int {
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	if len(out) > params.Limit {
		out = out[:params.Limit]
	}
	return out, nil
}

func (f *fakeStore) ListSessionFamilies(_ context.Context, userID, _ uuid.UUID) ([]storage.SessionFamily, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := []storage.SessionFamily{}
	for familyID, live := range f.families[userID] {
		if live {
			out = append(out, storage.SessionFamily{FamilyID: familyID})
		}
	}
	return out, nil
}

// harness is a gateway behind a test HTTP server that performs the same
// gates httpserver.ConnectWebSocket does, in the same order: the strict
// Origin check, the §8 connect budget, and the hand-off with an
// authenticated principal.
//
// The budget is here for fidelity rather than to be tested here — a harness
// that skipped a gate the real endpoint runs would let a socket exist in
// these tests that could never exist in production. What it answers a
// refusal with is deliberately crude; the contract's 429 and its Retry-After
// are httpserver's to write, and httpstack_test.go proves it does.
type harness struct {
	t      *testing.T
	store  *fakeStore
	gw     *Gateway
	server *httptest.Server
	origin string

	stopOnce sync.Once
}

// shutdown closes the gateway and the test server. Tests that assert on
// teardown (the goroutine accounting) call it themselves; everything else
// gets it from t.Cleanup.
func (h *harness) shutdown() {
	h.stopOnce.Do(func() {
		if err := h.gw.Close(); err != nil {
			h.t.Errorf("close gateway: %v", err)
		}
		h.server.Close()
	})
}

const (
	testUserHeader   = "X-Test-User"
	testFamilyHeader = "X-Test-Family"
)

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()

	store := newFakeStore()
	h := &harness{t: t, store: store}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		if !h.gw.OriginAllowed(r.Header.Get("Origin")) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		userID := uuid.MustParse(r.Header.Get(testUserHeader))
		familyID := uuid.MustParse(r.Header.Get(testFamilyHeader))
		if _, allowed := h.gw.ConnectAllowed(familyID, r.RemoteAddr); !allowed {
			http.Error(w, "connect budget exhausted", http.StatusTooManyRequests)
			return
		}
		store.mu.Lock()
		user := store.users[userID]
		store.mu.Unlock()

		h.gw.ServeWebSocket(w, r, user, familyID)
	})

	h.server = httptest.NewServer(mux)
	h.origin = h.server.URL
	h.gw = New(store, h.origin, opts...)

	t.Cleanup(h.shutdown)
	return h
}

// wsClient is one connected test client. A reader goroutine drains the
// socket into a channel so a test can wait for a frame — or wait to be sure
// none is coming — without the read timeout tearing the connection down.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn

	in       chan map[string]any
	stop     chan struct{}
	readDone chan struct{}
	readErr  error
}

// dial opens a socket as user. origin overrides the Origin header; an empty
// string sends the instance's real one.
func (h *harness) dial(user storage.User, familyID uuid.UUID) *wsClient {
	h.t.Helper()
	return h.dialOrigin(user, familyID, h.origin)
}

func (h *harness) dialOrigin(user storage.User, familyID uuid.UUID, origin string) *wsClient {
	h.t.Helper()

	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	header.Set(testUserHeader, user.ID.String())
	header.Set(testFamilyHeader, familyID.String())

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, h.server.URL+"/api/v1/ws", &websocket.DialOptions{
		HTTPHeader: header,
	})
	if resp != nil && resp.Body != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			h.t.Errorf("close handshake response: %v", closeErr)
		}
	}
	if err != nil {
		if resp != nil {
			h.t.Fatalf("dial: %v (status %d)", err, resp.StatusCode)
		}
		h.t.Fatalf("dial: %v", err)
	}
	// Nothing in this protocol is bigger than a server frame, and the
	// default 32 KiB limit would truncate a large replay.
	conn.SetReadLimit(maxFrameBytes + 1)

	c := &wsClient{
		t:        h.t,
		conn:     conn,
		in:       make(chan map[string]any, 512),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	go c.read()

	h.t.Cleanup(func() {
		close(c.stop)
		if err := conn.CloseNow(); err != nil {
			// Already closed by the server in most tests.
			_ = err
		}
		<-c.readDone
	})
	return c
}

func (c *wsClient) read() {
	defer close(c.readDone)
	for {
		_, raw, err := c.conn.Read(context.Background())
		if err != nil {
			c.readErr = err
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			c.readErr = err
			return
		}
		select {
		case c.in <- frame:
		case <-c.stop:
			return
		}
	}
}

// send writes one client frame, filling in the required ts.
func (c *wsClient) send(frame map[string]any) {
	c.t.Helper()
	if _, ok := frame["ts"]; !ok {
		frame["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, ok := frame["data"]; !ok {
		frame["data"] = map[string]any{}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		c.t.Fatalf("marshal frame: %v", err)
	}
	c.sendRaw(raw)
}

func (c *wsClient) sendRaw(raw []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

// hello performs the handshake and returns the hello_ok payload.
func (c *wsClient) hello(resume ...resumedCursor) helloOKData {
	c.t.Helper()

	data := map[string]any{"protocol_version": protocolVersion}
	if len(resume) > 0 {
		entries := make([]map[string]any, 0, len(resume))
		for _, r := range resume {
			entries = append(entries, map[string]any{"chan": r.Chan.String(), "seq": r.Seq})
		}
		data["resume"] = entries
	}
	c.send(map[string]any{"type": typeHello, "id": "h1", "data": data})

	frame := c.expect(typeHelloOK)
	var out helloOKData
	remarshal(c.t, frame["data"], &out)
	return out
}

// expect returns the next frame of one of the given types, skipping the
// heartbeat pings that may arrive at any time.
func (c *wsClient) expect(types ...string) map[string]any {
	c.t.Helper()
	return c.expectWithin(waitFor, types...)
}

// expectWithin is expect with a budget the caller picks.
func (c *wsClient) expectWithin(budget time.Duration, types ...string) map[string]any {
	c.t.Helper()
	deadline := time.After(budget)
	for {
		select {
		case frame, ok := <-c.in:
			if !ok {
				c.t.Fatalf("socket closed while waiting for %v", types)
			}
			got, _ := frame["type"].(string)
			if got == typePing && !slices.Contains(types, typePing) {
				continue
			}
			if !slices.Contains(types, got) {
				c.t.Fatalf("got frame %q, want one of %v", got, types)
			}
			return frame
		case <-deadline:
			c.t.Fatalf("no %v frame within %s", types, budget)
			return nil
		}
	}
}

// expectNone fails if any frame other than a heartbeat ping arrives. This is
// the assertion the IDOR tests are made of, so it waits long enough to mean
// something.
func (c *wsClient) expectNone() {
	c.t.Helper()
	deadline := time.After(waitNone)
	for {
		select {
		case frame, ok := <-c.in:
			if !ok {
				return
			}
			if t, _ := frame["type"].(string); t == typePing {
				continue
			}
			c.t.Fatalf("received a frame that must never have been delivered: %v", frame)
		case <-deadline:
			return
		}
	}
}

// waitClose returns the close code the server sent.
func (c *wsClient) waitClose() websocket.StatusCode {
	c.t.Helper()
	return c.waitCloseWithin(waitFor)
}

func (c *wsClient) waitCloseWithin(timeout time.Duration) websocket.StatusCode {
	c.t.Helper()
	select {
	case <-c.readDone:
	case <-time.After(timeout):
		c.t.Fatalf("socket did not close within %s", timeout)
	}
	return websocket.CloseStatus(c.readErr)
}

// closeNow drops the socket the way a browser tab closing does.
func (c *wsClient) closeNow() {
	c.t.Helper()
	if err := c.conn.CloseNow(); err != nil {
		c.t.Logf("close: %v", err)
	}
	select {
	case <-c.readDone:
	case <-time.After(waitFor):
		c.t.Fatal("client read loop did not stop after close")
	}
}

// waitForSeq blocks until the dispatcher has assigned a channel at least
// this sequence number, so a test can announce events and then reconnect
// without racing the queue.
func (h *harness) waitForSeq(channelID uuid.UUID, seq uint64) {
	h.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		h.gw.mu.Lock()
		buf := h.gw.channels[channelID]
		var got uint64
		if buf != nil {
			got = buf.lastSeq
		}
		h.gw.mu.Unlock()
		if got >= seq {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("channel %s reached seq %d, want %d", channelID, got, seq)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// remarshal moves a decoded frame payload into a typed struct.
func remarshal(t *testing.T, from any, into any) {
	t.Helper()
	raw, err := json.Marshal(from)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}

// frameSeq reads the seq field of a decoded frame.
func frameSeq(t *testing.T, frame map[string]any) uint64 {
	t.Helper()
	seq, ok := frame["seq"].(float64)
	if !ok {
		t.Fatalf("frame carries no seq: %v", frame)
	}
	return uint64(seq)
}

// frameChan reads the chan field of a decoded frame.
func frameChan(t *testing.T, frame map[string]any) uuid.UUID {
	t.Helper()
	s, ok := frame["chan"].(string)
	if !ok {
		t.Fatalf("frame has no chan: %v", frame)
	}
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("frame chan is not a uuid: %v", err)
	}
	return id
}

// testNow is the clock the tests hand to gateway methods that take one.
func testNow() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// testMessage builds a stored message for a channel.
func testMessage(channelID uuid.UUID, author storage.User, content string) storage.Message {
	return storage.Message{
		ID:        uuid.New(),
		ChannelID: channelID,
		Author: storage.MessageAuthor{
			ID: author.ID, Username: author.Username, DisplayName: author.DisplayName,
		},
		ClientMsgID: uuid.New(),
		Content:     content,
		CreatedAt:   time.Now(),
	}
}
