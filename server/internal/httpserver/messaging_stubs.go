package httpserver

// Phase 1.2 (messaging core) contract stubs — ROADMAP §1.2.
//
// The 1.2 contract (channels, membership, messages, read positions, DMs,
// search, the user directory and the WebSocket upgrade) landed in
// docs/api/openapi.yaml ahead of its implementation, so *apiServer needs one
// method per operation to satisfy the generated api.ServerInterface. Every
// handler below answers 501 not_implemented in the contract's Error shape.
// The messaging slice replaces them with real behavior and deletes this file.
//
// These stubs still sit behind securityMiddleware, so a 501 is only ever
// reached by a caller who already passed every route-level gate: anonymous
// callers get 401 and callers with must_change_password set get 403 before
// any handler here runs. 501 says "the feature is missing", never "you are
// allowed to do this" — per-channel authorization arrives with the real
// handlers as explicit authz.Can calls.

import (
	"net/http"

	openapitypes "github.com/oapi-codegen/runtime/types"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// notImplemented answers the contract Error envelope for an endpoint the
// server routes and gates but does not serve yet.
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, codeNotImplemented, msgNotImplemented)
}

// ListUsers is a Phase 1.2 stub: the instance user directory.
func (s *apiServer) ListUsers(w http.ResponseWriter, r *http.Request, _ api.ListUsersParams) {
	notImplemented(w, r)
}

// ListChannels is a Phase 1.2 stub: the caller's channels and DMs.
func (s *apiServer) ListChannels(w http.ResponseWriter, r *http.Request, _ api.ListChannelsParams) {
	notImplemented(w, r)
}

// CreateChannel is a Phase 1.2 stub: create a public or private channel.
func (s *apiServer) CreateChannel(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// GetChannel is a Phase 1.2 stub: one channel or DM.
func (s *apiServer) GetChannel(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	notImplemented(w, r)
}

// UpdateChannel is a Phase 1.2 stub: set the channel topic.
func (s *apiServer) UpdateChannel(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	notImplemented(w, r)
}

// ListChannelMembers is a Phase 1.2 stub: members of a channel.
func (s *apiServer) ListChannelMembers(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.ListChannelMembersParams) {
	notImplemented(w, r)
}

// AddChannelMember is a Phase 1.2 stub: invite a user into a channel.
func (s *apiServer) AddChannelMember(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	notImplemented(w, r)
}

// RemoveChannelMember is a Phase 1.2 stub: leave a channel or remove a member.
func (s *apiServer) RemoveChannelMember(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ openapitypes.UUID) {
	notImplemented(w, r)
}

// ListMessages is a Phase 1.2 stub: one page of channel history.
func (s *apiServer) ListMessages(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.ListMessagesParams) {
	notImplemented(w, r)
}

// SendMessage is a Phase 1.2 stub: send a message.
func (s *apiServer) SendMessage(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	notImplemented(w, r)
}

// EditMessage is a Phase 1.2 stub: edit your own message.
func (s *apiServer) EditMessage(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.MessageId) {
	notImplemented(w, r)
}

// DeleteMessage is a Phase 1.2 stub: soft-delete a message.
func (s *apiServer) DeleteMessage(w http.ResponseWriter, r *http.Request, _ api.ChannelId, _ api.MessageId) {
	notImplemented(w, r)
}

// SetReadPosition is a Phase 1.2 stub: move the caller's read position.
func (s *apiServer) SetReadPosition(w http.ResponseWriter, r *http.Request, _ api.ChannelId) {
	notImplemented(w, r)
}

// OpenDirectMessage is a Phase 1.2 stub: open or reuse a 1:1 DM.
func (s *apiServer) OpenDirectMessage(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// Search is a Phase 1.2 stub: search messages.
func (s *apiServer) Search(w http.ResponseWriter, r *http.Request, _ api.SearchParams) {
	notImplemented(w, r)
}

// ConnectWebSocket is a Phase 1.2 stub: the realtime WebSocket upgrade.
// There is no gateway yet, so the handshake is refused like any other
// unimplemented endpoint rather than half-opened. Origin validation and the
// session-family binding described in docs/api/ws-protocol.md arrive with
// the gateway itself.
func (s *apiServer) ConnectWebSocket(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
