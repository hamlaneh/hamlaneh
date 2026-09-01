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
// Every entry is WSEnforced: internal/wsgateway's security suite asserts
// each rule against a real socket — non-member sockets never receiving a
// private channel's events, presence limited to DM peers, read positions
// delivered only to the same user's other sockets, and the two removal
// events reaching disjoint audiences.
//
// message_updated and message_deleted joined them in slice 1.2b, licensed by
// TestEditAndDeleteEventsNeverReachANonMember and TestEditAndDeleteStopAtRemoval
// (internal/wsgateway/delivery_test.go): the first proves a stranger's socket
// receives neither event while a member's receives both, the second that the
// audience is read at send time, so a user removed mid-socket stops hearing
// them. WSEnforced means exactly that — a test asserts the rule — and it is
// not a claim to make ahead of one.
func WSRegistry() []WSEntry {
	return []WSEntry{
		// Client → server.
		wsEnforced("connect", C2S, WSSession),
		wsEnforced("hello", C2S, WSSession),
		wsEnforced("subscribe", C2S, WSMember),
		wsEnforced("unsubscribe", C2S, WSMember),
		wsEnforced("typing", C2S, WSMember),
		wsEnforced("presence", C2S, WSSession),
		wsEnforced("ping", C2S, WSSession),
		wsEnforced("pong", C2S, WSSession),

		// Server → client.
		wsEnforced("hello_ok", S2C, WSSession),
		wsEnforced("subscribed", S2C, WSMember),
		wsEnforced("unsubscribed", S2C, WSMember),
		wsEnforced("message_created", S2C, WSMember),
		wsEnforced("message_updated", S2C, WSMember),
		wsEnforced("message_deleted", S2C, WSMember),
		wsEnforced("channel_created", S2C, WSMember),
		wsEnforced("channel_updated", S2C, WSMember),
		wsEnforced("member_added", S2C, WSMember),
		// Removal is two events because the audiences are disjoint: the
		// remaining members may hear who left; the removed user is no longer
		// a member, so the only thing a socket of theirs may be told is that
		// the channel is gone for them (ws-protocol.md §4).
		wsEnforced("member_removed", S2C, WSMember),
		wsEnforced("channel_removed", S2C, WSSelf),
		wsEnforced("read_position", S2C, WSSelf),
		wsEnforced("typing", S2C, WSMember),
		wsEnforced("presence", S2C, WSMemberDM),
		wsEnforced("resync", S2C, WSMember),
		// The three call events (ADR 005). Membership scope, like every other
		// channel event: a stranger to a channel must not learn that a call
		// is happening in it, which is as much a disclosure as its messages.
		// TestCallEventsNeverReachANonMember and TestCallEventsStopAtRemoval
		// (internal/wsgateway/call_delivery_test.go) are what licenses
		// WSEnforced here — the first proves a non-member's socket receives
		// none of the three while a member's receives all three, the second
		// that the audience is read at send time.
		wsEnforced("call_started", S2C, WSMember),
		wsEnforced("call_updated", S2C, WSMember),
		wsEnforced("call_ended", S2C, WSMember),
		// The two E2EE transport events (ADR 006). mls_commit is
		// membership-scoped like every other channel event: a stranger must
		// not learn that a channel's group moved, which is as much a
		// disclosure as learning a message was sent.
		//
		// mls_welcome is `self` rather than `member`, and it is the one event
		// in this table where that is not a weaker rule but the only correct
		// one: a Welcome exists precisely because its recipient is NOT in the
		// group yet, so a membership check would refuse the event the design
		// depends on. It carries no payload, so the fan-out to a person's
		// other devices reveals only that they have something to fetch.
		//
		// TestMlsEventsNeverReachANonMember and TestMlsWelcomeReachesOnlyItsOwnUser
		// (internal/wsgateway/mls_delivery_test.go) are what licenses
		// WSEnforced on both.
		wsEnforced("mls_commit", S2C, WSMember),
		wsEnforced("mls_welcome", S2C, WSSelf),
		wsEnforced("ping", S2C, WSSession),
		wsEnforced("pong", S2C, WSSession),
		wsEnforced("error", S2C, WSSession),
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

// wsEnforced registers an operation whose rule internal/wsgateway's security
// suite asserts against a real socket.
func wsEnforced(op string, direction WSDirection, rule WSAuthz) WSEntry {
	return WSEntry{
		Op:     WSOperation{Op: op, Direction: direction},
		Authz:  rule,
		Status: WSEnforced,
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
