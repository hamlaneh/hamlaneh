package wsgateway

import (
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
)

// The connect-flood budget (ws-protocol.md §8), the one rate limit in this
// server that cannot be decided from a route.
//
// # Why it is not in the per-endpoint middleware
//
// httpserver's rate-limit table keys every budget it owns on the
// authenticated account, because that is the correct key for work a caller
// asks the server to do. §8 keys this one on the session *family* and on the
// client IP, and neither is the account: a family is one signed-in device, so
// a budget per account would let a laptop's reconnect storm throttle the
// phone; and the IP half has to bound a host that holds no session at all
// past the first one.
//
// # What one connect costs
//
// A refused handshake costs the session lookup the security middleware
// already did, an Origin comparison and two map lookups. An admitted one
// hijacks the connection and starts two goroutines that live until the socket
// does, plus a 256-frame outbound queue. That asymmetry is the whole reason
// the refusal happens before websocket.Accept: this budget bounds the rate at
// which one family or one address can make the server hold connections open.
//
// # The numbers, against what a real client does
//
// The webapp reconnects with exponential backoff and full jitter, 1s base and
// 30s cap (§8; webapp/src/chat/realtime.ts). Full jitter draws uniformly from
// [0, ceiling], and the ceiling doubles per attempt: 1, 2, 4, 8, 16, then 30
// forever. Expected delays are half of each, so the expected time to the Nth
// attempt of an outage runs 0.5s, 1.5s, 3.5s, 7.5s, 15.5s, 30.5s, 45.5s,
// 60.5s.
//
// One tab therefore makes about EIGHT connect attempts in the first minute of
// an outage, and about four a minute once the backoff has saturated at the
// 30s ceiling. That is the honest reconnect storm, and it is what these
// numbers are measured against.
//
//   - connectPerFamily is 60: seven and a half times the storm rate, so a
//     person with several tabs open on one session still fits, and a single
//     tab would need a run of jitter draws with a probability around 1e-18 to
//     reach it. It is also a flat ceiling of one socket a second from one
//     signed-in device, which nothing honest approaches.
//   - connectPerIP is 600, ten times looser, for the reason the password-reset
//     budget is looser per address: an office or a household shares one
//     address. It covers about 75 clients behind one NAT all reconnecting at
//     the storm rate at the same instant, or 150 once they have saturated.
//
// A crowd bigger than that degrades rather than breaks. A refused handshake
// never reaches handleOpen, so the client's attempt counter advances and its
// next wait is drawn from a bigger ceiling (realtime.ts: the counter resets on
// hello_ok, not on open). The herd's aggregate rate therefore falls with every
// refusal until it fits, which is the opposite of a lockout.
//
// # Why the window is a minute
//
// So the refusal never outlasts the client's own schedule. A refused client is
// admitted again within one window, by which point its backoff has already
// passed the documented 30s ceiling and it is asking more slowly than it was.
// A five-minute window would keep it refused for four minutes after it had
// already slowed down to the floor the protocol asks for — a stuck banner
// rather than a control.
const (
	connectPerFamily = 60
	connectPerIP     = 600
	connectWindow    = time.Minute
)

// ConnectAllowed reports whether a handshake may proceed under the §8 connect
// budget, spending one unit against the session family and one against the
// client IP when it may. On a refusal it returns how long the caller must
// wait, which httpserver.ConnectWebSocket turns into the handshake's
// Retry-After header.
//
// Both halves are checked before either is recorded. Recording the family
// unit and then refusing on the IP one would count an attempt that was never
// admitted, which extends a lockout the caller never earned — the same
// mistake internal/passwordreset avoids by keeping its two keys in one place.
//
// The wait is the longer of the two, for the same reason: a caller told to
// come back in one second by the half that is nearly clear would be refused
// again by the half that is not.
func (g *Gateway) ConnectAllowed(familyID uuid.UUID, clientIP string) (retryAfter time.Duration, allowed bool) {
	family := familyID.String()

	if g.connectByFamily.Limited(family) || g.connectByIP.Limited(clientIP) {
		return max(g.connectByFamily.RetryAfter(family), g.connectByIP.RetryAfter(clientIP)), false
	}

	g.connectByFamily.Record(family)
	g.connectByIP.Record(clientIP)
	return 0, true
}

// newConnectLimiters builds the pair on the given clock. They live on the
// Gateway rather than in package state, so every gateway carries its own
// windows and tests cannot bleed budget into each other.
func newConnectLimiters(now func() time.Time) (family, ip *ratelimit.Limiter) {
	opt := ratelimit.WithNow(now)
	return ratelimit.New(connectPerFamily, connectWindow, opt),
		ratelimit.New(connectPerIP, connectWindow, opt)
}
