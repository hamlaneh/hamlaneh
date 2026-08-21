package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Contract bounds enforced server-side (oapi-codegen generates models only,
// no validation). Account credential bounds live in internal/uservalidate.
const (
	maxIdentifierLen = 320
	maxUserAgentLen  = 512 // sessions.user_agent CHECK constraint
)

// Login authenticates with username/email and password:
// rate-limit check → validate → authenticate → create a session family →
// set the three session cookies → 200 with the user.
//
// The rate limiters count only failed authentications: both keys are
// checked (without consuming anything) before any database or argon2 work,
// and recorded only when authentication fails, so successful logins from a
// shared IP never lock legitimate users out.
func (s *apiServer) Login(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		internalError(w, r, errNoStorage)
		return
	}

	addr, ipKey := clientIP(r)
	if s.loginIPLimiter.Limited(ipKey) {
		writeError(w, r, http.StatusTooManyRequests, codeRateLimited, "too many attempts, try again later")
		return
	}

	var req api.LoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Identifier == "" || utf8.RuneCountInString(req.Identifier) > maxIdentifierLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "identifier must be 1 to 320 characters")
		return
	}
	if req.Password == "" || utf8.RuneCountInString(req.Password) > uservalidate.MaxPasswordLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "password must be 1 to 1024 characters")
		return
	}

	identifierKey := strings.ToLower(req.Identifier)
	if s.loginIdentifierLimiter.Limited(identifierKey) {
		writeError(w, r, http.StatusTooManyRequests, codeRateLimited, "too many attempts, try again later")
		return
	}

	user, err := s.store.UserByIdentifier(r.Context(), req.Identifier)
	if errors.Is(err, storage.ErrNotFound) {
		// Burn the same argon2 work as a real verification so unknown-user
		// and wrong-password attempts are indistinguishable by timing, then
		// answer through the same single call site.
		password.CompareDummy(req.Password)
		s.recordLoginFailure(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	ok, needsRehash, err := password.Verify(req.Password, user.PasswordHash)
	if err != nil {
		// A malformed stored hash is an internal defect. Fail closed with
		// the standard response so nothing about the account leaks, and
		// scream in the logs.
		slog.Error("stored password hash is malformed", "user_id", user.ID, "error", err)
		s.recordLoginFailure(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}
	if !ok {
		s.recordLoginFailure(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}

	if needsRehash {
		s.rehashPassword(r.Context(), user.ID, req.Password)
	}

	tokens, cookies := mintSessionTokens()
	tokens.UserAgent = sanitizedUserAgent(r)
	tokens.IP = ipParam(addr)

	if _, err := s.store.CreateSession(r.Context(), storage.NewSession{
		UserID:        user.ID,
		SessionTokens: tokens,
	}); err != nil {
		internalError(w, r, err)
		return
	}

	session.SetCookies(w, cookies)
	writeJSONValue(w, r, http.StatusOK, apiUser(user))
}

// recordLoginFailure counts one failed authentication against both login
// rate-limit keys. It runs only on authentication failures — successful
// logins consume no budget, and internal errors are not attempts.
func (s *apiServer) recordLoginFailure(ipKey, identifierKey string) {
	s.loginIPLimiter.Record(ipKey)
	s.loginIdentifierLimiter.Record(identifierKey)
}

// Logout revokes the whole session family of the presented session and
// clears all session cookies. Session and CSRF checks ran in middleware.
func (s *apiServer) Logout(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("logout reached without principal"))
		return
	}
	if err := s.store.RevokeFamily(r.Context(), prin.session.FamilyID); err != nil {
		internalError(w, r, err)
		return
	}
	session.SetCookies(w, session.ClearCookies())
	w.WriteHeader(http.StatusNoContent)
}

// RefreshSession rotates the refresh token: a valid token yields new access
// and refresh cookies (204); a used token trips reuse detection and revokes
// the family; anything invalid answers 401 with all cookies cleared.
func (s *apiServer) RefreshSession(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		internalError(w, r, errNoStorage)
		return
	}

	c, err := r.Cookie(session.RefreshCookie)
	if err != nil || c.Value == "" {
		refreshFailed(w, r)
		return
	}

	accessRaw, accessHash := session.NewToken()
	refreshRaw, refreshHash := session.NewToken()

	addr, _ := clientIP(r)
	_, outcome, err := s.store.RotateSession(r.Context(), session.HashToken(c.Value), storage.SessionTokens{
		AccessTokenHash:  accessHash,
		RefreshTokenHash: refreshHash,
		AccessTTL:        session.AccessTTL,
		RefreshTTL:       session.RefreshTTL,
		UserAgent:        sanitizedUserAgent(r),
		IP:               ipParam(addr),
	})
	if err != nil {
		internalError(w, r, err)
		return
	}

	if outcome != storage.RotateOutcomeRotated {
		// Reuse detection and plain invalidity answer identically; the
		// family revocation on reuse already happened inside RotateSession.
		refreshFailed(w, r)
		return
	}
	session.SetCookies(w, session.RotatedCookies(accessRaw, refreshRaw))
	w.WriteHeader(http.StatusNoContent)
}

// refreshFailed answers a failed refresh: cookies cleared, 401.
func refreshFailed(w http.ResponseWriter, r *http.Request) {
	session.SetCookies(w, session.ClearCookies())
	writeError(w, r, http.StatusUnauthorized, codeNotAuthenticated, msgNotAuthenticated)
}

// ChangePassword verifies the current password, stores the new hash,
// clears must_change_password, and revokes every other session family.
// It is reachable while the must-change gate is up — that is its job.
func (s *apiServer) ChangePassword(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("change-password reached without principal"))
		return
	}

	var req api.ChangePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CurrentPassword == "" || utf8.RuneCountInString(req.CurrentPassword) > uservalidate.MaxPasswordLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "current_password must be 1 to 1024 characters")
		return
	}

	ok, _, err := password.Verify(req.CurrentPassword, prin.user.PasswordHash)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusForbidden, codeInvalidCurrentPassword, "current password is incorrect")
		return
	}

	if vErr := uservalidate.Password(req.NewPassword); vErr != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "new_password "+vErr.Error())
		return
	}

	newHash := password.Hash(req.NewPassword)
	if err := s.store.UpdatePassword(r.Context(), prin.user.ID, newHash, prin.session.FamilyID); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mintSessionTokens generates the access, refresh, and CSRF tokens for a
// fresh login, returning the storage-side hashes and the cookies to set.
func mintSessionTokens() (storage.SessionTokens, []*http.Cookie) {
	accessRaw, accessHash := session.NewToken()
	refreshRaw, refreshHash := session.NewToken()
	csrf := session.NewCSRFToken()

	tokens := storage.SessionTokens{
		AccessTokenHash:  accessHash,
		RefreshTokenHash: refreshHash,
		AccessTTL:        session.AccessTTL,
		RefreshTTL:       session.RefreshTTL,
	}
	return tokens, session.Cookies(accessRaw, refreshRaw, csrf)
}

// rehashPassword transparently upgrades a stored hash to the current argon2
// parameters after a successful login. Failures are logged, never surfaced
// — the login itself already succeeded.
func (s *apiServer) rehashPassword(ctx context.Context, userID uuid.UUID, plaintext string) {
	if err := s.store.UpdatePasswordHash(ctx, userID, password.Hash(plaintext)); err != nil {
		slog.Error("rehash on login: store", "user_id", userID, "error", err)
	}
}

// sanitizedUserAgent returns the User-Agent header coerced to valid UTF-8
// and truncated to the schema's 512-character bound.
func sanitizedUserAgent(r *http.Request) string {
	ua := strings.ToValidUTF8(r.UserAgent(), string(utf8.RuneError))
	if utf8.RuneCountInString(ua) <= maxUserAgentLen {
		return ua
	}
	runes := []rune(ua)
	return string(runes[:maxUserAgentLen])
}

// ipParam adapts clientIP's result for the nullable inet column.
func ipParam(addr netip.Addr) *netip.Addr {
	if !addr.IsValid() {
		return nil
	}
	return &addr
}
