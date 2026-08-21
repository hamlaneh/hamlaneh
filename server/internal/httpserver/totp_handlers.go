package httpserver

import (
	"net/http"
)

// Two-step verification endpoints. Replaced with real behaviour by the
// Phase 1.1b TOTP slice; until then every route answers 501 AFTER the
// route-level gates have run, so a 501 is never a permission answer.
//
// This file is owned by one implementation agent. Sibling handler files exist
// so the reset and session slices can land in parallel without touching it.

func (s *apiServer) GetTotpStatus(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) StartTotpSetup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) VerifyTotpSetup(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) ActivateTotp(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) DisableTotp(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// CompleteTotpLogin is the second half of a two-step sign-in. It is gated by
// the challenge cookie rather than a session, mirroring RefreshSession.
func (s *apiServer) CompleteTotpLogin(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
