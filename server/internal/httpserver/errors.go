package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
)

// errorCode is a stable machine-readable error code from the Phase 1.1
// error contract. Clients localize by code; messages are stable English
// and never carry internal details.
type errorCode string

const (
	codeInvalidRequest         errorCode = "invalid_request"
	codeInvalidCredentials     errorCode = "invalid_credentials" // #nosec G101 -- a stable error code in the API contract, not a credential
	codeNotAuthenticated       errorCode = "not_authenticated"
	codeForbidden              errorCode = "forbidden"
	codeCSRFFailed             errorCode = "csrf_failed"
	codePasswordChangeRequired errorCode = "password_change_required"
	codeInvalidCurrentPassword errorCode = "invalid_current_password"

	// Phase 1.1b: two-step verification, password reset, session management.
	codeInvalidTOTPCode      errorCode = "invalid_totp_code"
	codeTOTPAlreadyEnabled   errorCode = "totp_already_enabled"
	codeTOTPSetupExpired     errorCode = "totp_setup_expired"
	codeTOTPSetupNotVerified errorCode = "totp_setup_not_verified"
	codeTOTPNotEnabled       errorCode = "totp_not_enabled"
	// One answer for unknown, expired and already-used reset tokens, so a
	// replayed link cannot tell an attacker which of the three it hit.
	codeInvalidResetToken   errorCode = "invalid_reset_token"
	codeCannotRevokeCurrent errorCode = "cannot_revoke_current_session"
	codeSessionNotFound     errorCode = "session_not_found"
	codeRateLimited         errorCode = "rate_limited"
	codeUsernameTaken       errorCode = "username_taken"
	codeEmailTaken          errorCode = "email_taken"
	codeInternalError       errorCode = "internal_error"

	// Phase 1.2: conversations. codeChannelNotFound is the answer to "no
	// such channel for this caller" — an unknown id and a channel the
	// caller is not in are deliberately indistinguishable, so a channel's
	// existence never leaks (openapi.yaml components.responses.NotFound).
	codeChannelNotFound   errorCode = "channel_not_found"
	codeUserNotFound      errorCode = "user_not_found"
	codeChannelSlugTaken  errorCode = "channel_slug_taken"
	codeDMMembershipFixed errorCode = "dm_membership_fixed"
	// The two message-moderation refusals. They are separate codes because
	// they are separate rules: editing is the author's alone, admins
	// included, while deleting is the author's or an admin member's. A
	// client localizes by code, so collapsing them would tell an admin they
	// cannot delete a message they can.
	codeNotMessageAuthor        errorCode = "not_message_author"
	codeNotMessageAuthorOrAdmin errorCode = "not_message_author_or_admin"
	// codeMessageDeleted answers an edit of a message that is already
	// deleted: its row is the placeholder the design draws, and words must
	// not reappear on one.
	codeMessageDeleted errorCode = "message_deleted"
	// codeNotFound answers a path under /api that no contract route claims.
	// It is the router's answer, not a resource's: the contract's
	// resource-level 404s carry their own codes (session_not_found,
	// channel_not_found, user_not_found).
	codeNotFound errorCode = "not_found"
)

// codeNotImplemented marks a contract endpoint whose behavior has not
// shipped. It left this file when slice 1.2b landed the last Phase 1.2
// handler, and returned with the Phase 1.3 contract: uploadFile is specified
// ahead of its implementation, exactly the contract-first shape the authz
// matrix gate (authztest.notImplementedOperations) exists to make deliberate
// — the operation is on that list, and a handler landing without tightening
// its row turns the matrix red.
const codeNotImplemented errorCode = "not_implemented"

// Stable messages for codes written from more than one call site.
const (
	msgNotAuthenticated = "authentication required"
	msgForbidden        = "not allowed"
	msgInternalError    = "internal server error"
	msgNotFound         = "no such endpoint"
)

// errorFallbackBody answers when even marshalling the error envelope fails;
// it must stay in sync with the Error contract schema.
const errorFallbackBody = `{"error":{"code":"internal_error","message":"internal server error"}}`

// writeError sends the contract's Error shape:
// {"error":{"code":...,"message":...}}.
func writeError(w http.ResponseWriter, r *http.Request, status int, code errorCode, message string) {
	var body api.Error
	body.Error.Code = string(code)
	body.Error.Message = message

	data, err := json.Marshal(body)
	if err != nil {
		// Unreachable for this shape; keep the response well-formed anyway.
		slog.Error("marshal error response", "path", r.URL.Path, "error", err)
		status = http.StatusInternalServerError
		data = []byte(errorFallbackBody)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeBody(w, r, data)
}

// writeRateLimited is the single source of every 429 this server answers:
// the contract Error shape plus a Retry-After header carrying how long the
// caller's budget stays exhausted, so a client can show a real countdown
// instead of guessing. Nothing else writes a rate-limited status — that is
// what makes "every 429 carries the header" a property of the code rather
// than a habit each handler has to remember.
//
// Seconds are rounded up, never below 1: a Retry-After of 0 invites an
// immediate retry of the request that was just refused.
func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, r, http.StatusTooManyRequests, codeRateLimited, "too many attempts, try again later")
}

// writeInvalidCredentials is the single source of the login-failure
// response. Unknown identifier and wrong password MUST answer through this
// one call site so the responses stay byte-identical (no account
// enumeration).
func writeInvalidCredentials(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusUnauthorized, codeInvalidCredentials, "invalid credentials")
}

// internalError logs the real cause server-side and answers with the
// generic 500 envelope; details never reach the client.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, r, http.StatusInternalServerError, codeInternalError, msgInternalError)
}

// requestErrorHandler replaces oapi-codegen's plain-text 400 for malformed
// request parameters with the contract's JSON Error shape. The underlying
// error is deliberately not echoed to the client.
func requestErrorHandler(w http.ResponseWriter, r *http.Request, _ error) {
	writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "malformed request parameters")
}

// writeJSONValue marshals v and sends it with the given status.
func writeJSONValue(w http.ResponseWriter, r *http.Request, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeBody(w, r, data)
}

// decodeJSON reads a bounded JSON request body into dst. On any failure —
// oversized body, malformed JSON, wrong field types, trailing content — it
// answers 400 invalid_request and reports false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	const maxJSONBody = 64 << 10 // far above any valid request; bounds DoS

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "malformed request body")
		return false
	}
	if dec.More() {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "malformed request body")
		return false
	}
	return true
}
