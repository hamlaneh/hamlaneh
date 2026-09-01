package wsgateway

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestRevokedSessionClosesSocketWithinBudget is (c), on the production sweep
// interval — the point of the test is the §7 budget, not the mechanism, so
// it deliberately does not speed the sweep up.
//
// The socket sends nothing after its hello. That is the case the budget
// exists for: the check is a sweep on a timer, so a socket that is receiving
// nothing and sending nothing still dies on schedule, and an idle session
// cannot outlive its revocation by staying quiet.
func TestRevokedSessionClosesSocketWithinBudget(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	familyID := h.store.addFamily(alice.ID)

	c := h.dial(alice, familyID)
	c.hello()

	revokedAt := time.Now()
	h.store.revokeFamily(alice.ID, familyID)

	code := c.waitCloseWithin(15 * time.Second)
	elapsed := time.Since(revokedAt)

	if code != closeUnauthorized {
		t.Fatalf("close code = %d, want %d", code, closeUnauthorized)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("socket survived revocation for %s, budget is 10s", elapsed)
	}
	t.Logf("socket closed %s after revocation", elapsed.Round(time.Millisecond))
}

// TestRevocationClosesEverySocketOfTheFamilyAndNoOther: revocation is per
// family, so the user's other device stays connected.
func TestRevocationClosesEverySocketOfTheFamilyAndNoOther(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithSweepInterval(20*time.Millisecond))
	alice := h.store.addUser("alice")
	laptop := h.store.addFamily(alice.ID)
	phone := h.store.addFamily(alice.ID)

	tab1 := h.dial(alice, laptop)
	tab1.hello()
	tab2 := h.dial(alice, laptop)
	tab2.hello()
	other := h.dial(alice, phone)
	other.hello()

	h.store.revokeFamily(alice.ID, laptop)

	for i, c := range []*wsClient{tab1, tab2} {
		if code := c.waitClose(); code != closeUnauthorized {
			t.Fatalf("socket %d close code = %d, want %d", i, code, closeUnauthorized)
		}
	}
	other.send(map[string]any{"type": typePing, "id": "alive"})
	if got := other.expect(typePong); got["id"] != "alive" {
		t.Error("revoking one family closed another family's socket")
	}
}

// TestGatewayCloseClosesEverySocket: a deploy is 1001, which the client
// treats as routine and reconnects from with backoff.
func TestGatewayCloseClosesEverySocket(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")

	first := h.dial(alice, h.store.addFamily(alice.ID))
	first.hello()
	second := h.dial(bob, h.store.addFamily(bob.ID))
	second.hello()

	h.shutdown()

	for i, c := range []*wsClient{first, second} {
		if code := c.waitClose(); code != closeGoingAway {
			t.Fatalf("socket %d close code = %d, want %d", i, code, closeGoingAway)
		}
	}
}

// TestSocketsReleaseEverything is the leak assertion: a closed socket
// releases every goroutine it owned, and so does the gateway.
//
// It is deliberately not parallel — it counts goroutines, and that only
// means something when nothing else in the package is running.
func TestSocketsReleaseEverything(t *testing.T) {
	baseline := settledGoroutines()

	h := newHarness(t)
	alice := h.store.addUser("alice")
	bob := h.store.addUser("bob")
	ch := h.store.addChannel(storage.ChannelKindPrivate, alice.ID, bob.ID)

	clients := make([]*wsClient, 0, 8)
	for range 8 {
		c := h.dial(bob, h.store.addFamily(bob.ID))
		c.hello()
		c.send(map[string]any{"type": typeSubscribe, "id": "s", "chan": ch.ID.String()})
		c.expect(typeSubscribed)
		clients = append(clients, c)
	}

	h.gw.MessageCreated(ch.ID, testMessage(ch.ID, alice, "traffic"))
	for _, c := range clients {
		c.expect(typeMessageCreated)
	}

	for _, c := range clients {
		c.closeNow()
	}
	h.shutdown()

	if got := h.gw.connectedUsers(); got != 0 {
		t.Errorf("gateway still holds sockets for %d users", got)
	}
	waitForGoroutines(t, baseline)
}

// TestConcurrentConnectDisconnectBroadcast is the race-detector workout:
// sockets opening and closing while events are being fanned out.
func TestConcurrentConnectDisconnectBroadcast(t *testing.T) {
	t.Parallel()

	h := newHarness(t, WithSweepInterval(10*time.Millisecond))
	author := h.store.addUser("author")

	const (
		workers = 8
		rounds  = 6
	)
	users := make([]storage.User, 0, workers)
	channels := make([]storage.Channel, 0, workers)
	for i := range workers {
		u := h.store.addUser("worker" + string(rune('a'+i)))
		users = append(users, u)
		channels = append(channels, h.store.addChannel(storage.ChannelKindPrivate, author.ID, u.ID))
	}

	stop := make(chan struct{})
	var producers sync.WaitGroup
	producers.Add(1)
	go func() {
		defer producers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, ch := range channels {
				h.gw.MessageCreated(ch.ID, testMessage(ch.ID, author, "hello"))
				h.gw.ReadPosition(author.ID, ch.ID, uuid.New(), time.Now())
			}
			time.Sleep(time.Millisecond)
		}
	}()

	errs := make(chan error, workers*rounds)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if err := churn(h, users[i], channels[i]); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	producers.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("worker: %v", err)
	}
}

// churn opens a socket, uses it, and drops it — the shape a flaky network
// produces all day.
func churn(h *harness, user storage.User, ch storage.Channel) error {
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	header := http.Header{}
	header.Set("Origin", h.origin)
	header.Set(testUserHeader, user.ID.String())
	header.Set(testFamilyHeader, h.store.addFamily(user.ID).String())

	conn, resp, err := websocket.Dial(ctx, h.server.URL+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			return closeErr
		}
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxFrameBytes + 1)

	hello := `{"type":"hello","id":"1","ts":"2026-08-21T09:12:00Z","data":{"protocol_version":1}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		return err
	}
	subscribe := `{"type":"subscribe","id":"2","chan":"` + ch.ID.String() +
		`","ts":"2026-08-21T09:12:00Z","data":{}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(subscribe)); err != nil {
		return err
	}
	typing := `{"type":"typing","chan":"` + ch.ID.String() +
		`","ts":"2026-08-21T09:12:00Z","data":{}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(typing)); err != nil {
		return err
	}

	// Drain a few frames so the socket is doing real work while it is torn
	// down; what arrives is asserted elsewhere.
	for range 3 {
		if _, _, err := conn.Read(ctx); err != nil {
			return err
		}
	}
	return nil
}

// settledGoroutines returns the goroutine count once it has stopped moving,
// so a straggler from an earlier test is not counted as this test's leak.
func settledGoroutines() int {
	previous := -1
	for range 50 {
		runtime.GC()
		current := runtime.NumGoroutine()
		if current == previous {
			return current
		}
		previous = current
		time.Sleep(20 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// waitForGoroutines fails unless the count comes back down to the baseline.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime.GC()
		if got := runtime.NumGoroutine(); got <= baseline {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutines did not return to the baseline of %d (now %d):\n%s",
				baseline, runtime.NumGoroutine(), buf[:n])
		}
		time.Sleep(20 * time.Millisecond)
	}
}
