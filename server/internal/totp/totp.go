// Package totp implements the two-step verification primitives Hamlaneh
// needs: RFC 6238 time-based one-time passwords, the enrolment material a
// client shows during setup (manual key, otpauth URI, inline-SVG QR), and
// the single-use recovery codes that stand in when the authenticator is
// lost.
//
// The one-time-password arithmetic itself is github.com/pquerna/otp — this
// package computes no HMAC of its own (CLAUDE.md principle 3: assemble,
// don't reinvent; never hand-roll cryptography). What it owns is the policy
// around that arithmetic: the secret size, the skew window, and the rule
// that an accepted time step is never accepted a second time.
//
// Nothing here logs, and no exported function puts a secret, a code, or a
// recovery code into an error message.
package totp

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	pqtotp "github.com/pquerna/otp/totp" // aliased: this package owns the name
)

// Algorithm parameters. They are the values every authenticator app defaults
// to, so a scanned account works without the user changing anything.
const (
	// SecretBytes is the secret size, 160 bits: the RFC 4226 §4 R6
	// recommendation, and the exact length user_totp.secret's CHECK
	// constraint pins.
	SecretBytes = 20
	// Period is the RFC 6238 time step.
	Period = 30 * time.Second
	// Digits is the code length.
	Digits = 6
	// SkewSteps is how many steps either side of the current one are
	// accepted. One step absorbs ordinary clock drift and the seconds a
	// human spends typing; widening it multiplies the codes an attacker's
	// single guess can hit.
	SkewSteps = 1
	// Issuer labels the account inside the authenticator app. It is a brand
	// name, not a user-facing string, so it is never translated.
	Issuer = "Hamlaneh"
)

// Lifetimes and caps. An uncapped verifier is a brute-force oracle, so both
// the pending setup and the login challenge die at their cap.
const (
	// SetupTTL bounds how long a pending setup can be completed.
	SetupTTL = time.Hour
	// ChallengeTTL bounds the half-authenticated state between the password
	// step and the code step.
	ChallengeTTL = 5 * time.Minute
	// MaxSetupAttempts is how many wrong codes revoke a pending setup.
	MaxSetupAttempts = 5
	// MaxChallengeAttempts is how many wrong codes revoke a login challenge.
	MaxChallengeAttempts = 5
)

// secretEncoding is how a secret is rendered as the manual key and handed to
// the library: base32, upper case, no padding — what every authenticator app
// expects to be typed, and what the otpauth URI carries.
var secretEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh 160-bit secret from crypto/rand.
//
// rand.Read never returns an error (its documented contract since Go 1.24);
// if the platform RNG somehow failed, a guessable secret would be worse than
// no second factor at all, so the only correct response is to crash.
func NewSecret() []byte {
	secret := make([]byte, SecretBytes)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("totp: read random secret: %v", err))
	}
	return secret
}

// Step returns the RFC 6238 time step covering t.
func Step(t time.Time) int64 {
	return t.Unix() / int64(Period/time.Second)
}

// IsAuthenticatorCode reports whether code is exactly six ASCII digits — the
// shape that is checked against the secret rather than against the recovery
// codes.
func IsAuthenticatorCode(code string) bool {
	if len(code) != Digits {
		return false
	}
	for i := range len(code) {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

// Verify checks code against secret at time now, accepting the current step
// and SkewSteps either side of it, and returns the step it accepted.
//
// lastUsedStep is the highest step this secret has already authenticated,
// nil when it has authenticated none. Steps at or below it are skipped, so
// an accepted code is never accepted twice even while its thirty-second
// window is still open (RFC 6238 §5.2), and an older code inside the window
// cannot be replayed after a newer one.
func Verify(secret []byte, code string, now time.Time, lastUsedStep *int64) (step int64, ok bool) {
	if !IsAuthenticatorCode(code) {
		return 0, false
	}

	encoded := EncodeSecret(secret)
	current := Step(now)
	for candidate := current - SkewSteps; candidate <= current+SkewSteps; candidate++ {
		if candidate < 0 {
			continue
		}
		if lastUsedStep != nil && candidate <= *lastUsedStep {
			continue
		}
		// The comparison inside ValidateCustom is constant-time.
		matched, err := hotp.ValidateCustom(code, uint64(candidate), encoded, hotp.ValidateOpts{ // #nosec G115 -- candidate is non-negative here
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			// The only failures are a non-base32 secret or a wrong-length
			// code, both excluded above. Anything else is not a match.
			return 0, false
		}
		if matched {
			return candidate, true
		}
	}
	return 0, false
}

// EncodeSecret renders a secret as the manual key: base32, no padding.
func EncodeSecret(secret []byte) string {
	return secretEncoding.EncodeToString(secret)
}

// Enrollment is everything setup step 1 hands the client. Every field is
// derived from the secret on the spot; none of it is stored.
type Enrollment struct {
	// ManualKey is the secret in base32 without padding, for someone whose
	// camera is not an option.
	ManualKey string
	// OtpauthURI is what the QR encodes.
	OtpauthURI string
	// QRSVG is the inline SVG rendering of OtpauthURI.
	QRSVG string
}

// Enroll builds the enrolment material for a secret and the account name the
// authenticator app should show. accountName must not be empty.
func Enroll(secret []byte, accountName string) (Enrollment, error) {
	key, err := pqtotp.Generate(pqtotp.GenerateOpts{
		Issuer:      Issuer,
		AccountName: accountName,
		Period:      uint(Period / time.Second),
		Secret:      secret,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return Enrollment{}, fmt.Errorf("totp: build enrolment key: %w", err)
	}

	uri := key.URL()
	svg, err := renderQR(uri)
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{ManualKey: key.Secret(), OtpauthURI: uri, QRSVG: svg}, nil
}
