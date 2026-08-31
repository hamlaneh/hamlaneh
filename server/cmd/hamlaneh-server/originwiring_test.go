package main

import (
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/wsgateway"
)

// gatewayFor builds a gateway exactly as start() does for m, over a loopback
// origin, and asks it about one Origin header.
//
// The store is nil and provably untouched: OriginAllowed reads none, no event
// is ever published to the dispatch loop, and the sweep — the one background
// path that would — is pushed an hour out and the gateway is closed long
// before it.
func gatewayFor(t *testing.T, m mode, origin string) bool {
	t.Helper()

	g := wsgateway.New(nil, "http://127.0.0.1:8080",
		append(m.gatewayOptions(), wsgateway.WithSweepInterval(time.Hour))...)
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Errorf("close gateway: %v", err)
		}
	})
	return g.OriginAllowed(origin)
}

// TestHomeModeAcceptsBothLoopbackOrigins is the wiring half of the fix: home
// mode has to actually ASK for the allowance, or the gateway-level tests prove
// something no deployment uses.
//
// The failure this prevents is the worst kind — silent and total. Home mode
// prints http://127.0.0.1:8080 and a person types localhost:8080; the page
// loads either way, but a refused upgrade means the chat never receives a
// message and never says why.
func TestHomeModeAcceptsBothLoopbackOrigins(t *testing.T) {
	homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}

	for _, origin := range []string{
		"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080",
	} {
		if !gatewayFor(t, m, origin) {
			t.Errorf("home mode refused %q; that is a chat that loads and receives nothing", origin)
		}
	}
}

// TestHomeModeStillRefusesAForeignOrigin: the allowance is loopback and the
// bound port, not a wildcard. A cross-site handshake is refused in home mode
// exactly as it is in server mode.
func TestHomeModeStillRefusesAForeignOrigin(t *testing.T) {
	homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}

	for _, origin := range []string{
		"http://evil.example.com",
		"https://localhost:8080",
		"http://localhost:9090",
		"http://localhost.evil.net:8080",
		"null",
		"",
	} {
		if gatewayFor(t, m, origin) {
			t.Errorf("home mode allowed %q; the Origin check is the CSWSH defense and must not widen", origin)
		}
	}
}

// TestServerModeGetsNoOriginAllowance is the regression guard the whole change
// rests on. Server mode must pass no origin option at all, so its handshake
// behaviour is byte-for-byte what it was: one exact origin, everything else
// refused — including the loopback sibling home mode now accepts.
func TestServerModeGetsNoOriginAllowance(t *testing.T) {
	m := serverMode()

	if opts := m.gatewayOptions(); len(opts) != 0 {
		t.Fatalf("server mode passes %d gateway option(s); it must pass none", len(opts))
	}
	if !gatewayFor(t, m, "http://127.0.0.1:8080") {
		t.Error("server mode refused its own configured origin")
	}
	for _, origin := range []string{"http://localhost:8080", "http://[::1]:8080"} {
		if gatewayFor(t, m, origin) {
			t.Errorf("server mode allowed %q; home mode's allowance leaked into it", origin)
		}
	}
}

// TestServerModeStillTrustsItsProxy and its home-mode sibling below are the
// wiring half of the X-Forwarded-For fix. The behaviour they stand for lives
// in internal/httpserver (TestLoginRateLimitTrustsForwardedForBehindAProxy,
// TestLoginRateLimitIgnoresForwardedForWithNoProxy); what these pin is that
// each mode actually asks for the one it needs, so neither of those proves
// something no deployment uses.
func TestServerModeStillTrustsItsProxy(t *testing.T) {
	if !serverMode().trustsForwardedFor() {
		t.Error("server mode stopped trusting X-Forwarded-For; " +
			"Caddy is the only thing that sets it there and per-IP limits now collapse onto Caddy's address")
	}
}

// TestHomeModeTrustsNoForwardedFor: nothing forwards in home mode, so the
// header is written by whoever sent the request. Trusting it would let any
// local process choose its own sign-in budget and its own audit-log address.
func TestHomeModeTrustsNoForwardedFor(t *testing.T) {
	homeTestEnv(t)

	m, err := homeMode()
	if err != nil {
		t.Fatalf("homeMode() error = %v", err)
	}
	if m.trustsForwardedFor() {
		t.Error("home mode trusts X-Forwarded-For; there is no proxy there to have written it")
	}
}
