package httpserver

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Self-service session management — the settings Sessions list. Replaced with
// real behaviour by the Phase 1.1b sessions slice.
//
// This file is owned by one implementation agent; the TOTP and reset handlers
// live in sibling files so the three can land in parallel.

func (s *apiServer) ListMySessions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) RevokeSessionFamily(w http.ResponseWriter, r *http.Request, familyID openapi_types.UUID) {
	_ = familyID
	notImplemented(w, r)
}

func (s *apiServer) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
