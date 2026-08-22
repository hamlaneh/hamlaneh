// Package ratelimit provides a small in-memory sliding-window rate limiter.
//
// Phase 1.1 uses it for login attempts (keyed by client IP and by
// identifier); Phase 1.2 generalizes it into per-endpoint budgets. State
// lives in process memory, which is exactly right for the single-server
// deployments Hamlaneh targets — a restart forgiving all windows is
// acceptable for brute-force protection.
//
// Checking and counting are separate on purpose: callers ask Limited before
// doing guarded work and Record only the attempts that should count (for
// login, failed authentications and two-step challenge mints), so completed
// sign-ins never consume the budget.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter tracks at most limit recorded events per key within a sliding
// window. The zero value is not usable; construct with New.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu        sync.Mutex
	events    map[string][]time.Time
	lastSweep time.Time
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithNow replaces the limiter's clock, letting tests move time instead of
// sleeping.
func WithNow(now func() time.Time) Option {
	return func(l *Limiter) { l.now = now }
}

// New returns a Limiter that reports keys limited once limit events were
// recorded within any window-sized time span. limit must be positive and
// window must be greater than zero; New panics otherwise, because a
// misconfigured limiter would silently disable a security control.
func New(limit int, window time.Duration, opts ...Option) *Limiter {
	if limit <= 0 || window <= 0 {
		panic("ratelimit: limit and window must be positive")
	}
	l := &Limiter{
		limit:  limit,
		window: window,
		now:    time.Now,
		events: make(map[string][]time.Time),
	}
	for _, opt := range opts {
		opt(l)
	}
	l.lastSweep = l.now()
	return l
}

// Limited reports whether key has reached its limit within the current
// window. It records nothing — a limited key's lockout therefore never
// extends past window after its last recorded event — and it never creates
// state for unseen keys, so checking is free of memory cost.
func (l *Limiter) Limited(key string) bool {
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.maybeSweep(now, cutoff)

	times, ok := l.events[key]
	if !ok {
		return false
	}
	recent := pruneBefore(times, cutoff)
	if len(recent) == 0 {
		delete(l.events, key)
		return false
	}
	l.events[key] = recent
	return len(recent) >= l.limit
}

// RetryAfter reports how much longer key stays limited: the time until
// enough recorded events age out of the window that Limited reports false.
// It returns zero for a key that is not currently limited, and it records
// nothing — like Limited, it is free to call on rejected requests. Callers
// use it to put an honest Retry-After header on a 429.
func (l *Limiter) RetryAfter(key string) time.Duration {
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.maybeSweep(now, cutoff)

	recent := pruneBefore(l.events[key], cutoff)
	if len(recent) == 0 {
		delete(l.events, key)
		return 0
	}
	l.events[key] = recent
	if len(recent) < l.limit {
		return 0
	}
	// The key stops being limited the moment its in-window count falls to
	// limit-1, which is when the (len-limit+1) oldest events have all aged
	// out; the last of those is recent[len(recent)-l.limit].
	return recent[len(recent)-l.limit].Add(l.window).Sub(now)
}

// Record counts one event for key at the current time. Callers gate Record
// behind Limited, so a key at its limit stops accumulating events as soon
// as its requests are rejected.
func (l *Limiter) Record(key string) {
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.maybeSweep(now, cutoff)

	l.events[key] = append(pruneBefore(l.events[key], cutoff), now)
}

// maybeSweep drops fully-expired keys at most once per window, bounding
// memory growth from one-off keys (each client IP is a key).
func (l *Limiter) maybeSweep(now, cutoff time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for key, times := range l.events {
		if recent := pruneBefore(times, cutoff); len(recent) == 0 {
			delete(l.events, key)
		} else {
			l.events[key] = recent
		}
	}
}

// pruneBefore returns the suffix of times at or after cutoff. times is
// append-ordered, so everything before the first recent entry is expired.
func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	for i, ts := range times {
		if !ts.Before(cutoff) {
			return times[i:]
		}
	}
	return nil
}
