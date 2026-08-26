// Package session owns the browser-facing half of Hamlaneh sessions: opaque
// token generation and hashing, the three session cookies and their exact
// attributes, and the CSRF double-submit comparison.
//
// Token model (Phase 1.1 security design): tokens are 32 bytes from
// crypto/rand, base64url-encoded into cookies, and stored server-side only
// as SHA-256 digests — a database leak reveals no usable token. The
// database half (families, rotation, reuse detection) lives in
// internal/storage.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// Token and cookie lifetimes.
const (
	// AccessTTL bounds how long a stolen access token works.
	AccessTTL = 15 * time.Minute
	// RefreshTTL bounds how long a session survives without use; each
	// rotation starts a fresh refresh window. Since ADR 004 it is the
	// CEILING on that window, not the value: the org's configured
	// session_lifetime_hours is read at each mint and clamped to it.
	RefreshTTL = 30 * 24 * time.Hour
)

// Cookie and header names.
const (
	// AccessCookie carries the access token on every request.
	AccessCookie = "hamlaneh_session"
	// RefreshCookie carries the refresh token, scoped to the refresh
	// endpoint only so it never rides along on ordinary requests.
	RefreshCookie = "hamlaneh_refresh"
	// CSRFCookie carries the CSRF double-submit value. It is deliberately
	// not HttpOnly: the frontend reads it and echoes it in CSRFHeader.
	CSRFCookie = "hamlaneh_csrf"
	// CSRFHeader is the request header that must match CSRFCookie on
	// mutating API requests.
	CSRFHeader = "X-Hamlaneh-CSRF"

	// RefreshCookiePath scopes RefreshCookie to the refresh endpoint.
	RefreshCookiePath = "/api/v1/auth/refresh"
)

// tokenBytes is the entropy of every opaque token (256 bits).
const tokenBytes = 32

// tokenEncoding is how raw token bytes become cookie values.
var tokenEncoding = base64.RawURLEncoding

// NewToken generates an opaque token: raw is the base64url cookie value,
// hash is the SHA-256 digest stored in the database.
func NewToken() (raw string, hash []byte) {
	raw = tokenEncoding.EncodeToString(randomBytes())
	return raw, HashToken(raw)
}

// HashToken returns the SHA-256 digest of a presented cookie value, the form
// tokens are stored and looked up in. Hashing the encoded string directly
// keeps lookups total: any cookie value hashes, valid base64 or not.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// NewCSRFToken generates the per-login CSRF double-submit value. It is never
// stored server-side; the check is cookie-vs-header equality.
func NewCSRFToken() string {
	return tokenEncoding.EncodeToString(randomBytes())
}

// randomBytes returns tokenBytes bytes from crypto/rand. rand.Read never
// returns an error (its documented contract since Go 1.24); should the
// platform RNG somehow fail anyway, minting any token would be unsafe, so
// the only correct response is to crash.
func randomBytes() []byte {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("session: read random bytes: %v", err))
	}
	return buf
}

// ValidCSRF reports whether the presented header value matches the cookie
// value, in constant time. Empty values never match.
func ValidCSRF(cookieValue, headerValue string) bool {
	if cookieValue == "" || headerValue == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieValue), []byte(headerValue)) == 1
}

// Cookies returns the three session cookies for freshly issued token values.
// SameSite=Strict is the first CSRF line of defense; the custom header check
// is the second.
func Cookies(access, refresh, csrf string) []*http.Cookie {
	return []*http.Cookie{
		accessCookie(access, int(AccessTTL.Seconds())),
		refreshCookie(refresh, int(RefreshTTL.Seconds())),
		csrfCookie(csrf, int(RefreshTTL.Seconds())),
	}
}

// RotatedCookies returns the cookies a successful refresh sets: new access
// and refresh tokens. The CSRF cookie is per-login and stays untouched.
func RotatedCookies(access, refresh string) []*http.Cookie {
	return []*http.Cookie{
		accessCookie(access, int(AccessTTL.Seconds())),
		refreshCookie(refresh, int(RefreshTTL.Seconds())),
	}
}

// ClearCookies returns expired versions of all three cookies, for logout and
// for refresh failures. Attributes must match the set cookies exactly or
// browsers treat them as different cookies and keep the originals.
func ClearCookies() []*http.Cookie {
	return []*http.Cookie{
		accessCookie("", -1),
		refreshCookie("", -1),
		csrfCookie("", -1),
	}
}

// SetCookies adds cookies to a response.
func SetCookies(w http.ResponseWriter, cookies []*http.Cookie) {
	for _, c := range cookies {
		http.SetCookie(w, c)
	}
}

func accessCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     AccessCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

func refreshCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     RefreshCookie,
		Value:    value,
		Path:     RefreshCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

func csrfCookie(value string, maxAge int) *http.Cookie {
	// Deliberately not HttpOnly: the double-submit pattern requires the
	// frontend to read this cookie and echo it in CSRFHeader. The cookie
	// carries no session authority — the HttpOnly access and refresh
	// cookies do.
	return &http.Cookie{ // #nosec G124 -- deliberate: the double-submit CSRF cookie must be readable by JS
		Name:     CSRFCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}
