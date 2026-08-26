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
// An account with two-step verification on stops one step short of that: the
// password earns a challenge cookie and 202, and only CompleteTotpLogin
// mints its session (see challengeIfTwoStep in totp_handlers.go).
//
// The rate limiters count failed authentications and two-step challenge
// mints (see consumeLoginBudget for why a mint is an attempt): both keys are
// checked (without consuming anything) before any database or argon2 work,
// and a login that ends in a session records nothing, so completed sign-ins
// from a shared IP never lock legitimate users out.
func (s *apiServer) Login(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		internalError(w, r, errNoStorage)
		return
	}

	addr, ipKey := clientIP(r)
	if s.loginIPLimiter.Limited(ipKey) {
		writeRateLimited(w, r, s.loginIPLimiter.RetryAfter(ipKey))
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
		writeRateLimited(w, r, s.loginIdentifierLimiter.RetryAfter(identifierKey))
		return
	}

	user, err := s.store.UserByIdentifier(r.Context(), req.Identifier)
	if errors.Is(err, storage.ErrNotFound) {
		// Burn the same argon2 work as a real verification so unknown-user
		// and wrong-password attempts are indistinguishable by timing, then
		// answer through the same single call site.
		password.CompareDummy(req.Password)
		s.consumeLoginBudget(ipKey, identifierKey)
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
		s.consumeLoginBudget(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}
	if !ok {
		s.consumeLoginBudget(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}

	if !user.IsActive {
		// A deactivated account cannot sign in. The refusal is deliberately
		// the same invalid_credentials every other failure answers with: an
		// account's state is not something an unauthenticated caller gets to
		// learn, and "wrong password" and "this person has left" must look
		// identical from outside. Deactivation already revoked every session
		// the account held, so this is the only door left to close.
		s.consumeLoginBudget(ipKey, identifierKey)
		writeInvalidCredentials(w, r)
		return
	}

	if needsRehash {
		s.rehashPassword(r.Context(), user.ID, req.Password)
	}

	// The password is only the first half for an account with two-step
	// verification on: challengeIfTwoStep answers 202 with the challenge
	// cookie and nothing else, and no session is minted here. It fails closed
	// — anything it cannot determine is answered by it, not fallen through.
	// It receives both limiter keys because minting a challenge spends login
	// budget exactly as a failed password would.
	if s.challengeIfTwoStep(w, r, user.ID, ipKey, identifierKey) {
		return
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

// consumeLoginBudget counts one login attempt against both login rate-limit
// keys. Exactly two kinds of attempt spend it, and both leave the attacker
// wanting another go:
//
//   - a failed authentication (unknown identifier or wrong password), and
//   - a two-step challenge mint — the password was CORRECT, so this is not
//     recorded as a failure but as spending the login-attempt budget: every
//     mint opens a fresh five-guess code window, and if minting were free an
//     attacker holding the password could retry the second factor without
//     limit (mint, burn five guesses, mint again). Metering the mint is what
//     kills that loop at its source.
//
// A login that ends in a session consumes nothing (a completed two-step
// sign-in included), and internal errors are not attempts.
func (s *apiServer) consumeLoginBudget(ipKey, identifierKey string) {
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
