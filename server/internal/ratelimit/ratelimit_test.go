package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic window tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLimiterLimitedAndRecord(t *testing.T) {
	t.Parallel()

	t.Run("not limited below the limit, limited at it", func(t *testing.T) {
		t.Parallel()
		l := New(10, 5*time.Minute, WithNow(newFakeClock().Now))

		for i := 1; i <= 10; i++ {
			if l.Limited("key") {
				t.Fatalf("limited after %d recorded events, want the first 10 unhindered", i-1)
			}
			l.Record("key")
		}
		if !l.Limited("key") {
			t.Error("not limited after 10 recorded events, want limited")
		}
	})

	t.Run("checks alone never make a key limited", func(t *testing.T) {
		t.Parallel()
		l := New(2, time.Minute, WithNow(newFakeClock().Now))

		for i := range 100 {
			if l.Limited("key") {
				t.Fatalf("check %d limited a key with no recorded events", i+1)
			}
		}
		// After all those checks the full budget is still available.
		l.Record("key")
		if l.Limited("key") {
			t.Error("limited after 1 of 2 events; checks consumed budget")
		}
	})

	t.Run("keys are independent", func(t *testing.T) {
		t.Parallel()
		l := New(2, time.Minute, WithNow(newFakeClock().Now))

		l.Record("a")
		l.Record("a")
		if !l.Limited("a") {
			t.Error("key a not limited at its limit")
		}
		if l.Limited("b") {
			t.Error("key b limited; keys are not independent")
		}
	})

	t.Run("window slides", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(2, time.Minute, WithNow(clock.Now))

		l.Record("k")
		clock.Advance(30 * time.Second)
		l.Record("k")
		if !l.Limited("k") {
			t.Error("not limited with two events inside the window")
		}

		// 31s later the first event (61s old) has left the window; the
		// second (31s old) has not.
		clock.Advance(31 * time.Second)
		if l.Limited("k") {
			t.Error("limited after the oldest event expired")
		}
		l.Record("k")
		if !l.Limited("k") {
			t.Error("not limited while two events remain in the window")
		}
	})

	t.Run("a check prunes a fully-expired key between sweeps", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(2, time.Minute, WithNow(clock.Now))

		clock.Advance(30 * time.Second)
		l.Record("k")
		// An event elsewhere runs the sweep while k is still live...
		clock.Advance(31 * time.Second)
		l.Record("other")
		// ...then k's only event expires before the next sweep is due.
		clock.Advance(34 * time.Second)
		if l.Limited("k") {
			t.Error("limited although every event expired")
		}
		l.mu.Lock()
		_, exists := l.events["k"]
		l.mu.Unlock()
		if exists {
			t.Error("fully-expired key not removed by the check")
		}
	})

	t.Run("being limited does not extend the lockout", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(1, time.Minute, WithNow(clock.Now))

		l.Record("k")
		// Hammer checks while locked out; none of these may count as events.
		for range 5 {
			clock.Advance(10 * time.Second)
			if !l.Limited("k") {
				t.Fatal("not limited during lockout")
			}
		}
		// 61s after the only recorded event, the window is clear even though
		// the last rejected check was 10s ago.
		clock.Advance(11 * time.Second)
		if l.Limited("k") {
			t.Error("limited after window expiry; checks extended the lockout")
		}
	})
}

// TestLimiterRetryAfter pins that RetryAfter is the honest remainder of the
// lockout — exactly the time Limited keeps answering true — so a 429's
// Retry-After header can promise something real.
func TestLimiterRetryAfter(t *testing.T) {
	t.Parallel()

	t.Run("zero for unseen and under-limit keys", func(t *testing.T) {
		t.Parallel()
		l := New(2, time.Minute, WithNow(newFakeClock().Now))

		if got := l.RetryAfter("unseen"); got != 0 {
			t.Errorf("RetryAfter(unseen) = %v, want 0", got)
		}
		l.Record("k")
		if got := l.RetryAfter("k"); got != 0 {
			t.Errorf("RetryAfter under the limit = %v, want 0", got)
		}
	})

	t.Run("the remainder counts down as time passes", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(2, time.Minute, WithNow(clock.Now))

		l.Record("k")
		clock.Advance(10 * time.Second)
		l.Record("k")

		// Limited clears when the FIRST event (10s old) leaves the window.
		if got := l.RetryAfter("k"); got != 50*time.Second {
			t.Errorf("RetryAfter = %v, want 50s", got)
		}
		clock.Advance(20 * time.Second)
		if got := l.RetryAfter("k"); got != 30*time.Second {
			t.Errorf("RetryAfter after 20s = %v, want 30s", got)
		}

		// The prediction comes true: still limited one instant before it,
		// free once the window has slid past the governing event.
		clock.Advance(30 * time.Second)
		if !l.Limited("k") {
			t.Error("not limited although RetryAfter promised 30s more")
		}
		clock.Advance(time.Second)
		if l.Limited("k") {
			t.Error("still limited after the promised remainder elapsed")
		}
		if got := l.RetryAfter("k"); got != 0 {
			t.Errorf("RetryAfter after expiry = %v, want 0", got)
		}
	})

	t.Run("past the limit the governing event moves up", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(2, time.Minute, WithNow(clock.Now))

		// Three events: the key stays limited until the count falls BELOW
		// the limit, i.e. until the second event (40s old) ages out too.
		l.Record("k")
		clock.Advance(20 * time.Second)
		l.Record("k")
		clock.Advance(20 * time.Second)
		l.Record("k")
		if got := l.RetryAfter("k"); got != 40*time.Second {
			t.Errorf("RetryAfter with 3 of 2 events = %v, want 40s", got)
		}
	})

	t.Run("a fully-expired key is pruned and free", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock()
		l := New(1, time.Minute, WithNow(clock.Now))

		l.Record("k")
		clock.Advance(61 * time.Second)
		if got := l.RetryAfter("k"); got != 0 {
			t.Errorf("RetryAfter after full expiry = %v, want 0", got)
		}
		l.mu.Lock()
		_, exists := l.events["k"]
		l.mu.Unlock()
		if exists {
			t.Error("fully-expired key not removed by RetryAfter")
		}
	})
}

// TestLimiterSweep pins the memory bound: keys whose events have all expired
// are removed once a full window has passed, and checks on unseen keys
// create no state at all.
func TestLimiterSweep(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	l := New(3, time.Minute, WithNow(clock.Now))

	for i := range 100 {
		l.Record(fmt.Sprintf("key-%d", i))
	}
	for i := range 100 {
		l.Limited(fmt.Sprintf("checked-only-%d", i))
	}
	clock.Advance(45 * time.Second)
	l.Record("recent") // young enough to survive the sweep
	clock.Advance(30 * time.Second)
	l.Record("fresh") // 75s since the last sweep: triggers it

	l.mu.Lock()
	keys := len(l.events)
	l.mu.Unlock()
	if keys != 2 {
		t.Errorf("after sweep %d keys remain, want 2 (recent + fresh)", keys)
	}
}

func TestNewPanicsOnBadConfig(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		limit  int
		window time.Duration
	}{
		{"zero limit", 0, time.Minute},
		{"negative limit", -1, time.Minute},
		{"zero window", 10, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Error("New with invalid config did not panic")
				}
			}()
			New(tt.limit, tt.window)
		})
	}
}

// TestLimiterConcurrentAccess exercises the mutex under contention; the race
// detector (Linux CI) verifies safety, and the exact recorded-event count
// verifies no Record is lost while concurrent checks interleave.
func TestLimiterConcurrentAccess(t *testing.T) {
	t.Parallel()

	l := New(50, time.Minute)
	var wg sync.WaitGroup

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				l.Limited("shared")
				l.Record("shared")
			}
		}()
	}
	wg.Wait()

	l.mu.Lock()
	recorded := len(l.events["shared"])
	l.mu.Unlock()
	if recorded != 200 {
		t.Errorf("%d of 200 concurrent events recorded, want exactly 200", recorded)
	}
	if !l.Limited("shared") {
		t.Error("key not limited after recording far past the limit")
	}
}
