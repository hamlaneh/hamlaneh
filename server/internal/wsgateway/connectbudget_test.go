package wsgateway

import (
	"math/rand/v2"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// newBudgetGateway returns a gateway that exists only for its connect budget:
// no socket is ever opened on it, so the store is never read.
func newBudgetGateway(t *testing.T, opts ...Option) *Gateway {
	t.Helper()

	g := New(newFakeStore(), "https://chat.example.com", opts...)
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})
	return g
}

// TestConnectBudgetRefusesAtItsLimit: the last connect inside the budget is
// admitted and the next one is not, and the refusal carries a positive wait
// so the handshake's Retry-After is an honest number rather than a guess.
func TestConnectBudgetRefusesAtItsLimit(t *testing.T) {
	t.Parallel()

	g := newBudgetGateway(t)
	family, ip := uuid.New(), "203.0.113.7"

	for i := range connectPerFamily {
		if _, allowed := g.ConnectAllowed(family, ip); !allowed {
			t.Fatalf("connect %d of %d was refused inside the budget", i+1, connectPerFamily)
		}
	}

	retryAfter, allowed := g.ConnectAllowed(family, ip)
	if allowed {
		t.Fatalf("connect %d was admitted, budget is %d", connectPerFamily+1, connectPerFamily)
	}
	if retryAfter <= 0 {
		t.Fatalf("retry-after on a refusal = %s, want a positive wait", retryAfter)
	}
	if retryAfter > connectWindow {
		t.Fatalf("retry-after = %s, longer than the %s window", retryAfter, connectWindow)
	}
}

// exhaustFamily spends one session family's whole budget from one address.
func exhaustFamily(t *testing.T, g *Gateway, family uuid.UUID, ip string) {
	t.Helper()

	for i := range connectPerFamily {
		if _, allowed := g.ConnectAllowed(family, ip); !allowed {
			t.Fatalf("connect %d of %d refused while filling the family budget", i+1, connectPerFamily)
		}
	}
}

// exhaustIP fills one address's whole budget, taking a fresh session family
// every connectPerFamily connects so the family half never bites first. That
// is also what the address half is for: many signed-in devices, one NAT.
func exhaustIP(t *testing.T, g *Gateway, ip string) {
	t.Helper()

	family := uuid.New()
	for spent := range connectPerIP {
		if spent > 0 && spent%connectPerFamily == 0 {
			family = uuid.New()
		}
		if _, allowed := g.ConnectAllowed(family, ip); !allowed {
			t.Fatalf("connect %d of %d refused while filling the address budget", spent+1, connectPerIP)
		}
	}
}

// TestConnectBudgetKeysAreIndependent: §8 keys the budget per session family
// AND per IP, which is only true if exhausting one key leaves the other
// alone. A single shared window would make the looser address budget
// unreachable and let one device's reconnect storm lock out every colleague
// behind the same NAT.
func TestConnectBudgetKeysAreIndependent(t *testing.T) {
	t.Parallel()

	t.Run("a second family on the flooded address", func(t *testing.T) {
		t.Parallel()

		g := newBudgetGateway(t)
		const ip = "203.0.113.7"

		flooded := uuid.New()
		exhaustFamily(t, g, flooded, ip)
		if _, allowed := g.ConnectAllowed(flooded, ip); allowed {
			t.Fatal("the flooding family was still admitted past its budget")
		}

		if _, allowed := g.ConnectAllowed(uuid.New(), ip); !allowed {
			t.Fatal("a second session family on the same address inherited the first one's refusal")
		}
	})

	t.Run("the same family from a second address", func(t *testing.T) {
		t.Parallel()

		g := newBudgetGateway(t)
		const flooded = "203.0.113.7"

		exhaustIP(t, g, flooded)
		if _, allowed := g.ConnectAllowed(uuid.New(), flooded); allowed {
			t.Fatal("the flooding address was still admitted past its budget")
		}

		if _, allowed := g.ConnectAllowed(uuid.New(), "198.51.100.4"); !allowed {
			t.Fatal("a second address inherited the first one's refusal")
		}
	})
}

// testClock is a hand-cranked clock for the windows, so proving a minute of
// sliding does not cost a minute. It is atomic because the connect budget is
// spent on whichever goroutine is serving the handshake.
type testClock struct{ nanos atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}

func (c *testClock) now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }
func (c *testClock) set(t time.Time)         { c.nanos.Store(t.UnixNano()) }

// TestConnectBudgetWindowSlides: a refused caller is admitted again once its
// oldest connects age out. The budget bounds a rate; it is not a lockout, and
// the Retry-After it hands out is the real wait rather than a padded guess —
// the caller is still refused just before it elapses and admitted just after.
func TestConnectBudgetWindowSlides(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	g := newBudgetGateway(t, WithConnectClock(clock.now))
	family, ip := uuid.New(), "203.0.113.7"

	exhaustFamily(t, g, family, ip)
	retryAfter, allowed := g.ConnectAllowed(family, ip)
	if allowed {
		t.Fatal("admitted past the budget")
	}

	clock.advance(retryAfter - time.Millisecond)
	if _, allowed := g.ConnectAllowed(family, ip); allowed {
		t.Fatalf("admitted %s early, before the %s it was told to wait", time.Millisecond, retryAfter)
	}

	clock.advance(2 * time.Millisecond)
	if _, allowed := g.ConnectAllowed(family, ip); !allowed {
		t.Fatalf("still refused after the whole %s wait elapsed", retryAfter)
	}
}

// documentedBackoff is the reconnect schedule ws-protocol.md §8 specifies and
// webapp/src/chat/realtime.ts implements: exponential backoff with full
// jitter, a one-second base and a thirty-second cap. It is restated here
// rather than imported because the claim under test is about the protocol the
// server promises to tolerate, not about one client's source file.
func documentedBackoff(attempt int, rnd *rand.Rand) time.Duration {
	ceiling := min(backoffCap, time.Second<<min(attempt, backoffSaturatedAttempt))
	return time.Duration(rnd.Float64() * float64(ceiling))
}

const (
	backoffCap = 30 * time.Second
	// backoffSaturatedAttempt is where 1s << attempt first passes the cap.
	backoffSaturatedAttempt = 5
)

// reconnectingClient is one RealtimeClient riding out an outage: it never
// reaches hello_ok, so its attempt counter only ever grows, exactly as
// realtime.ts leaves it.
type reconnectingClient struct {
	family uuid.UUID

	attempt int
	nextAt  time.Time

	// at records when this client asked, so the test can report the peak rate
	// it actually produced against the budget that allowed it.
	at []time.Time
}

// peakInWindow is the most entries any window-sized span of times holds.
// times must be ascending, which a simulation that only ever moves forward
// guarantees.
func peakInWindow(times []time.Time, window time.Duration) int {
	peak, start := 0, 0
	for end := range times {
		for times[end].Sub(times[start]) >= window {
			start++
		}
		peak = max(peak, end-start+1)
	}
	return peak
}

// TestHonestReconnectIsNeverRefused is the test that fixes the numbers.
//
// It replays the documented backoff — the real schedule, with real jitter,
// against the production limits — through a five-minute outage, fifty times
// per shape with a different jitter stream each time, and fails if one single
// connect is refused. That is the whole point: the budget exists to refuse a
// flood, and a flood is not what a client recovering from a deploy looks
// like. Tightening either limit far enough to clip an honest client fails
// here rather than in somebody's offline banner.
//
// The three shapes are the ones the two keys are sized for. One tab is the
// unit. Several tabs share a session family, which is what the family budget
// has to have room for. Many signed-in devices share one NAT, which is what
// the address budget has to have room for — and is why it is the looser of
// the two.
//
// The logged peak is the margin: it is the most connects any one key actually
// produced in a window, next to the limit that allowed them.
func TestHonestReconnectIsNeverRefused(t *testing.T) {
	t.Parallel()

	const (
		outage = 5 * time.Minute
		runs   = 50
	)

	tests := []struct {
		name     string
		families int
		tabs     int
		budget   int
	}{
		{"one tab on one device", 1, 1, connectPerFamily},
		{"four tabs on one device", 1, 4, connectPerFamily},
		{"sixty devices behind one NAT", 60, 1, connectPerIP},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const ip = "203.0.113.7"
			clock := newTestClock()
			g := newBudgetGateway(t, WithConnectClock(clock.now))
			peak := 0

			for run := range runs {
				// A fresh jitter stream per run, and a clock jump past the
				// window so the previous outage's connects have all aged out.
				rnd := rand.New(rand.NewPCG(uint64(run), 0x5eed))
				clock.advance(2 * connectWindow)
				start := clock.now()

				clients := make([]*reconnectingClient, 0, tc.families*tc.tabs)
				for range tc.families {
					family := uuid.New()
					for range tc.tabs {
						c := &reconnectingClient{family: family}
						c.nextAt = start.Add(documentedBackoff(c.attempt, rnd))
						c.attempt++
						clients = append(clients, c)
					}
				}

				deadline := start.Add(outage)
				for {
					next := clients[0]
					for _, c := range clients[1:] {
						if c.nextAt.Before(next.nextAt) {
							next = c
						}
					}
					if next.nextAt.After(deadline) {
						break
					}

					clock.set(next.nextAt)
					if _, allowed := g.ConnectAllowed(next.family, ip); !allowed {
						t.Fatalf("run %d: an honest client following the documented backoff "+
							"was refused %s into the outage, at attempt %d",
							run, next.nextAt.Sub(start).Round(time.Second), next.attempt)
					}
					next.at = append(next.at, next.nextAt)

					next.nextAt = next.nextAt.Add(documentedBackoff(next.attempt, rnd))
					next.attempt++
				}

				byFamily := map[uuid.UUID][]time.Time{}
				var byIP []time.Time
				for _, c := range clients {
					byFamily[c.family] = append(byFamily[c.family], c.at...)
					byIP = append(byIP, c.at...)
				}
				if tc.budget == connectPerIP {
					slices.SortFunc(byIP, time.Time.Compare)
					peak = max(peak, peakInWindow(byIP, connectWindow))
					continue
				}
				for _, at := range byFamily {
					slices.SortFunc(at, time.Time.Compare)
					peak = max(peak, peakInWindow(at, connectWindow))
				}
			}

			t.Logf("peak honest rate %d connects per %s, budget %d", peak, connectWindow, tc.budget)
		})
	}
}
