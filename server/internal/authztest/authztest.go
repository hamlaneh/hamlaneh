// Package authztest is the authorization matrix harness (CLAUDE.md testing
// policy). Every contract endpoint registers one Entry declaring the
// expected status for every principal; the matrix test runs the full grid
// against a real server and database, and the completeness test fails the
// build when an endpoint in docs/api/openapi.yaml has no entry.
//
// Registering a new endpoint is a one-line ask per slice: add its Entry
// here, and the harness enforces the user-A-cannot-touch-B matrix for it
// forever.
package authztest

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Principal is a matrix column: who is making the request.
type Principal string

// The four Phase 1.1 principals.
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

// Principals returns every matrix column in stable order.
func Principals() []Principal {
	return []Principal{Anonymous, Member, MemberMustChange, Admin}
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

// Fixture describes the acting user of one matrix cell. Every cell gets a
// fresh user (and session), so cells cannot interfere.
type Fixture struct {
	Username string
	Password string
	// Unique is a per-cell suffix for bodies that must not collide
	// (e.g. admin-created usernames).
	Unique string
}

// Entry is one endpoint's row in the authorization matrix.
type Entry struct {
	Method string
	// Path is the contract path exactly as openapi.yaml spells it,
	// {template} segments included; it is the completeness gate's key.
	Path  string
	Class Class

	// RequestTarget is the concrete target the harness requests when Path
	// is not one itself: real ids substituted for {template} segments,
	// plus any query parameter the contract makes required (search's q,
	// which the generated router rejects with 400 before the security
	// middleware ever runs). Empty means Path is already a valid target.
	RequestTarget string

	// Body builds a minimal valid request body for the acting fixture;
	// nil for bodyless requests.
	Body func(fx Fixture) string

	// Want is the expected status per principal. Every principal must be
	// present (checked by the completeness test).
	Want map[Principal]int

	// WantCode optionally pins the contract error code of non-2xx cells,
	// catching gate-versus-authz mixups that share a status.
	WantCode map[Principal]string
}

// Target returns the request target for this entry.
func (e Entry) Target() string {
	if e.RequestTarget != "" {
		return e.RequestTarget
	}
	return e.Path
}

// Fixture ids for the path-parameterised Phase 1.2 routes. They name
// nothing in the database on purpose: while those endpoints are stubs the
// request never reaches storage, and a well-formed uuid is all the
// generated router needs to bind the path parameter and hand the request
// to the security middleware.
const (
	fixtureChannelID = "11111111-2222-3333-4444-555555555555"
	fixtureUserID    = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	fixtureMessageID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
)

// channelPath builds a target under the fixture channel.
func channelPath(suffix string) string {
	return "/api/v1/channels/" + fixtureChannelID + suffix
}

// sessionStub builds the matrix row of one Phase 1.2 messaging endpoint
// while its handler is still a stub (server/internal/httpserver/
// messaging_stubs.go). target is empty when path is already a concrete
// request target.
//
// The expectations say what the stubs actually support: the route-level
// gates decide first — anonymous 401, still-owes-a-password-change 403 —
// and a caller past both gates reaches a handler that answers 501.
//
// The 501 cells are a statement about missing code, never a permission
// decision. When the messaging slice lands they tighten into real
// per-channel outcomes (a member of the channel 200/201, a non-member 404 so
// a private channel's existence never leaks, an admin who is not a member
// the same 404), and this helper disappears with them.
func sessionStub(method, path, target string) Entry {
	return Entry{
		Method:        method,
		Path:          path,
		Class:         ClassSession,
		RequestTarget: target,
		Want: map[Principal]int{
			Anonymous:        http.StatusUnauthorized,
			MemberMustChange: http.StatusForbidden,
			Member:           http.StatusNotImplemented,
			Admin:            http.StatusNotImplemented,
		},
		WantCode: map[Principal]string{
			Anonymous:        "not_authenticated",
			MemberMustChange: "password_change_required",
			Member:           "not_implemented",
			Admin:            "not_implemented",
		},
	}
}

// sessionEntry builds the matrix row of an implemented session-gated
// endpoint. The route-level gates decide first — anonymous 401,
// still-owes-a-password-change 403 — and a caller past both reaches the
// handler, which answers want (with code, or "" when want is a success).
//
// target is the concrete request target when path carries {template}
// segments, empty when path is already one (same convention as
// sessionStub).
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

// passwordBody re-authenticates as the acting fixture. The TOTP rows that
// need it send the CORRECT password on purpose: what the matrix pins there
// is the account's state, not a re-auth failure that would mask it.
func passwordBody(fx Fixture) string {
	return fmt.Sprintf(`{"password":%q}`, fx.Password)
}

// Registry returns the full authorization matrix for the Phase 1.1 surface.
func Registry() []Entry {
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

		// Phase 1.2 messaging surface — see sessionStub for what these
		// expectations mean and what they become once the slice ships.
		sessionStub(http.MethodGet, "/api/v1/users", ""),
		sessionStub(http.MethodGet, "/api/v1/channels", ""),
		sessionStub(http.MethodPost, "/api/v1/channels", ""),
		sessionStub(http.MethodPost, "/api/v1/dms", ""),
		sessionStub(http.MethodGet, "/api/v1/channels/{channelId}", channelPath("")),
		sessionStub(http.MethodPatch, "/api/v1/channels/{channelId}", channelPath("")),
		sessionStub(http.MethodGet, "/api/v1/channels/{channelId}/members", channelPath("/members")),
		sessionStub(http.MethodPost, "/api/v1/channels/{channelId}/members", channelPath("/members")),
		sessionStub(http.MethodDelete, "/api/v1/channels/{channelId}/members/{userId}",
			channelPath("/members/"+fixtureUserID)),
		sessionStub(http.MethodGet, "/api/v1/channels/{channelId}/messages", channelPath("/messages")),
		sessionStub(http.MethodPost, "/api/v1/channels/{channelId}/messages", channelPath("/messages")),
		sessionStub(http.MethodPatch, "/api/v1/channels/{channelId}/messages/{messageId}",
			channelPath("/messages/"+fixtureMessageID)),
		sessionStub(http.MethodDelete, "/api/v1/channels/{channelId}/messages/{messageId}",
			channelPath("/messages/"+fixtureMessageID)),
		sessionStub(http.MethodPut, "/api/v1/channels/{channelId}/read", channelPath("/read")),
		sessionStub(http.MethodGet, "/api/v1/search", "/api/v1/search?q=hello"),
		sessionStub(http.MethodGet, "/api/v1/ws", ""),

		// Phase 1.1b two-step verification. Real outcomes: every cell below is
		// the handler's own answer for a fresh account, so a Member/Admin cell
		// that is not 501 proves the gates ran AND the handler decided.
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
