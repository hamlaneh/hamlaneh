package totp_test

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/hotp"

	"github.com/hamlaneh/hamlaneh/server/internal/totp"
)

// rfcSecret is the ASCII secret RFC 6238 Appendix B uses for its SHA-1 test
// vectors: "12345678901234567890".
var rfcSecret = []byte("12345678901234567890")

// TestVerifyMatchesRFC6238Vectors pins the arithmetic against the published
// vectors rather than against our own code generator, so a library swap
// cannot silently change what a code means. The RFC prints eight digits; a
// six-digit code is the same value modulo one million.
func TestVerifyMatchesRFC6238Vectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		unix int64
		code string
	}{
		{name: "T=59", unix: 59, code: "287082"},
		{name: "T=1111111109", unix: 1111111109, code: "081804"},
		{name: "T=1111111111", unix: 1111111111, code: "050471"},
		{name: "T=1234567890", unix: 1234567890, code: "005924"},
		{name: "T=2000000000", unix: 2000000000, code: "279037"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			at := time.Unix(tt.unix, 0).UTC()
			step, ok := totp.Verify(rfcSecret, tt.code, at, nil)
			if !ok {
				t.Fatalf("Verify(%s) rejected the RFC vector", tt.code)
			}
			if want := totp.Step(at); step != want {
				t.Errorf("accepted step %d, want %d", step, want)
			}
		})
	}
}

// TestVerifySkewWindow pins the window: one step either side is accepted,
// two is not.
func TestVerifySkewWindow(t *testing.T) {
	t.Parallel()

	base := time.Unix(1234567890, 0).UTC()
	center := totp.Step(base)

	tests := []struct {
		name       string
		codeStep   int64
		wantAccept bool
	}{
		{name: "one step early", codeStep: center - 1, wantAccept: true},
		{name: "current step", codeStep: center, wantAccept: true},
		{name: "one step late", codeStep: center + 1, wantAccept: true},
		{name: "two steps early", codeStep: center - 2, wantAccept: false},
		{name: "two steps late", codeStep: center + 2, wantAccept: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code := codeForStep(t, rfcSecret, tt.codeStep)
			step, ok := totp.Verify(rfcSecret, code, base, nil)
			if ok != tt.wantAccept {
				t.Fatalf("Verify at step offset %d: accepted=%v, want %v", tt.codeStep-center, ok, tt.wantAccept)
			}
			if ok && step != tt.codeStep {
				t.Errorf("accepted step %d, want %d", step, tt.codeStep)
			}
		})
	}
}

// TestVerifyRejectsReplay pins RFC 6238 section 5.2: once a step has been
// accepted, no code for that step or an earlier one is accepted again, even
// while the thirty-second window is still open.
func TestVerifyRejectsReplay(t *testing.T) {
	t.Parallel()

	at := time.Unix(1234567890, 0).UTC()
	current := totp.Step(at)
	code := codeForStep(t, rfcSecret, current)

	step, ok := totp.Verify(rfcSecret, code, at, nil)
	if !ok {
		t.Fatal("first use of a fresh code was rejected")
	}

	if _, replayed := totp.Verify(rfcSecret, code, at, &step); replayed {
		t.Error("the same code was accepted twice inside its window")
	}

	// The previous step's code is inside the skew window but already behind
	// the used step, so it must not sneak in either.
	previous := codeForStep(t, rfcSecret, current-1)
	if _, replayed := totp.Verify(rfcSecret, previous, at, &step); replayed {
		t.Error("a code older than the last used step was accepted")
	}

	// The next step is still ahead of the used one and stays valid.
	next := codeForStep(t, rfcSecret, current+1)
	if _, accepted := totp.Verify(rfcSecret, next, at, &step); !accepted {
		t.Error("the next step's code was rejected after a replay guard was set")
	}
}

func TestVerifyRejectsMalformedCodes(t *testing.T) {
	t.Parallel()

	at := time.Unix(1234567890, 0).UTC()
	for _, code := range []string{"", "12345", "1234567", "abcdef", "12345 ", "۱۲۳۴۵۶", "000000"} {
		if _, ok := totp.Verify(rfcSecret, code, at, nil); ok {
			t.Errorf("Verify accepted %q", code)
		}
	}
}

func TestIsAuthenticatorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code string
		want bool
	}{
		{code: "000000", want: true},
		{code: "123456", want: true},
		{code: "12345", want: false},
		{code: "1234567", want: false},
		{code: "12345a", want: false},
		{code: "4T7M-9QKX", want: false},
		{code: "", want: false},
	}
	for _, tt := range tests {
		if got := totp.IsAuthenticatorCode(tt.code); got != tt.want {
			t.Errorf("IsAuthenticatorCode(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestNewSecret(t *testing.T) {
	t.Parallel()

	first := totp.NewSecret()
	if len(first) != totp.SecretBytes {
		t.Fatalf("secret is %d bytes, want %d", len(first), totp.SecretBytes)
	}

	seen := map[string]bool{}
	for range 64 {
		key := totp.EncodeSecret(totp.NewSecret())
		if seen[key] {
			t.Fatal("NewSecret repeated a secret")
		}
		seen[key] = true
	}
}

func TestEnroll(t *testing.T) {
	t.Parallel()

	secret := totp.NewSecret()
	enrollment, err := totp.Enroll(secret, "a.jones")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// The manual key is the secret itself, base32 without padding, so
	// someone typing it by hand ends up with the same authenticator.
	if strings.Contains(enrollment.ManualKey, "=") {
		t.Errorf("manual key is padded: %q", enrollment.ManualKey)
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.ManualKey)
	if err != nil {
		t.Fatalf("manual key is not base32: %v", err)
	}
	if string(decoded) != string(secret) {
		t.Error("manual key does not decode back to the secret")
	}

	uri, err := url.Parse(enrollment.OtpauthURI)
	if err != nil {
		t.Fatalf("otpauth URI does not parse: %v", err)
	}
	if uri.Scheme != "otpauth" || uri.Host != "totp" {
		t.Errorf("got %s://%s, want otpauth://totp", uri.Scheme, uri.Host)
	}
	if want := "/" + totp.Issuer + ":a.jones"; uri.Path != want {
		t.Errorf("label is %q, want %q", uri.Path, want)
	}

	q := uri.Query()
	for key, want := range map[string]string{
		"secret":    enrollment.ManualKey,
		"issuer":    totp.Issuer,
		"period":    "30",
		"digits":    "6",
		"algorithm": "SHA1",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("otpauth %s = %q, want %q", key, got, want)
		}
	}

	// A code generated from the enrolment URI's secret verifies against the
	// raw secret: what the app scans and what the server stores agree.
	code := codeForStep(t, secret, totp.Step(time.Now()))
	if _, ok := totp.Verify(secret, code, time.Now(), nil); !ok {
		t.Error("a code for the enrolled secret did not verify")
	}
}

func TestEnrollRejectsEmptyAccountName(t *testing.T) {
	t.Parallel()

	if _, err := totp.Enroll(totp.NewSecret(), ""); err == nil {
		t.Error("Enroll accepted an empty account name")
	}
}

// codeForStep produces the six-digit code a compliant authenticator would
// show for a given time step, so the skew and replay tests can name a step
// rather than a literal code. TestVerifyMatchesRFC6238Vectors is what pins
// this generator's agreement with the published vectors.
func codeForStep(t *testing.T, secret []byte, step int64) string {
	t.Helper()

	if step < 0 {
		t.Fatalf("negative step %d", step)
	}
	code, err := hotp.GenerateCode(totp.EncodeSecret(secret), uint64(step))
	if err != nil {
		t.Fatalf("generate code for step %d: %v", step, err)
	}
	return code
}
