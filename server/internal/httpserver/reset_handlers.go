package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Password-reset endpoints and the public instance document.
//
// The policy itself lives in internal/passwordreset and reaches these
// handlers as apiServer.reset, installed by WithPasswordReset at startup. A
// nil service is the honest state of an install with no mail transport; see
// the field's documentation for what each endpoint then answers.

// Contract bounds for the reset pair (oapi-codegen generates models only,
// no validation).
const (
	maxResetEmailLen = 320
	minResetTokenLen = 20
	maxResetTokenLen = 128
)

// RequestPasswordReset accepts a reset request and answers 202 with an
// empty body — the same 202, byte for byte, whether or not the address
// belongs to an account.
//
// Everything that could distinguish the two paths is handled in
// internal/passwordreset: the token is minted and hashed before the lookup,
// the mail is dispatched asynchronously, and both rate-limit keys (the
// presented address, the client IP) are counted for requests that matched
// nothing as well as for those that did.
func (s *apiServer) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req api.PasswordResetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.TrimSpace(string(req.Email))
	if email == "" || utf8.RuneCountInString(email) > maxResetEmailLen || strings.ContainsAny(email, "\r\n") {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "email must be 1 to 320 characters")
		return
	}

	if s.reset == nil {
		resetRequestAccepted(w)
		return
	}

	_, ipKey := clientIP(r)
	err := s.reset.Request(r.Context(), ipKey, email)
	var limited *passwordreset.RateLimitedError
	switch {
	case errors.As(err, &limited):
		writeRateLimited(w, r, limited.RetryAfter)
	case err != nil:
		// Deliberately not surfaced. Storing a token and dispatching mail
		// only happen for addresses that exist, so answering 500 when they
		// fail would tell the caller that this one does.
		slog.Error("password reset request", "path", r.URL.Path, "error", err)
		resetRequestAccepted(w)
	default:
		resetRequestAccepted(w)
	}
}

// resetRequestAccepted writes the one accepted response. Every success path
// goes through it so no branch can drift into a different body, header, or
// status.
func resetRequestAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
}

// CompletePasswordReset consumes an emailed token and installs the new
// password. Unknown, expired and already-used tokens answer identically, so
// a replayed link reveals nothing about which of the three it was.
//
// The reset revokes every session family — a reset means the old password
// may be in someone else's hands, so no device stays signed in — and sets
// no cookies: the user signs in fresh, through two-step verification if
// their account has it, because the emailed token proved control of a
// mailbox and never of an authenticator.
func (s *apiServer) CompletePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req api.PasswordResetCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if n := utf8.RuneCountInString(req.Token); n < minResetTokenLen || n > maxResetTokenLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "token must be 20 to 128 characters")
		return
	}
	// The password policy is checked before the token is touched: a
	// rejected password must not burn a link the user then cannot reuse.
	if err := uservalidate.Password(req.NewPassword); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "new_password "+err.Error())
		return
	}

	if s.reset == nil {
		writeInvalidResetToken(w, r)
		return
	}

	_, ipKey := clientIP(r)
	err := s.reset.Complete(r.Context(), ipKey, req.Token, req.NewPassword)
	var limited *passwordreset.RateLimitedError
	switch {
	case errors.As(err, &limited):
		writeRateLimited(w, r, limited.RetryAfter)
	case errors.Is(err, passwordreset.ErrInvalidToken):
		writeInvalidResetToken(w, r)
	case err != nil:
		internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// writeInvalidResetToken is the single source of the reset-failure
// response. Unknown, expired and already-used tokens MUST answer through
// this one call site so the three stay indistinguishable.
func writeInvalidResetToken(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusUnauthorized, codeInvalidResetToken,
		"reset link is invalid or has expired")
}

// GetInstance is public: the sign-in screen reads it before anyone has a
// session, to learn the password policy and whether reset is even
// available.
//
// password_min_length comes from internal/uservalidate and
// max_file_size_bytes from the constant UploadFile enforces — the same
// constants the server validates against, so the form and the check can
// never drift.
// password_reset_available is false when no mail transport is configured,
// so the screen omits a link that would go nowhere.
func (s *apiServer) GetInstance(w http.ResponseWriter, r *http.Request) {
	info := api.InstanceInfo{
		MaxFileSizeBytes:       maxUploadBytes,
		PasswordMinLength:      uservalidate.MinPasswordLen,
		PasswordResetAvailable: s.reset != nil && s.reset.Available(),
		// sso.enabled follows CONFIGURATION, not the provider's health: the
		// button exists whenever the door does, and a provider that is down
		// answers 503 at the door rather than hiding it.
		Sso: &api.SsoStatus{Enabled: s.sso != nil},
	}
	if s.sso != nil {
		// Present whenever enabled (the contract): ProviderName defaults
		// rather than returning empty, so the pair cannot desynchronize.
		name := s.sso.ProviderName()
		info.Sso.ProviderName = &name
	}
	writeJSONValue(w, r, http.StatusOK, info)
}
