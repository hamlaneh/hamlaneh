package httpserver

// What is left of the Phase 1.2 contract stubs — ROADMAP §1.2.
//
// The 1.2 contract landed in docs/api/openapi.yaml ahead of its
// implementation, so *apiServer needs one method per operation to satisfy
// the generated api.ServerInterface. Slice 1.2a implemented the
// conversation surface (channel_handlers.go, message_handlers.go) and the
// user directory (directory_handler.go); slice 1.2b implemented search
// (search_handler.go, storage/search.go, migration 0006). The two
// operations below still answer 501 in the contract's Error shape, for a
// reason the storage layer makes concrete:
//
//   - EditMessage, DeleteMessage: slice 1.2b. Nothing in storage writes
//     edited_at or deleted_at; messages.go states outright that this slice
//     sends and reads messages only.
//
// These stubs still sit behind securityMiddleware, so a 501 is only ever
// reached by a caller who already passed every route-level gate: anonymous
// callers get 401 and callers with must_change_password set get 403 before
// any handler here runs. 501 says "the feature is missing", never "you are
// allowed to do this".

import (
	"net/http"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// notImplemented answers the contract Error envelope for an endpoint the
// server routes and gates but does not serve yet.
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, codeNotImplemented, msgNotImplemented)
}

// EditMessage is a slice 1.2b stub: edit your own message.
func (s *apiServer) EditMessage(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.MessageId) {
	notImplemented(w, r)
}

// DeleteMessage is a slice 1.2b stub: soft-delete a message.
func (s *apiServer) DeleteMessage(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.MessageId) {
	notImplemented(w, r)
}
