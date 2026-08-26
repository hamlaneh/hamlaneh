package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The WebSocket upgrade endpoint. Split out of messaging_stubs.go so the
// gateway and the REST messaging handlers can be built without sharing a
// file; docs/api/ws-protocol.md is the contract everything above the upgrade
// obeys.

// codeOriginNotAllowed answers a handshake whose Origin is missing, "null",
// or not the instance's public origin. It is a 403 on the handshake
// response, before any socket exists (ws-protocol.md §8).
const codeOriginNotAllowed errorCode = "origin_not_allowed"

const msgOriginNotAllowed = "origin not allowed"

// errNoPrincipal reports a session-classified route reached without one,
// which securityMiddleware makes impossible and which would be a routing bug.
var errNoPrincipal = errors.New("no principal on a session-gated route")

// webSocketGateway is the part of an attached Realtime that can also serve
// the upgrade. Declaring it here keeps the gateway replaceable: this package
// depends on two method signatures, not on a package.
type webSocketGateway interface {
	// OriginAllowed reports whether a handshake carrying this Origin may
	// proceed. The gateway owns the answer because it owns the configured
	// public origin.
	OriginAllowed(origin string) bool
	// ConnectAllowed reports whether this handshake fits the connect-flood
	// budget, spending one unit when it does, and returns the wait to publish
	// when it does not. The gateway owns this one too: ws-protocol.md §8 keys
	// it per session family and per IP, and both windows outlive any single
	// request, so there is nothing request-scoped for the per-endpoint
	// rate-limit table to hold.
	ConnectAllowed(familyID uuid.UUID, clientIP string) (time.Duration, bool)
	// ServeWebSocket takes over an authenticated request whose Origin has
	// already been accepted and serves the socket until it closes.
	ServeWebSocket(w http.ResponseWriter, r *http.Request, user storage.User, familyID uuid.UUID)
}

// ConnectWebSocket is the realtime upgrade endpoint (ws-protocol.md §1).
//
// Authentication is the session cookie, already verified by
// securityMiddleware; the socket then binds to the session *family*, not to
// the access token, so a fifteen-minute rotation does not drop it and
// revoking the family closes it within ten seconds (§7).
//
// Origin is checked strictly before anything else: present, not "null", and
// equal to the instance's configured public origin. No wildcard, no
// substring match, no registrable-domain relaxation. The double-submit CSRF
// header cannot be sent on a browser WebSocket handshake, so this check
// carries that load, and a server with no gateway attached has no configured
// origin — which allows nothing, because "I cannot tell whether this is
// same-site" must never read as "yes".
func (s *apiServer) ConnectWebSocket(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errNoPrincipal)
		return
	}

	gw, attached := s.realtime.(webSocketGateway)
	if !attached || !gw.OriginAllowed(r.Header.Get("Origin")) {
		writeError(w, r, http.StatusForbidden, codeOriginNotAllowed, msgOriginNotAllowed)
		return
	}

	// The connect-flood budget (§8), spent last of the three gates and still
	// before the upgrade. Last, because a handshake refused for its Origin or
	// its session is refused for what is wrong with it, and a cross-site
	// attempt must not be able to spend a budget on the victim's behalf.
	// Before the upgrade, because that is where the cost is: a refusal here
	// costs two map lookups, while an accepted handshake hijacks the
	// connection and starts two goroutines that live as long as the socket.
	//
	// So this is a 429 on the handshake response and never a close code —
	// there is no socket yet to close (§8). It goes through writeRateLimited
	// like every other 429 this server writes, which is what puts the
	// Retry-After on it.
	//
	// The address comes from clientIP, the same derivation every other
	// per-address budget here uses, trust model included: the leftmost
	// X-Forwarded-For value only when the direct peer is the reverse proxy.
	_, ip := clientIP(r)
	if retryAfter, allowed := gw.ConnectAllowed(prin.session.FamilyID, ip); !allowed {
		writeRateLimited(w, r, retryAfter)
		return
	}

	// The server's ReadTimeout and WriteTimeout do not need clearing here:
	// net/http drops the connection's deadlines when the handler hijacks it
	// (server.go, hijackLocked). The socket sets its own bounds instead — a
	// heartbeat inbound (§6) and a write timeout outbound — and
	// TestHandshakeSucceedsThroughTheWholeStack keeps a socket alive past
	// both request deadlines so a future change here cannot quietly
	// reintroduce a realtime connection that dies after ten seconds.
	gw.ServeWebSocket(w, r, prin.user, prin.session.FamilyID)
}
