package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/totp"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Two-step verification (Phase 1.1b). Setting it up is three steps —
// setup, verify, activate — and the account stays password-only until the
// last one, so no path can leave a user locked out by a factor they never
// proved or a fallback they were never shown.
//
// Signing in is two halves: the password half answers 202 and sets the
// challenge cookie, and this file's CompleteTotpLogin is the only place in
// the server that mints session cookies for such an account.

// Challenge cookie: the half-authenticated state between the password step
// and the code step. It is HttpOnly, Secure and SameSite=Strict like the
// session cookies, and path-scoped to the one endpoint that completes the
// sign-in, so it never rides along on any other request. It carries no
// authority anywhere else: routePolicies classifies exactly one route as
// classChallengeCookie.
const (
	challengeCookieName = "hamlaneh_2fa"
	challengeCookiePath = "/api/v1/auth/login/totp"
)

// Contract bounds for TotpLoginRequest.code (openapi.yaml).
const (
	minLoginCodeLen = 6
	maxLoginCodeLen = 16
)

// GetTotpStatus reports the caller's two-step state for the Security card.
func (s *apiServer) GetTotpStatus(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp status")
	if !ok {
		return
	}

	record, err := store.TotpByUser(r.Context(), prin.user.ID)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && !record.Enabled()) {
		// A pending setup is not a state the card shows: until activate, the
		// account is password-only and the card offers to set it up.
		writeJSONValue(w, r, http.StatusOK, api.TotpStatus{Enabled: false})
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	remaining, total, err := store.RecoveryCodeCounts(r.Context(), prin.user.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.TotpStatus{
		Enabled:                true,
		ActivatedAt:            record.ActivatedAt,
		RecoveryCodesRemaining: &remaining,
		RecoveryCodesTotal:     &total,
	})
}

// StartTotpSetup is step 1: a fresh secret, its manual key, its otpauth URI
// and a QR rendered from it on the spot. Calling it again before activation
// replaces the pending setup, which is why cancelling needs no endpoint.
func (s *apiServer) StartTotpSetup(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp setup")
	if !ok {
		return
	}

	secret := totp.NewSecret()
	enrollment, err := totp.Enroll(secret, prin.user.Username)
	if err != nil {
		internalError(w, r, err)
		return
	}

	err = store.StartTotpSetup(r.Context(), prin.user.ID, secret, totp.SetupTTL)
	if errors.Is(err, storage.ErrTotpAlreadyEnabled) {
		writeError(w, r, http.StatusConflict, codeTOTPAlreadyEnabled,
			"two-step verification is already on")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	writeNoStore(w, r, http.StatusOK, api.TotpSetup{
		Secret:     enrollment.ManualKey,
		OtpauthUri: enrollment.OtpauthURI,
		QrSvg:      enrollment.QRSVG,
	})
}

// VerifyTotpSetup is step 2: the first authenticator code proves the app was
// enrolled, and the ten recovery codes come back to be shown exactly once.
// Two-step verification is still off afterwards.
func (s *apiServer) VerifyTotpSetup(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp verify")
	if !ok {
		return
	}

	var req api.VerifyTotpSetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !totp.IsAuthenticatorCode(req.Code) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "code must be six digits")
		return
	}

	codes := totp.NewRecoveryCodes(totp.RecoveryCodeCount)
	now := time.Now()
	outcome, err := store.VerifyTotpSetup(r.Context(), storage.TotpSetupVerification{
		UserID:      prin.user.ID,
		MaxAttempts: totp.MaxSetupAttempts,
		CheckCode: func(secret []byte, lastUsedStep *int64) (int64, bool) {
			return totp.Verify(secret, req.Code, now, lastUsedStep)
		},
		RecoveryCodeHashes: func() []string { return hashRecoveryCodes(codes) },
	})
	if err != nil {
		internalError(w, r, err)
		return
	}

	switch outcome {
	case storage.TotpVerifyAccepted:
		writeNoStore(w, r, http.StatusOK, api.RecoveryCodes{Codes: codes})
	case storage.TotpVerifyRejected:
		// The secret survives a wrong code: the client clears the cells and
		// the user tries again without restarting setup.
		writeError(w, r, http.StatusForbidden, codeInvalidTOTPCode, "that code is not valid")
	case storage.TotpVerifyRevoked, storage.TotpVerifyNoSetup:
		writeError(w, r, http.StatusConflict, codeTOTPSetupExpired,
			"there is no pending setup; start again")
	default:
		// Unreachable while the cases above cover the enum, and the
		// exhaustive linter keeps them covering it. It exists because the
		// failure mode without it is silent: a fifth constant would fall
		// through writing nothing, and net/http would then send 200 with an
		// empty body — a success shape for an outcome nobody classified.
		internalError(w, r, fmt.Errorf("unhandled totp verify outcome %q (%d)", outcome, outcome))
	}
}

// ActivateTotp is step 3, behind the saved-the-codes acknowledgement. Only a
// verified, unexpired setup holding recovery codes turns on.
func (s *apiServer) ActivateTotp(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp activate")
	if !ok {
		return
	}

	_, err := store.ActivateTotp(r.Context(), prin.user.ID)
	if errors.Is(err, storage.ErrTotpSetupNotVerified) {
		writeError(w, r, http.StatusConflict, codeTOTPSetupNotVerified,
			"there is no verified setup to activate")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DisableTotp turns two-step verification off after re-asking for the
// password. Sessions are deliberately left alone — see the contract.
func (s *apiServer) DisableTotp(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp disable")
	if !ok {
		return
	}
	// The two-step settings budget was already spent by rateLimitMiddleware,
	// which is what puts it ahead of confirmPassword: the argon2id
	// verification below is the cost that budget exists to bound, and a 429
	// written after it would have paid for what it refused.
	if !s.confirmPassword(w, r, prin) {
		return
	}

	err := store.DisableTotp(r.Context(), prin.user.ID)
	if errors.Is(err, storage.ErrTotpNotEnabled) {
		writeError(w, r, http.StatusConflict, codeTOTPNotEnabled, "two-step verification is not on")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RegenerateRecoveryCodes replaces the whole set after re-asking for the
// password: minting sign-in credentials deserves the same posture as
// disabling the second factor.
func (s *apiServer) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	prin, store, ok := s.totpPrincipal(w, r, "totp recovery codes")
	if !ok {
		return
	}
	// Spent by rateLimitMiddleware before this handler ran, which is what
	// keeps it ahead of the argon2id verification below — see DisableTotp.
	if !s.confirmPassword(w, r, prin) {
		return
	}

	// The codes are generated eagerly (a crypto/rand read) but hashed
	// lazily: the store calls the callback inside its transaction, once it
	// knows the account has a second factor to reissue for, so a refused
	// regeneration costs no argon2id work.
	codes := totp.NewRecoveryCodes(totp.RecoveryCodeCount)
	err := store.ReplaceRecoveryCodes(r.Context(), prin.user.ID,
		func() []string { return hashRecoveryCodes(codes) })
	if errors.Is(err, storage.ErrTotpNotEnabled) {
		writeError(w, r, http.StatusConflict, codeTOTPNotEnabled, "two-step verification is not on")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeNoStore(w, r, http.StatusOK, api.RecoveryCodes{Codes: codes})
}

// CompleteTotpLogin is the second half of a two-step sign-in. It is gated by
// the challenge cookie rather than a session, mirroring RefreshSession, and
// it is the only place session cookies are minted for an account with
// two-step verification on.
//
// It is rate limited per client IP and per account (the contract's 429),
// mirroring Login's shape: windows are checked before the guarded work and
// only failed attempts are recorded. This limiter is independent of the
// per-challenge attempt cap on purpose — the cap bounds one challenge,
// while these windows keep counting across challenge boundaries, so burning
// a challenge and minting a fresh one buys no new guessing budget here.
func (s *apiServer) CompleteTotpLogin(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	addr, ipKey := clientIP(r)
	if s.totpIPLimiter.Limited(ipKey) {
		writeRateLimited(w, r, s.totpIPLimiter.RetryAfter(ipKey))
		return
	}

	c, err := r.Cookie(challengeCookieName)
	if err != nil || c.Value == "" {
		challengeFailed(w, r)
		return
	}

	var req api.TotpLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if n := utf8.RuneCountInString(req.Code); n < minLoginCodeLen || n > maxLoginCodeLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "code must be 6 to 16 characters")
		return
	}

	tokenHash := session.HashToken(c.Value)

	// The per-account window must be consulted BEFORE the code is evaluated
	// — a 429 written after CompleteTotpChallenge ran would not have stopped
	// the guess — so the account behind the token is resolved first. It is
	// keyed on the user id, not the challenge token, precisely so that it
	// survives challenge turnover: a distributed attacker rotating IPs and
	// challenges still runs into one budget per victim. A token matching no
	// challenge names no account to limit; the main call classifies it as
	// TotpChallengeNone below and answers 401.
	accountKey := ""
	challengeUser, err := store.TotpChallengeUserByTokenHash(r.Context(), tokenHash)
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		internalError(w, r, err)
		return
	default:
		accountKey = challengeUser.String()
		if s.totpAccountLimiter.Limited(accountKey) {
			writeRateLimited(w, r, s.totpAccountLimiter.RetryAfter(accountKey))
			return
		}
	}

	tokens, cookies := mintSessionTokens()
	tokens.UserAgent = sanitizedUserAgent(r)
	tokens.IP = ipParam(addr)

	att := storage.TotpChallengeAttempt{
		TokenHash:   tokenHash,
		MaxAttempts: totp.MaxChallengeAttempts,
		Session:     tokens,
	}
	// Exactly one shape is tried, so a six-digit guess never costs ten argon2
	// comparisons and a recovery code is never checked against the clock.
	// A code that is neither shape still costs an attempt.
	now := time.Now()
	switch normalized, isRecovery := totp.NormalizeRecoveryCode(req.Code); {
	case totp.IsAuthenticatorCode(req.Code):
		att.CheckCode = func(secret []byte, lastUsedStep *int64) (int64, bool) {
			return totp.Verify(secret, req.Code, now, lastUsedStep)
		}
	case isRecovery:
		att.MatchRecoveryCode = func(storedHash string) bool {
			matched, _, matchErr := password.Verify(normalized, storedHash)
			return matchErr == nil && matched
		}
	}

	user, _, outcome, err := store.CompleteTotpChallenge(r.Context(), att)
	if err != nil {
		internalError(w, r, err)
		return
	}

	switch outcome {
	case storage.TotpChallengeCompleted:
		// A completed sign-in records nothing, matching Login's rule that
		// only attempts an attacker would want to repeat spend budget.
		http.SetCookie(w, challengeCookie("", -1))
		session.SetCookies(w, cookies)
		writeJSONValue(w, r, http.StatusOK, apiUser(user))
	case storage.TotpChallengeRejected:
		// The challenge survives: the cells clear and the caller retries.
		s.recordTotpFailure(ipKey, accountKey)
		writeError(w, r, http.StatusUnauthorized, codeInvalidTOTPCode, "that code is not valid")
	case storage.TotpChallengeRevoked:
		s.recordTotpFailure(ipKey, accountKey)
		challengeFailed(w, r)
	case storage.TotpChallengeNone:
		// No live challenge answered to the token, so no code was checked
		// against any account — but it is still a failed attempt by this
		// client, and counting it bounds how hard a dead cookie can hammer
		// the challenge lookup.
		s.totpIPLimiter.Record(ipKey)
		challengeFailed(w, r)
	default:
		// Fail closed. Without this the endpoint that guards the second
		// factor would answer a future unclassified outcome by writing
		// nothing at all, which net/http turns into 200 OK with an empty
		// body — indistinguishable, to a client, from a completed sign-in.
		s.recordTotpFailure(ipKey, accountKey)
		internalError(w, r, fmt.Errorf("unhandled totp challenge outcome %q (%d)", outcome, outcome))
	}
}

// recordTotpFailure counts one wrong sign-in code against the two-step
// limiter keys. accountKey is never empty here in practice — a wrong code
// implies a live challenge, which the pre-lookup resolved — but the guard
// keeps a future refactor from recording an empty-string account bucket.
func (s *apiServer) recordTotpFailure(ipKey, accountKey string) {
	s.totpIPLimiter.Record(ipKey)
	if accountKey != "" {
		s.totpAccountLimiter.Record(accountKey)
	}
}

// challengeIfTwoStep answers a password-verified login on an account with
// two-step verification on: it mints the challenge, spends one unit of the
// login-attempt budget on both keys, sets the cookie, and writes 202 with
// the methods the client may use. It reports whether it answered the
// request; false means the caller should mint a session as usual. Every
// failure inside is answered here, never by falling through — a two-step
// account must not be able to sign in on the password alone.
//
// The budget spend is what makes challenges finite: Login checks both
// limiters before any work, so once the mints (plus any failures) fill a
// window, the next password login answers 429 instead of opening yet
// another five-guess code window. It is recorded only when a challenge
// actually came into existence — internal errors are not attempts.
//
// Its one caller is Login, in auth_handlers.go, which calls it after the
// password is verified and before any session token is minted.
func (s *apiServer) challengeIfTwoStep(w http.ResponseWriter, r *http.Request, userID uuid.UUID, ipKey, identifierKey string) bool {
	store, ok := s.requireStore(w, r)
	if !ok {
		return true
	}

	record, err := store.TotpByUser(r.Context(), userID)
	if errors.Is(err, storage.ErrNotFound) {
		return false
	}
	if err != nil {
		internalError(w, r, err)
		return true
	}
	if !record.Enabled() {
		return false
	}

	raw, hash := session.NewToken()
	if err := store.CreateTotpChallenge(r.Context(), userID, hash, totp.ChallengeTTL); err != nil {
		internalError(w, r, err)
		return true
	}
	s.consumeLoginBudget(ipKey, identifierKey)

	http.SetCookie(w, challengeCookie(raw, int(totp.ChallengeTTL.Seconds())))
	writeJSONValue(w, r, http.StatusAccepted, api.TwoFactorChallenge{
		Methods: []api.TwoFactorChallengeMethods{api.Totp},
	})
	return true
}

// totpPrincipal resolves the request principal and the store together, the
// pair every settings handler needs.
func (s *apiServer) totpPrincipal(w http.ResponseWriter, r *http.Request, name string) (principal, Store, bool) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New(name+" reached without principal"))
		return principal{}, nil, false
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return principal{}, nil, false
	}
	return prin, store, true
}

// confirmPassword enforces the re-authentication the contract requires
// before the second factor changes: a hijacked session must not be able to
// turn two-step verification off or mint fresh sign-in codes. It answers the
// failure itself and reports whether the password was correct.
func (s *apiServer) confirmPassword(w http.ResponseWriter, r *http.Request, prin principal) bool {
	var req api.PasswordConfirmRequest
	if !decodeJSON(w, r, &req) {
		return false
	}
	if req.Password == "" || utf8.RuneCountInString(req.Password) > uservalidate.MaxPasswordLen {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "password must be 1 to 1024 characters")
		return false
	}

	ok, _, err := password.Verify(req.Password, prin.user.PasswordHash)
	if err != nil {
		internalError(w, r, err)
		return false
	}
	if !ok {
		writeError(w, r, http.StatusForbidden, codeInvalidCurrentPassword, "current password is incorrect")
		return false
	}
	return true
}

// hashRecoveryCodes turns freshly generated codes into what is stored:
// argon2id, in the same format as a password hash. A bare digest of forty
// bits would be brute-forced offline in minutes.
func hashRecoveryCodes(codes []string) []string {
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, password.Hash(code))
	}
	return hashes
}

// challengeFailed answers a sign-in attempt with no live challenge behind
// it: the cookie is cleared and the caller returns to the password step. It
// is deliberately identical for a missing, expired, consumed and
// attempt-capped challenge.
func challengeFailed(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, challengeCookie("", -1))
	writeError(w, r, http.StatusUnauthorized, codeNotAuthenticated, msgNotAuthenticated)
}

// challengeCookie builds the two-step challenge cookie. Attributes must
// match between setting and clearing it, or browsers keep the original.
func challengeCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     challengeCookieName,
		Value:    value,
		Path:     challengeCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// writeNoStore sends a JSON body that must never be cached: setup material
// and recovery codes are shown once and are credentials in transit.
func writeNoStore(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSONValue(w, r, status, v)
}
