package httpserver

import "net/http"

// The WebSocket upgrade endpoint. Split out of messaging_stubs.go so the
// gateway and the REST messaging handlers can be built without sharing a
// file; docs/api/ws-protocol.md is the contract everything above the upgrade
// obeys.
// ConnectWebSocket is a Phase 1.2 stub: the realtime WebSocket upgrade.
// There is no gateway yet, so the handshake is refused like any other
// unimplemented endpoint rather than half-opened. Origin validation and the
// session-family binding described in docs/api/ws-protocol.md arrive with
// the gateway itself.
func (s *apiServer) ConnectWebSocket(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
