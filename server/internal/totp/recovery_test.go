package totp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/totp"
)

// contractShape is the RecoveryCodes pattern from docs/api/openapi.yaml.
var contractShape = regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`)

func TestNewRecoveryCodes(t *testing.T) {
	t.Parallel()

	codes := totp.NewRecoveryCodes(totp.RecoveryCodeCount)
	if len(codes) != totp.RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), totp.RecoveryCodeCount)
	}

	seen := map[string]bool{}
	for _, code := range codes {
		if !contractShape.MatchString(code) {
			t.Errorf("code %q does not match the contract pattern", code)
		}
		if seen[code] {
			t.Errorf("code %q was issued twice in one set", code)
		}
		seen[code] = true

		// The alphabet has no look-alikes to mistype.
		for _, forbidden := range []string{"I", "L", "O", "U"} {
			if strings.Contains(code, forbidden) {
				t.Errorf("code %q contains the ambiguous symbol %s", code, forbidden)
			}
		}
	}
}

// TestNewRecoveryCodesAreUnpredictable is a smoke check that the generator
// draws from the whole alphabet rather than a fixed or degenerate source.
func TestNewRecoveryCodesAreUnpredictable(t *testing.T) {
	t.Parallel()

	symbols := map[rune]bool{}
	all := map[string]bool{}
	for range 50 {
		for _, code := range totp.NewRecoveryCodes(totp.RecoveryCodeCount) {
			if all[code] {
				t.Fatalf("code %q repeated across sets", code)
			}
			all[code] = true
			for _, r := range strings.ReplaceAll(code, "-", "") {
				symbols[r] = true
			}
		}
	}
	if len(symbols) != 32 {
		t.Errorf("generator used %d distinct symbols, want the full alphabet of 32", len(symbols))
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "canonical", input: "4T7M-9QKX", want: "4T7M-9QKX", ok: true},
		{name: "lower case", input: "4t7m-9qkx", want: "4T7M-9QKX", ok: true},
		{name: "no hyphen", input: "4T7M9QKX", want: "4T7M-9QKX", ok: true},
		{name: "spaced", input: " 4T7M 9QKX ", want: "4T7M-9QKX", ok: true},
		{name: "hyphen in an odd place", input: "4-T7M9Q-KX", want: "4T7M-9QKX", ok: true},
		{name: "letter oh reads as zero", input: "4T7M-9QKO", want: "4T7M-9QK0", ok: true},
		{name: "letters i and l read as one", input: "IT7M-9QKL", want: "1T7M-9QK1", ok: true},
		{name: "too short", input: "4T7M-9QK", ok: false},
		{name: "too long", input: "4T7M-9QKX2", ok: false},
		{name: "six digit code", input: "123456", ok: false},
		{name: "symbol outside the alphabet", input: "4T7M-9QK!", ok: false},
		{name: "letter u is not in the alphabet", input: "4T7M-9QKU", ok: false},
		{name: "non ascii", input: "4T7M-9QKمم", ok: false},
		{name: "empty", input: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := totp.NormalizeRecoveryCode(tt.input)
			if ok != tt.ok {
				t.Fatalf("NormalizeRecoveryCode(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("NormalizeRecoveryCode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestNormalizeRecoveryCodeRoundTrips pins that every generated code
// normalizes back to itself, so what was hashed is what a paste matches.
func TestNormalizeRecoveryCodeRoundTrips(t *testing.T) {
	t.Parallel()

	for _, code := range totp.NewRecoveryCodes(totp.RecoveryCodeCount) {
		got, ok := totp.NormalizeRecoveryCode(code)
		if !ok || got != code {
			t.Errorf("NormalizeRecoveryCode(%q) = %q, %v; want the code unchanged", code, got, ok)
		}
		lowered, ok := totp.NormalizeRecoveryCode(strings.ToLower(code))
		if !ok || lowered != code {
			t.Errorf("lower-cased %q normalized to %q, %v", code, lowered, ok)
		}
	}
}
