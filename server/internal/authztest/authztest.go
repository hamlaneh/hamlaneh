// Package authztest is the authorization matrix harness (CLAUDE.md testing
// policy). Every contract endpoint registers one Entry declaring the
// expected status for every principal; the matrix test runs the full grid
// against a real server and database, and the completeness test fails the
// build when an endpoint in docs/api/openapi.yaml has no entry.
//
// Registering a new endpoint is a one-line ask per slice: add its Entry
// here, and the harness enforces the user-A-cannot-touch-B matrix for it
// forever. An endpoint that acts on a channel registers two — one per
// fixture kind — and answers the five channel relations instead of the four
// instance-scoped columns (ADR 002).
package authztest

import (
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// Principal is a matrix column: who is making the request.
type Principal string

// The instance-scoped principals: what an endpoint outside any channel can
// be asked by.
const (
	// Anonymous sends no cookies at all.
	Anonymous Principal = "anonymous"
	// Member is an authenticated non-admin.
	Member Principal = "member"
	// MemberMustChange is authenticated with must_change_password set.
	MemberMustChange Principal = "member-must-change"
	// Admin is an authenticated admin.
	Admin Principal = "admin"
)

// The channel relations (ADR 002). Instance role and channel membership are
// independent facts, so both admin columns exist: they pin the contract's
// boundary from either side — admin grants nothing outside membership, and
// within membership only what the operation's own text grants.
const (
	// ChannelNonMember is authenticated and a stranger to this channel.
	ChannelNonMember Principal = "channel-non-member"
	// ChannelMember is a member who neither created the channel nor wrote
	// the fixture message.
	ChannelMember Principal = "channel-member"
	// ChannelOwner is the member who created the channel
	// (channels.created_by). Never the fixture message's author: creating a
	// channel confers no authority over what is said in it.
	ChannelOwner Principal = "channel-owner"
	// AdminNonMember is an org admin who is a stranger to this channel.
	AdminNonMember Principal = "admin-non-member"
	// AdminMember is an org admin who is a member of this channel, and not
	// the fixture message's author.
	AdminMember Principal = "admin-member"
	// MemberAuthor is the authorship refinement: a member acting on a
	// message they wrote. Optional — declared only by operations whose
	// contract distinguishes the author.
	MemberAuthor Principal = "member-author"
)

// Principals returns every matrix column in stable order. It is the universe
// of columns, not the set any one entry declares: an entry runs exactly the
// principals present in its Want (Entry.Principals), and must declare at
// least the ones its shape requires (Entry.RequiredPrincipals).
func Principals() []Principal {
	return []Principal{
		Anonymous, Member, MemberMustChange, Admin,
		ChannelNonMember, ChannelMember, ChannelOwner,
		AdminNonMember, AdminMember, MemberAuthor,
	}
}

// InstancePrincipals are the columns every instance-scoped entry must
// declare.
func InstancePrincipals() []Principal {
	return []Principal{Anonymous, Member, MemberMustChange, Admin}
}

// ChannelPrincipals are the seven columns ADR 002 requires of every
// channel-scoped entry: the two route-gate columns, unchanged, plus the five
// channel relations. MemberAuthor is a refinement on top, not one of the
// seven.
func ChannelPrincipals() []Principal {
	return []Principal{
		Anonymous, MemberMustChange,
		ChannelNonMember, ChannelMember, ChannelOwner,
		AdminNonMember, AdminMember,
	}
}

// Class is the deliberate security classification of an endpoint. The zero
// value is unclassified and fails the completeness test: classification is
// an explicit decision, never a default.
type Class int

const (
	// ClassUnclassified marks a forgotten decision; completeness fails.
	ClassUnclassified Class = iota
	// ClassPublic endpoints require no authentication.
	ClassPublic
	// ClassRefreshCookie marks POST /api/v1/auth/refresh: gated by the
	// refresh cookie inside the handler, deliberately not "anonymous" even
	// though the spec models no security scheme for it (ROADMAP 1.1).
	ClassRefreshCookie
	// ClassChallengeCookie marks POST /api/v1/auth/login/totp: gated by the
	// two-step challenge cookie the 202 login sets, not by a session. A valid
	// session grants nothing there, which is the point of the class.
	ClassChallengeCookie
	// ClassSession endpoints require a valid session.
	ClassSession
	// ClassAdmin endpoints require a valid session plus admin.
	ClassAdmin
)

// Fixture is one matrix cell's provisioned world: the acting user and, for a
// channel-scoped cell, the fresh channel it acts on, the message inside it,
// and the other users its bodies and paths name.
//
// Every cell gets its own — cells mutate (a removal, an edit, a send), and a
// fixture shared across cells would make a failure depend on the order they
// happened to run in.
type Fixture struct {
	Username string
	Password string
	// Unique is a per-cell suffix for bodies that must not collide
	// (e.g. admin-created usernames, channel slugs).
	Unique string

	// ChannelID and MessageID name the cell's channel and the message inside
	// it that the message-scoped operations act on. Set for channel-scoped
	// cells only.
	ChannelID string
	MessageID string
	// MemberUserID is a member of that channel other than the acting user:
	// the target of a removal, and the author of MessageID for every
	// principal except MemberAuthor.
	MemberUserID string

	// OutsiderUserID is a real user who belongs to no channel: the target of
	// an invite, and the peer of a newly opened direct message.
	OutsiderUserID string
}

// Entry is one endpoint's row in the authorization matrix.
//
// A channel-scoped entry (Kind non-empty) is registered once per fixture
// kind, so one contract operation has several rows — private and dm always,
// public where the tripwire calls for it — each answering on a channel of
// that kind. The completeness gate refuses a {channelId} operation that
// registers the instance-scoped shape instead.
type Entry struct {
	Method string
	// Path is the contract path exactly as openapi.yaml spells it,
	// {template} segments included; it is the completeness gate's key.
	Path  string
	Class Class

	// Kind is the kind of channel this row's fixture provisions, and is what
	// makes the entry channel-scoped. Empty for instance-scoped entries.
	Kind storage.ChannelKind

	// RequestTarget is the concrete target the harness requests when Path is
	// not one itself and the fixture cannot supply it: an id the contract
	// requires to name nothing, plus any query parameter the contract makes
	// required (search's q, which the generated router rejects with 400
	// before the security middleware ever runs). Empty means Path is already
	// a valid target, or that its {template} segments come from the fixture.
	RequestTarget string

	// Body builds a minimal valid request body for the acting fixture;
	// nil for bodyless requests.
	Body func(fx Fixture) string

	// Want is the expected status per principal. Every principal the entry's
	// shape requires must be present (checked by the completeness test and
	// by the runner), and the runner executes exactly the keys that are.
	Want map[Principal]int

	// WantCode optionally pins the contract error code of non-2xx cells,
	// catching gate-versus-authz mixups that share a status.
	WantCode map[Principal]string
}

// Target returns the request target for one cell: RequestTarget, or Path when
// there is none, with every {template} segment replaced by the fixture's real
// ids.
func (e Entry) Target(fx Fixture) string {
	target := e.RequestTarget
	if target == "" {
		target = e.Path
	}
	return strings.NewReplacer(
		"{channelId}", fx.ChannelID,
		"{messageId}", fx.MessageID,
		"{userId}", fx.MemberUserID,
	).Replace(target)
}

// ChannelScoped reports whether this entry acts on a fixture channel.
func (e Entry) ChannelScoped() bool { return e.Kind != "" }

// RequiredPrincipals returns the columns this entry's shape must declare.
func (e Entry) RequiredPrincipals() []Principal {
	if e.ChannelScoped() {
		return ChannelPrincipals()
	}
	return InstancePrincipals()
}

// Principals returns the columns this entry actually runs: every principal
// present in Want, in the stable order of Principals. A refinement runs
// because it is declared; a key that is not a known principal runs never,
// which is what the runner's cell count catches.
func (e Entry) Principals() []Principal {
	cols := make([]Principal, 0, len(e.Want))
	for _, p := range Principals() {
		if _, ok := e.Want[p]; ok {
			cols = append(cols, p)
		}
	}
	return cols
}

// outcome is one cell's expected answer: a status, plus the contract error
// code when it is a refusal. The code is not decoration — a 404 and a 403 are
// not interchangeable here, and neither are two refusals sharing a status:
// "you are not a member" and "this channel has no topic" are both answers a
// member of a private channel must never see confused for the other.
type outcome struct {
	status int
	code   string
}

// success is an outcome with no error code to pin.
func success(status int) outcome { return outcome{status: status} }

// refusal is a non-2xx outcome and the contract code that must carry it.
func refusal(status int, code string) outcome { return outcome{status: status, code: code} }

// notMember is the answer every channel-scoped operation owes a stranger:
// 404, never 403, so a channel's existence never leaks — and an org admin who
// is not a member is a stranger, because membership is the only visibility
// rule in this phase (openapi.yaml, ADR 002).
var notMember = refusal(http.StatusNotFound, "channel_not_found")

// members builds the five channel-relation columns from the three answers a
// member can get. Both non-member columns are notMember on every operation,
// which is exactly the boundary ADR 002 exists to pin from both sides.
func members(member, owner, adminMember outcome) map[Principal]outcome {
	return map[Principal]outcome{
		ChannelNonMember: notMember,
		AdminNonMember:   notMember,
		ChannelMember:    member,
		ChannelOwner:     owner,
		AdminMember:      adminMember,
	}
}

// plus adds one refinement column to a want map, returning a copy so a map
// shared by several rows cannot be edited through one of them.
func plus(want map[Principal]outcome, p Principal, o outcome) map[Principal]outcome {
	out := maps.Clone(want)
	out[p] = o
	return out
}

// notImplementedYet is what an endpoint nobody has written a handler for
// answers every caller the route gates let through (messaging_stubs.go).
//
// It is never a permission decision, and no cell carrying it claims to be
// one: the route-level gates still decide first — anonymous 401,
// still-owes-a-password-change 403 — and nothing past them has looked at
// membership or authorship, because there is no code there to look. A 501
// says the feature is missing, never "you are allowed to do this".
var notImplementedYet = refusal(http.StatusNotImplemented, "not_implemented")

// stubbed rewrites a row's want map to what its missing handler actually
// answers: 501 for every principal past the route gates, the two non-member
// columns included — no membership check exists yet to produce the 404 a
// stranger is owed.
//
// The contract's real per-channel outcomes stay written at the call site
// instead of being deleted, so tightening the row when the handler lands is
// removing this wrapper and the operation's entry from
// notImplementedOperations — not reconstructing the answers from scratch.
func stubbed(want map[Principal]outcome) map[Principal]outcome {
	out := maps.Clone(want)
	for p := range out {
		out[p] = notImplementedYet
	}
	return out
}

// searchPath is message search's contract path, spelled as openapi.yaml
// spells it. Its registry row and its entry below must name one operation,
// not two strings that agree today.
const searchPath = "/api/v1/search"

// notImplementedOperations is the closed list of contract operations whose
// matrix rows may expect a 501: the two that are slice 1.2b work
// (docs/ROADMAP.md) and have no handler at all.
//
// DiffNotImplemented enforces the list in both directions — a row expecting
// 501 for an unlisted operation fails, so leaving a cell at "nobody wrote
// this" is always a deliberate edit here; and a listed operation whose rows
// no longer expect 501 fails too, so the list cannot outlive the stubs it
// describes. The remaining direction is covered by TestAuthzMatrix itself: a
// handler that lands while its row still says 501 gets asked, answers for
// real, and turns the cell red.
//
// GET /api/v1/ws is deliberately absent. The gateway is being written now,
// and its row pins the handshake's origin refusal rather than a 501.
func notImplementedOperations() []Operation {
	return []Operation{
		{Method: http.MethodPatch, Path: messagePath},  // edit a message
		{Method: http.MethodDelete, Path: messagePath}, // soft-delete a message
	}
}

// channelEntry builds one channel-scoped row. want carries the five channel
// relations and any refinement; the two route-gate columns are identical on
// every channel-scoped endpoint — anonymous 401, still-owes-a-password-change
// 403 — and are filled in here so no row can quietly disagree about them.
func channelEntry(method, path string, kind storage.ChannelKind,
	body func(Fixture) string, want map[Principal]outcome,
) Entry {
	e := Entry{
		Method: method,
		Path:   path,
		Class:  ClassSession,
		Kind:   kind,
		Body:   body,
		Want: map[Principal]int{
			Anonymous:        http.StatusUnauthorized,
			MemberMustChange: http.StatusForbidden,
		},
		WantCode: map[Principal]string{
			Anonymous:        "not_authenticated",
			MemberMustChange: "password_change_required",
		},
	}
	for p, o := range want {
		e.Want[p] = o.status
		if o.code != "" {
			e.WantCode[p] = o.code
		}
	}
	return e
}

// bothKinds registers one {channelId} operation for the two kinds ADR 002
// requires, for an operation whose answers do not depend on the kind.
func bothKinds(method, path string, body func(Fixture) string, want map[Principal]outcome) []Entry {
	return []Entry{
		channelEntry(method, path, storage.ChannelKindPrivate, body, want),
		channelEntry(method, path, storage.ChannelKindDM, body, want),
	}
}

// sessionEntry builds the matrix row of an implemented session-gated
// endpoint. The route-level gates decide first — anonymous 401,
// still-owes-a-password-change 403 — and a caller past both reaches the
// handler, which answers want (with code, or "" when want is a success).
//
// target is the concrete request target when path carries {template}
// segments the fixture does not fill, empty when path is already one.
//
// Every fixture is a fresh account, so want is the handler's answer for one
// in its initial state: no pending setup, no second factor, one session.
func sessionEntry(method, path, target string, body func(Fixture) string, want int, code string) Entry {
	e := Entry{
		Method:        method,
		Path:          path,
		Class:         ClassSession,
		RequestTarget: target,
		Body:          body,
		Want: map[Principal]int{
			Anonymous:        http.StatusUnauthorized,
			MemberMustChange: http.StatusForbidden,
			Member:           want,
			Admin:            want,
		},
		WantCode: map[Principal]string{
			Anonymous:        "not_authenticated",
			MemberMustChange: "password_change_required",
		},
	}
	if code != "" {
		e.WantCode[Member] = code
		e.WantCode[Admin] = code
	}
	return e
}

// The contract paths of the channel-scoped surface, spelled exactly as
// openapi.yaml spells them: the {template} segments are the completeness
// gate's key, and Entry.Target fills them from the cell's fixture.
const (
	channelPath  = "/api/v1/channels/{channelId}"
	membersPath  = channelPath + "/members"
	memberPath   = membersPath + "/{userId}"
	messagesPath = channelPath + "/messages"
	messagePath  = messagesPath + "/{messageId}"
	readPath     = channelPath + "/read"
)

// matrixClientMsgID is the send row's idempotency key. Every cell posts into
// its own fresh channel and the key is unique per (channel, author), so one
// constant can never collide — not with another cell's send, and not with the
// fixture message, which storage creates under a key of its own.
const matrixClientMsgID = "00000000-0000-4000-8000-00000000c0de"

// passwordBody re-authenticates as the acting fixture. The TOTP rows that
// need it send the CORRECT password on purpose: what the matrix pins there
// is the account's state, not a re-auth failure that would mask it.
func passwordBody(fx Fixture) string {
	return fmt.Sprintf(`{"password":%q}`, fx.Password)
}

// userIDBody names the cell's outsider: a real account that is in no channel.
// Both operations that take one want a stranger — an invite that adds
// somebody who is already a member answers 204 without proving anything, and
// a DM opened with an existing peer is the 200 half of the idempotent pair,
// not the 201 the row pins.
func userIDBody(fx Fixture) string {
	return fmt.Sprintf(`{"user_id":%q}`, fx.OutsiderUserID)
}

// newChannelBody creates a channel whose slug is this cell's alone.
func newChannelBody(fx Fixture) string {
	return fmt.Sprintf(`{"slug":"new%s","kind":"private"}`, fx.Unique)
}

// readBody moves the read position to the cell's own fixture message, which
// the contract requires to belong to the channel — so a refusal is about
// permission, never about the message being somewhere else.
func readBody(fx Fixture) string {
	return fmt.Sprintf(`{"message_id":%q}`, fx.MessageID)
}

// Registry returns the full authorization matrix: the instance-scoped
// surface followed by the channel-scoped one.
func Registry() []Entry {
	return append(instanceRegistry(), channelRegistry()...)
}

// instanceRegistry is the half of the matrix decided without a channel:
// instance role, session state, and the endpoints that answer the same to
// everybody by design.
func instanceRegistry() []Entry {
	return []Entry{
		{
			Method: http.MethodGet,
			Path:   "/healthz",
			Class:  ClassPublic,
			Want: map[Principal]int{
				Anonymous: http.StatusOK, Member: http.StatusOK,
				MemberMustChange: http.StatusOK, Admin: http.StatusOK,
			},
		},
		{
			Method: http.MethodGet,
			Path:   "/readyz",
			Class:  ClassPublic,
			Want: map[Principal]int{
				Anonymous: http.StatusOK, Member: http.StatusOK,
				MemberMustChange: http.StatusOK, Admin: http.StatusOK,
			},
		},
		{
			Method: http.MethodPost,
			Path:   "/api/v1/auth/login",
			Class:  ClassPublic,
			Body: func(fx Fixture) string {
				return fmt.Sprintf(`{"identifier":%q,"password":%q}`, fx.Username, fx.Password)
			},
			Want: map[Principal]int{
				Anonymous: http.StatusOK, Member: http.StatusOK,
				MemberMustChange: http.StatusOK, Admin: http.StatusOK,
			},
		},
		{
			Method: http.MethodPost,
			Path:   "/api/v1/auth/logout",
			Class:  ClassSession,
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusNoContent,
				MemberMustChange: http.StatusNoContent, Admin: http.StatusNoContent,
			},
			WantCode: map[Principal]string{Anonymous: "not_authenticated"},
		},
		{
			Method: http.MethodPost,
			Path:   "/api/v1/auth/refresh",
			Class:  ClassRefreshCookie,
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusNoContent,
				MemberMustChange: http.StatusNoContent, Admin: http.StatusNoContent,
			},
			WantCode: map[Principal]string{Anonymous: "not_authenticated"},
		},
		{
			Method: http.MethodPost,
			Path:   "/api/v1/auth/change-password",
			Class:  ClassSession,
			Body: func(fx Fixture) string {
				return fmt.Sprintf(`{"current_password":%q,"new_password":"a replacement passphrase"}`, fx.Password)
			},
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusNoContent,
				MemberMustChange: http.StatusNoContent, Admin: http.StatusNoContent,
			},
			WantCode: map[Principal]string{Anonymous: "not_authenticated"},
		},
		{
			Method: http.MethodGet,
			Path:   "/api/v1/users/me",
			Class:  ClassSession,
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusOK,
				MemberMustChange: http.StatusOK, Admin: http.StatusOK,
			},
			WantCode: map[Principal]string{Anonymous: "not_authenticated"},
		},
		{
			Method: http.MethodGet,
			Path:   "/api/v1/admin/users",
			Class:  ClassAdmin,
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusForbidden,
				MemberMustChange: http.StatusForbidden, Admin: http.StatusOK,
			},
			WantCode: map[Principal]string{
				Anonymous:        "not_authenticated",
				Member:           "forbidden",
				MemberMustChange: "password_change_required",
			},
		},
		{
			Method: http.MethodPost,
			Path:   "/api/v1/admin/users",
			Class:  ClassAdmin,
			Body: func(fx Fixture) string {
				return fmt.Sprintf(`{"username":"created%s","password":"an initial password"}`, fx.Unique)
			},
			Want: map[Principal]int{
				Anonymous: http.StatusUnauthorized, Member: http.StatusForbidden,
				MemberMustChange: http.StatusForbidden, Admin: http.StatusCreated,
			},
			WantCode: map[Principal]string{
				Anonymous:        "not_authenticated",
				Member:           "forbidden",
				MemberMustChange: "password_change_required",
			},
		},

		// Phase 1.2, the endpoints decided without a channel. Nothing here
		// turns on membership: the directory and the sidebar are the caller's
		// own view, creating a channel needs no permission beyond a session,
		// and a DM is opened with a user, not inside a conversation.
		sessionEntry(http.MethodGet, "/api/v1/users", "", nil, http.StatusOK, ""),
		sessionEntry(http.MethodGet, "/api/v1/channels", "", nil, http.StatusOK, ""),
		sessionEntry(http.MethodPost, "/api/v1/channels", "", newChannelBody, http.StatusCreated, ""),
		// A fresh fixture shares no DM with anybody, so opening one is always
		// the 201 half of this idempotent pair.
		sessionEntry(http.MethodPost, "/api/v1/dms", "", userIDBody, http.StatusCreated, ""),
		// Search landed in slice 1.2b, so these are real outcomes: a session
		// is the whole gate, and every signed-in caller gets a 200 — an empty
		// page for a fresh fixture, which has said nothing to find.
		//
		// This row pins the first half of search's contract, "a session".
		// The second half — results only from your own channels — is not a
		// matrix shape: it is one caller reading a page, not a principal
		// reaching an endpoint, and the query enforces it in SQL rather than
		// at the route. It is pinned where it can be proved, by the leak test
		// in storage (TestSearchScopeIntegration) and its handler-level twin.
		sessionEntry(http.MethodGet, searchPath, searchPath+"?q=hello", nil,
			http.StatusOK, ""),
		// The upgrade endpoint, asked without an Origin header. The contract
		// requires the handshake to carry one matching the instance origin and
		// to reject a missing or mismatched value (CSWSH defense), so the
		// authenticated columns pin that refusal: a session is necessary and
		// not sufficient to open a socket. 101 is deliberately not the
		// expectation — the harness serves through an httptest recorder, which
		// cannot be hijacked, and a row that could only ever pass by not
		// upgrading would assert nothing.
		sessionEntry(http.MethodGet, "/api/v1/ws", "", nil, http.StatusForbidden, "origin_not_allowed"),

		// Phase 1.1b two-step verification. Real outcomes: every cell below is
		// the handler's own answer for a fresh account, so a Member/Admin cell
		// proves the gates ran AND the handler decided.
		//
		// The four settings endpoints that refuse do so on the account's state,
		// never on permission: nothing has been set up, so there is nothing to
		// verify, activate, disable or reissue. Each carries the body that gets
		// it past request validation to that decision — a 400 here would pin
		// the shape of the request instead of the security answer.
		sessionEntry(http.MethodGet, "/api/v1/users/me/totp", "", nil, http.StatusOK, ""),
		sessionEntry(http.MethodPost, "/api/v1/users/me/totp/setup", "", nil, http.StatusOK, ""),
		sessionEntry(http.MethodPost, "/api/v1/users/me/totp/verify", "",
			func(Fixture) string { return `{"code":"123456"}` },
			http.StatusConflict, "totp_setup_expired"),
		sessionEntry(http.MethodPost, "/api/v1/users/me/totp/activate", "", nil,
			http.StatusConflict, "totp_setup_not_verified"),
		sessionEntry(http.MethodPost, "/api/v1/users/me/totp/disable", "", passwordBody,
			http.StatusConflict, "totp_not_enabled"),
		sessionEntry(http.MethodPost, "/api/v1/users/me/totp/recovery-codes", "", passwordBody,
			http.StatusConflict, "totp_not_enabled"),

		// Session management is implemented: these are real outcomes, not stubs.
		sessionEntry(http.MethodGet, "/api/v1/users/me/sessions", "", nil, http.StatusOK, ""),
		// A well-formed id naming nothing answers exactly like another
		// account's family: 404. That indistinguishability is the property —
		// a guessed id must never confirm that someone else is signed in.
		sessionEntry(http.MethodDelete, "/api/v1/users/me/sessions/{familyId}",
			"/api/v1/users/me/sessions/00000000-0000-4000-8000-0000000000ff",
			nil, http.StatusNotFound, "session_not_found"),
		sessionEntry(http.MethodPost, "/api/v1/users/me/sessions/revoke-others", "", nil,
			http.StatusNoContent, ""),

		// Public by design. The instance document is read before anyone has a
		// session, and the reset pair answers identically to every principal —
		// that uniformity IS the enumeration-safety property, so the matrix
		// pins it rather than leaving it to the handler tests.
		{
			Method: http.MethodGet,
			Path:   "/api/v1/instance",
			Class:  ClassPublic,
			Want: map[Principal]int{
				Anonymous:        http.StatusOK,
				MemberMustChange: http.StatusOK,
				Member:           http.StatusOK,
				Admin:            http.StatusOK,
			},
		},
		{
			// Every principal gets the same 202 for an address that does not
			// exist — and the handler tests prove it is byte-identical to the
			// answer for one that does. That uniformity IS the enumeration
			// defence, so the matrix pins it rather than trusting the handler.
			Method: http.MethodPost,
			Path:   "/api/v1/auth/reset-request",
			Class:  ClassPublic,
			Body: func(fx Fixture) string {
				return `{"email":"nobody` + fx.Unique + `@example.invalid"}`
			},
			Want: map[Principal]int{
				Anonymous:        http.StatusAccepted,
				MemberMustChange: http.StatusAccepted,
				Member:           http.StatusAccepted,
				Admin:            http.StatusAccepted,
			},
		},
		{
			// A garbage token answers exactly as an expired, superseded or
			// already-used one does: one code for all four, so a replayed link
			// never reveals which of them it hit.
			Method: http.MethodPost,
			Path:   "/api/v1/auth/reset-complete",
			Class:  ClassPublic,
			Body: func(Fixture) string {
				return `{"token":"` + strings.Repeat("z", 43) +
					`","new_password":"a matrix passphrase"}`
			},
			Want: map[Principal]int{
				Anonymous:        http.StatusUnauthorized,
				MemberMustChange: http.StatusUnauthorized,
				Member:           http.StatusUnauthorized,
				Admin:            http.StatusUnauthorized,
			},
			WantCode: map[Principal]string{
				Anonymous:        "invalid_reset_token",
				MemberMustChange: "invalid_reset_token",
				Member:           "invalid_reset_token",
				Admin:            "invalid_reset_token",
			},
		},

		{
			// Gated by the two-step challenge cookie, not a session. No fixture
			// holds a challenge, so all four principals answer 401
			// not_authenticated — identically, the signed-in ones included.
			// That uniformity IS the class: a session is not authority here,
			// and only the half-authenticated state a 202 login mints is.
			//
			// The refusal also lands before the body is looked at, which is why
			// this row sends none: no challenge means no request to validate.
			Method: http.MethodPost,
			Path:   "/api/v1/auth/login/totp",
			Class:  ClassChallengeCookie,
			Want: map[Principal]int{
				Anonymous:        http.StatusUnauthorized,
				MemberMustChange: http.StatusUnauthorized,
				Member:           http.StatusUnauthorized,
				Admin:            http.StatusUnauthorized,
			},
			WantCode: map[Principal]string{
				Anonymous:        "not_authenticated",
				MemberMustChange: "not_authenticated",
				Member:           "not_authenticated",
				Admin:            "not_authenticated",
			},
		},
	}
}

// channelRegistry is the channel-scoped half of the matrix: every operation
// whose contract path names a {channelId}, registered once per fixture kind.
//
// Every Want here is read off that operation's description in
// docs/api/openapi.yaml. Three answers recur and are worth stating once:
//
//   - A non-member gets 404 channel_not_found, org admins included.
//     Membership is the only visibility rule in Phase 1.2, so an admin
//     outside a channel is a stranger to it and must not be able to tell it
//     from a channel that does not exist.
//   - A direct message refuses what its shape cannot carry with 400, not 403:
//     its pair is fixed and it has no topic. That is a statement about the
//     channel, not about the caller — and it is decided after membership, so
//     the 400 never reaches somebody who should be seeing a 404.
//   - Creating a channel is not moderating it. ChannelOwner gets the member's
//     answer on every message operation; only authorship and instance-admin
//     move those.
func channelRegistry() []Entry {
	var (
		read      = success(http.StatusOK)
		created   = success(http.StatusCreated)
		done      = success(http.StatusNoContent)
		dmFixed   = refusal(http.StatusBadRequest, "dm_membership_fixed")
		notOwner  = refusal(http.StatusForbidden, "forbidden")
		notAuthor = refusal(http.StatusForbidden, "not_message_author")
		notMod    = refusal(http.StatusForbidden, "not_message_author_or_admin")
		// A direct message has no topic, so PATCHing one is a 400. It is the
		// single 400 in this surface the contract states without naming a
		// code; pinned here to the generic request-validation code the server
		// already writes and the contract-generated webapp mock already
		// answers. A handler that wants a specific code changes openapi.yaml
		// first — this value is not the place to settle it.
		dmNoTopic = refusal(http.StatusBadRequest, "invalid_request")
	)

	readable := members(read, read, read)
	entries := bothKinds(http.MethodGet, channelPath, nil, readable)

	// The public tripwire (ADR 002). In Phase 1.2 membership is the only
	// visibility rule for every kind, so this row is what turns red if
	// somebody later opens public channels up without a contract change.
	// One row, not a third full kind: proving sameness twice buys nothing.
	entries = append(entries,
		channelEntry(http.MethodGet, channelPath, storage.ChannelKindPublic, nil, readable))

	topicBody := func(Fixture) string { return `{"topic":"a matrix topic"}` }
	entries = append(entries,
		channelEntry(http.MethodPatch, channelPath, storage.ChannelKindPrivate, topicBody,
			members(read, read, read)),
		channelEntry(http.MethodPatch, channelPath, storage.ChannelKindDM, topicBody,
			members(dmNoTopic, dmNoTopic, dmNoTopic)))

	entries = append(entries, bothKinds(http.MethodGet, membersPath, nil, readable)...)

	// Any member may invite; a DM's pair is fixed.
	entries = append(entries,
		channelEntry(http.MethodPost, membersPath, storage.ChannelKindPrivate, userIDBody,
			members(done, done, done)),
		channelEntry(http.MethodPost, membersPath, storage.ChannelKindDM, userIDBody,
			members(dmFixed, dmFixed, dmFixed)))

	// Removing somebody else: the creator, or an admin who is a member. The
	// fixture targets a member who is not the acting user, so no cell here is
	// the always-allowed case of leaving, and the channel keeps a member
	// either way — the contract's last_member refusal is never in play.
	entries = append(entries,
		channelEntry(http.MethodDelete, memberPath, storage.ChannelKindPrivate, nil,
			members(notOwner, done, done)),
		channelEntry(http.MethodDelete, memberPath, storage.ChannelKindDM, nil,
			members(dmFixed, dmFixed, dmFixed)))

	entries = append(entries, bothKinds(http.MethodGet, messagesPath, nil, readable)...)

	sendBody := func(Fixture) string {
		return fmt.Sprintf(`{"client_msg_id":%q,"content":"a matrix message"}`, matrixClientMsgID)
	}
	entries = append(entries, bothKinds(http.MethodPost, messagesPath, sendBody,
		members(created, created, created))...)

	// Editing is author-only, admins included: rewriting somebody else's
	// words is impersonation, and no role makes that acceptable. Deleting is
	// the author, or an admin who is a member of this channel — deletion
	// removes words, it does not put new ones in somebody's mouth.
	//
	// Both are slice 1.2b (docs/ROADMAP.md) and neither handler exists, so
	// what the server answers today is the stub's 501 and stubbed() says so.
	// The wants inside it are the contract's real answers, kept here for the
	// slice that makes them true: landing the handler is deleting the
	// wrapper and the operation's entry in notImplementedOperations.
	editBody := func(Fixture) string { return `{"content":"an edited matrix message"}` }
	entries = append(entries, bothKinds(http.MethodPatch, messagePath, editBody,
		stubbed(plus(members(notAuthor, notAuthor, notAuthor), MemberAuthor, read)))...)

	entries = append(entries, bothKinds(http.MethodDelete, messagePath, nil,
		stubbed(plus(members(notMod, notMod, done), MemberAuthor, done)))...)

	// A read position is the caller's own, so every member may move theirs
	// and a stranger still gets the channel's 404.
	entries = append(entries, bothKinds(http.MethodPut, readPath, readBody,
		members(done, done, done))...)

	return entries
}

// Operation identifies one (method, path) pair of the API contract.
type Operation struct {
	Method string
	Path   string
}

func (op Operation) String() string { return op.Method + " " + op.Path }

// DiffRegistry compares the contract's operations against the registry:
// missing lists spec operations without a matrix entry (the CI-failing
// case), extra lists registry entries for operations the spec no longer
// has. Both come back sorted for stable failure output.
//
// One operation may have several entries (one per fixture kind); that is a
// coverage question, answered by the kind-coverage gate, not this one.
func DiffRegistry(specOps []Operation, entries []Entry) (missing, extra []string) {
	inRegistry := map[Operation]bool{}
	for _, e := range entries {
		inRegistry[Operation{Method: e.Method, Path: e.Path}] = true
	}
	inSpec := map[Operation]bool{}
	for _, op := range specOps {
		inSpec[op] = true
		if !inRegistry[op] {
			missing = append(missing, op.String())
		}
	}
	for op := range inRegistry {
		if !inSpec[op] {
			extra = append(extra, op.String())
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// DiffNotImplemented compares the registry's 501 expectations against the
// operations allowed to carry one (notImplementedOperations).
//
// unlisted names the cells expecting a 501 for an operation nobody listed —
// the CI-failing case that keeps "no handler exists" a deliberate statement
// rather than a shortcut. stale names listed operations no cell expects a 501
// for any more: the handler landed and the list outlived it. Both come back
// sorted for stable failure output.
func DiffNotImplemented(allowed []Operation, entries []Entry) (unlisted, stale []string) {
	isAllowed := map[Operation]bool{}
	for _, op := range allowed {
		isAllowed[op] = true
	}

	expectsStub := map[Operation]bool{}
	for _, e := range entries {
		op := Operation{Method: e.Method, Path: e.Path}
		for _, principal := range e.Principals() {
			if e.Want[principal] != http.StatusNotImplemented {
				continue
			}
			expectsStub[op] = true
			if !isAllowed[op] {
				unlisted = append(unlisted, fmt.Sprintf("%s [%s] as %s", op, e.Kind, principal))
			}
		}
	}
	for _, op := range allowed {
		if !expectsStub[op] {
			stale = append(stale, op.String())
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	return unlisted, stale
}
