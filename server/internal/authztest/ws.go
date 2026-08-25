package authztest

import "sort"

// The WebSocket half of the authorization harness. CLAUDE.md requires every
// endpoint — REST *and* WS subscribe/publish — to register one entry, but
// the OpenAPI completeness gate cannot see WS operations: the contract for
// them lives in docs/api/ws-protocol.md. WSRegistry is their register, and
// TestWSRegistryCoversProtocol is the gate that keeps the two in step.

// WSDirection is which way a WebSocket frame travels.
type WSDirection string

const (
	// C2S is a client → server operation.
	C2S WSDirection = "c2s"
	// S2C is a server → client event.
	S2C WSDirection = "s2c"
)

// WSOperation identifies one row of the protocol's operation table. The key
// is the pair: typing, ping and pong each exist in both directions with
// different rules.
type WSOperation struct {
	Op        string
	Direction WSDirection
}

func (op WSOperation) String() string { return string(op.Direction) + " " + op.Op }

// WSAuthz is the authorization rule an operation must enforce, mirroring
// the authz column of docs/api/ws-protocol.md §10.
type WSAuthz string

const (
	// WSAuthzUnspecified marks a forgotten decision; completeness fails.
	WSAuthzUnspecified WSAuthz = ""
	// WSPublic needs no session (nothing qualifies today; reserved).
	WSPublic WSAuthz = "public"
	// WSSession needs any authenticated socket.
	WSSession WSAuthz = "session"
	// WSMember needs membership of the frame's channel at the moment the
	// frame is sent or received; a non-member gets channel_not_found, never
	// forbidden, and never the event.
	WSMember WSAuthz = "member"
	// WSMemberDM is WSMember restricted to channels of kind dm.
	WSMemberDM WSAuthz = "member-dm"
	// WSSelf is delivered only to sockets of the same user.
	WSSelf WSAuthz = "self"
)

// WSAuthzRules returns every rule the protocol document may name.
func WSAuthzRules() []WSAuthz {
	return []WSAuthz{WSPublic, WSSession, WSMember, WSMemberDM, WSSelf}
}

// WSStatus records how far the gateway has got with one operation.
type WSStatus string

const (
	// WSStatusUnspecified marks a forgotten decision; completeness fails.
	WSStatusUnspecified WSStatus = ""
	// WSNotImplemented marks an operation the protocol specifies and no
	// code serves yet.
	WSNotImplemented WSStatus = "not_implemented"
	// WSEnforced marks an operation whose rule the WS security suite
	// asserts for real.
	WSEnforced WSStatus = "enforced"
)

// WSRow is one parsed row of the operation table in ws-protocol.md.
type WSRow struct {
	Op    WSOperation
	Scope string
	Authz WSAuthz
}

// WSEntry is one WebSocket operation's registration.
type WSEntry struct {
	Op    WSOperation
	Authz WSAuthz
	// Status is what the harness asserts today. It is a statement about
	// this repository's code, never a permission decision.
	Status WSStatus
}

// WSRegistry returns the WebSocket authorization registry: one entry per row
// of the operation table in docs/api/ws-protocol.md §10.
//
// Every entry is WSNotImplemented today. Phase 1.2's gateway does not exist
// yet — GET /api/v1/ws answers 501 like the rest of the messaging surface,
// so no frame of any kind is served and nothing enforces these rules at
// runtime. Authz records the rule the gateway must enforce when it lands,
// and the completeness gate keeps this list and the protocol document from
// drifting apart in the meantime.
//
// When the gateway ships, each entry flips to WSEnforced and the WS security
// suite asserts its rule per principal — non-member sockets never receiving a
// private channel's events, presence limited to DM peers, read positions
// delivered only to the user's own sockets. Until then, do not read
// WSNotImplemented as "allowed".
func WSRegistry() []WSEntry {
	return []WSEntry{
		// Client → server.
		wsStub("connect", C2S, WSSession),
		wsStub("hello", C2S, WSSession),
		wsStub("subscribe", C2S, WSMember),
		wsStub("unsubscribe", C2S, WSMember),
		wsStub("typing", C2S, WSMember),
		wsStub("presence", C2S, WSSession),
		wsStub("ping", C2S, WSSession),
		wsStub("pong", C2S, WSSession),

		// Server → client.
		wsStub("hello_ok", S2C, WSSession),
		wsStub("subscribed", S2C, WSMember),
		wsStub("unsubscribed", S2C, WSMember),
		wsStub("message_created", S2C, WSMember),
		wsStub("message_updated", S2C, WSMember),
		wsStub("message_deleted", S2C, WSMember),
		wsStub("channel_created", S2C, WSMember),
		wsStub("channel_updated", S2C, WSMember),
		wsStub("member_added", S2C, WSMember),
		// Removal is two events because the audiences are disjoint: the
		// remaining members may hear who left; the removed user is no longer
		// a member, so the only thing a socket of theirs may be told is that
		// the channel is gone for them (ws-protocol.md §4).
		wsStub("member_removed", S2C, WSMember),
		wsStub("channel_removed", S2C, WSSelf),
		wsStub("read_position", S2C, WSSelf),
		wsStub("typing", S2C, WSMember),
		wsStub("presence", S2C, WSMemberDM),
		wsStub("resync", S2C, WSMember),
		wsStub("ping", S2C, WSSession),
		wsStub("pong", S2C, WSSession),
		wsStub("error", S2C, WSSession),
	}
}

// wsStub registers an operation the gateway does not serve yet.
func wsStub(op string, direction WSDirection, rule WSAuthz) WSEntry {
	return WSEntry{
		Op:     WSOperation{Op: op, Direction: direction},
		Authz:  rule,
		Status: WSNotImplemented,
	}
}

// DiffWSRegistry compares the protocol document's operation table against
// the registry: missing lists documented operations with no entry (the
// CI-failing case), extra lists entries for operations the document no
// longer has. Both come back sorted for stable failure output.
func DiffWSRegistry(rows []WSRow, entries []WSEntry) (missing, extra []string) {
	inRegistry := map[WSOperation]bool{}
	for _, e := range entries {
		inRegistry[e.Op] = true
	}
	inDoc := map[WSOperation]bool{}
	for _, row := range rows {
		inDoc[row.Op] = true
		if !inRegistry[row.Op] {
			missing = append(missing, row.Op.String())
		}
	}
	for op := range inRegistry {
		if !inDoc[op] {
			extra = append(extra, op.String())
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
