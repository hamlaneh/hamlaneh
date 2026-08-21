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
	Path   string
	Class  Class

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
