package wsgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Frame types (§3 client -> server, §4 server -> client). typing, ping and
// pong appear in both directions.
//
// message_updated and message_deleted are absent: nothing emits them yet.
// Editing and soft-deleting a message is slice 1.2b, and the two frame types
// arrive with the Realtime methods that announce them.
const (
	typeHello        = "hello"
	typeHelloOK      = "hello_ok"
	typeSubscribe    = "subscribe"
	typeSubscribed   = "subscribed"
	typeUnsubscribe  = "unsubscribe"
	typeUnsubscribed = "unsubscribed"
	typeTyping       = "typing"
	typePresence     = "presence"
	typePing         = "ping"
	typePong         = "pong"
	typeResync       = "resync"
	typeError        = "error"

	typeMessageCreated = "message_created"
	typeChannelCreated = "channel_created"
	typeChannelUpdated = "channel_updated"
	typeChannelRemoved = "channel_removed"
	typeMemberAdded    = "member_added"
	typeMemberRemoved  = "member_removed"
	typeReadPosition   = "read_position"
)

// Close codes (§8). The 1000-range pair are the standard ones; the
// 4400-range are this protocol's.
const (
	closeNormal        websocket.StatusCode = 1000
	closeGoingAway     websocket.StatusCode = 1001
	closeProtocolError websocket.StatusCode = 4400
	closeUnauthorized  websocket.StatusCode = 4401
	closeHeartbeat     websocket.StatusCode = 4408
	closeFrameTooLarge websocket.StatusCode = 4413
)

// 4429 (rate limited) is deliberately absent. §8 spends it on the connect
// budget, which belongs to the generic per-endpoint rate-limit middleware
// this phase still owes; the per-frame budgets this package does enforce
// answer with an error frame and keep the socket open.

// Error frame codes. channel_not_found is deliberately the answer to both
// "no such channel" and "not yours" (§3), the same non-leaking answer the
// REST paths give — a socket must not become the one place a channel's
// existence leaks.
const (
	codeChannelNotFound = "channel_not_found"
	codeRateLimited     = "rate_limited"
	codeInternalError   = "internal_error"
)

// Presence states (§4). offline is server-derived and a client that claims
// it is ignored.
const (
	presenceOnline  = "online"
	presenceAway    = "away"
	presenceOffline = "offline"
)

// maxCorrelationID is the §2 bound on the client's echoed correlation id.
const maxCorrelationID = 64

// errMalformed marks a frame that must close the socket with 4400. There is
// no partial-parse recovery: an ambiguous frame is a bug or an attack, and
// neither deserves a best-effort interpretation (§2).
var errMalformed = errors.New("malformed frame")

// inFrame is a decoded client frame. Every field is validated by parseFrame
// before a handler sees it.
type inFrame struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Chan *uuid.UUID      `json:"chan"`
	TS   *time.Time      `json:"ts"`
	Data json.RawMessage `json:"data"`
}

// outFrame is a server frame. Chan and Seq are pointers so they are omitted
// from the frames that do not carry them rather than sent as zero values.
type outFrame struct {
	Type string     `json:"type"`
	ID   string     `json:"id,omitempty"`
	Chan *uuid.UUID `json:"chan,omitempty"`
	Seq  *uint64    `json:"seq,omitempty"`
	TS   time.Time  `json:"ts"`
	Data any        `json:"data"`
}

// channelScoped reports whether a client operation must carry chan (§3).
func channelScoped(frameType string) bool {
	switch frameType {
	case typeSubscribe, typeUnsubscribe, typeTyping:
		return true
	default:
		return false
	}
}

// parseFrame decodes and validates one client frame. Every failure it
// reports is fatal to the socket; a frame whose only problem is an
// unrecognised type parses fine and is ignored by the caller.
func parseFrame(raw []byte) (inFrame, error) {
	var f inFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return inFrame{}, fmt.Errorf("%w: %w", errMalformed, err)
	}
	switch {
	case f.Type == "":
		return inFrame{}, fmt.Errorf("%w: missing type", errMalformed)
	case f.TS == nil:
		return inFrame{}, fmt.Errorf("%w: missing ts", errMalformed)
	case len(f.ID) > maxCorrelationID:
		return inFrame{}, fmt.Errorf("%w: id too long", errMalformed)
	case !isJSONObject(f.Data):
		return inFrame{}, fmt.Errorf("%w: data is not an object", errMalformed)
	case channelScoped(f.Type) && f.Chan == nil:
		return inFrame{}, fmt.Errorf("%w: %s without chan", errMalformed, f.Type)
	}
	return f, nil
}

// isJSONObject reports whether raw is a JSON object. data is required on
// every frame and is never null and never a bare scalar (§2).
func isJSONObject(raw json.RawMessage) bool {
	trimmed := trimJSONSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func trimJSONSpace(raw json.RawMessage) json.RawMessage {
	for len(raw) > 0 {
		switch raw[0] {
		case ' ', '\t', '\r', '\n':
			raw = raw[1:]
		default:
			return raw
		}
	}
	return raw
}

// encode renders a server frame. The server's ts is authoritative and always
// UTC (§2); data is an object even when there is nothing to carry.
func encode(f outFrame) ([]byte, error) {
	f.TS = f.TS.UTC()
	if f.Data == nil {
		f.Data = struct{}{}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode %s frame: %w", f.Type, err)
	}
	return b, nil
}

// encodeOrLog renders a frame the gateway itself built. Every call site
// marshals a fixed struct of scalars, uuids and times, so a failure would be
// a programming error rather than a runtime condition; it is logged and the
// frame is dropped rather than taking the process down or failing the write
// that produced it.
func encodeOrLog(f outFrame) []byte {
	b, err := encode(f)
	if err != nil {
		slog.Error("ws encode frame", "type", f.Type, "error", err)
		return nil
	}
	return b
}

// Payloads for the frames this gateway sends.

type helloOKData struct {
	ProtocolVersion          int             `json:"protocol_version"`
	UserID                   uuid.UUID       `json:"user_id"`
	SessionFamilyID          uuid.UUID       `json:"session_family_id"`
	HeartbeatIntervalSeconds int             `json:"heartbeat_interval_seconds"`
	MaxFrameBytes            int             `json:"max_frame_bytes"`
	Resumed                  []resumedCursor `json:"resumed"`
	Resync                   []uuid.UUID     `json:"resync"`
}

type resumedCursor struct {
	Chan uuid.UUID `json:"chan"`
	Seq  uint64    `json:"seq"`
}

type helloData struct {
	ProtocolVersion int             `json:"protocol_version"`
	Resume          []resumedCursor `json:"resume"`
}

type presenceData struct {
	State string `json:"state"`
}

type errorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type messageData struct {
	Message api.Message `json:"message"`
}

type channelData struct {
	Channel api.Channel `json:"channel"`
}

type memberData struct {
	Chan uuid.UUID       `json:"chan"`
	User api.UserSummary `json:"user"`
}

type chanData struct {
	Chan uuid.UUID `json:"chan"`
}

type readPositionData struct {
	Chan      uuid.UUID `json:"chan"`
	MessageID uuid.UUID `json:"message_id"`
	ReadAt    time.Time `json:"read_at"`
}

type typingData struct {
	UserID uuid.UUID `json:"user_id"`
}

type presenceEventData struct {
	UserID uuid.UUID `json:"user_id"`
	State  string    `json:"state"`
}

// The contract shapes an event carries. These mirror httpserver's apiMessage,
// apiChannel and apiUserSummary; they are duplicated rather than shared
// because those live unexported in the handler package. Any change to the
// Message or Channel schema has to be made in both places until a slice
// moves them somewhere both can import.

func apiMessage(m storage.Message) api.Message {
	return api.Message{
		Id:        m.ID,
		ChannelId: m.ChannelID,
		Author: api.UserSummary{
			Id:          m.Author.ID,
			Username:    m.Author.Username,
			DisplayName: m.Author.DisplayName,
		},
		ClientMsgId: m.ClientMsgID,
		Content:     m.Content,
		CreatedAt:   m.CreatedAt,
		EditedAt:    m.EditedAt,
		DeletedAt:   m.DeletedAt,
		Attachments: []api.Attachment{},
	}
}

// apiChannel carries dm_peer for the same reason the REST mapping does: a
// direct message has no slug, so dm_peer is the only thing that names it, and
// a channel_created without one puts a nameless row in the recipient's
// sidebar. It is caller-scoped, which is why every caller of this must hand
// over a row read for the recipient of the event — see
// httpserver.announceInvitation.
func apiChannel(ch storage.Channel) api.Channel {
	out := api.Channel{
		Id:                ch.ID,
		Kind:              api.ChannelKind(ch.Kind),
		Slug:              ch.Slug,
		Topic:             ch.Topic,
		MemberCount:       ch.MemberCount,
		UnreadCount:       ch.UnreadCount,
		MentionCount:      ch.MentionCount,
		LastReadMessageId: ch.LastReadMessageID,
		LastMessageAt:     ch.LastMessageAt,
		CreatedBy:         ch.CreatedBy,
		CreatedAt:         ch.CreatedAt,
	}
	if peer := ch.DMPeer; peer != nil {
		out.DmPeer = &api.UserSummary{Id: peer.ID, Username: peer.Username, DisplayName: peer.DisplayName}
	}
	return out
}

func apiUserSummary(u storage.User) api.UserSummary {
	return api.UserSummary{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}
