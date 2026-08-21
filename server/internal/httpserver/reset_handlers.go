package httpserver

import (
	"net/http"
)

// Password-reset endpoints and the public instance document. Replaced with
// real behaviour by the Phase 1.1b reset slice.
//
// This file is owned by one implementation agent; the TOTP and session
// handlers live in sibling files so the three can land in parallel.

func (s *apiServer) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (s *apiServer) CompletePasswordReset(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

// GetInstance is public: the sign-in screen reads it before anyone has a
// session, to learn the password policy and whether reset is even available.
func (s *apiServer) GetInstance(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
