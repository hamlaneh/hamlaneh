package totp

import (
	"crypto/rand"
	"fmt"
	"strings"
	"unicode"
)

// Recovery code shape: the design's XXXX-XXXX, drawn as ten numbered rows on
// settings-2fa-recovery-codes.
const (
	// RecoveryCodeCount is how many codes a set holds.
	RecoveryCodeCount = 10
	// recoveryGroupLen is the length of each hyphen-separated group.
	recoveryGroupLen = 4
	// recoverySymbols is how many symbols a code carries, hyphen excluded.
	recoverySymbols = 2 * recoveryGroupLen
)

// recoveryAlphabet is Crockford's base32 alphabet: the digits plus the
// letters that cannot be mistaken for one (no I, L, O or U). Thirty-two
// symbols divide 256 exactly, so a byte from crypto/rand maps to a symbol
// with no modulo bias, and eight symbols carry 40 bits — which is exactly
// why the stored form is argon2id and not a bare digest.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewRecoveryCodes returns n fresh codes in the canonical XXXX-XXXX form.
// They are shown to the user exactly once; only their argon2id hashes are
// kept.
func NewRecoveryCodes(n int) []string {
	codes := make([]string, 0, n)
	for range n {
		codes = append(codes, newRecoveryCode())
	}
	return codes
}

// newRecoveryCode draws one code from crypto/rand.
//
// rand.Read never returns an error (its documented contract since Go 1.24);
// a guessable sign-in credential is worse than none, so a failing platform
// RNG must crash rather than produce one.
func newRecoveryCode() string {
	raw := make([]byte, recoverySymbols)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("totp: read random recovery code: %v", err))
	}

	code := make([]byte, 0, recoverySymbols+1)
	for i, b := range raw {
		if i == recoveryGroupLen {
			code = append(code, '-')
		}
		code = append(code, recoveryAlphabet[int(b)%len(recoveryAlphabet)])
	}
	return string(code)
}

// NormalizeRecoveryCode folds a code the user typed into the canonical form
// that was hashed at generation: hyphens and spaces dropped, lower case
// raised, and the look-alikes Crockford leaves out mapped onto the digit
// they resemble (I and L to 1, O to 0). It reports false for anything that
// is not the shape of a recovery code.
func NormalizeRecoveryCode(raw string) (string, bool) {
	symbols := make([]rune, 0, recoverySymbols)
	for _, r := range raw {
		if r == '-' || r == ' ' {
			continue
		}
		// Anything outside the alphabet is rejected here, non-ASCII included:
		// upper-casing never lands a foreign rune on one of these symbols.
		symbol := disambiguate(unicode.ToUpper(r))
		if !strings.ContainsRune(recoveryAlphabet, symbol) {
			return "", false
		}
		if len(symbols) == recoverySymbols {
			return "", false
		}
		symbols = append(symbols, symbol)
	}
	if len(symbols) != recoverySymbols {
		return "", false
	}
	return string(symbols[:recoveryGroupLen]) + "-" + string(symbols[recoveryGroupLen:]), true
}

// disambiguate maps the three letters Crockford's alphabet omits onto the
// digit a reader would confuse them with.
func disambiguate(symbol rune) rune {
	switch symbol {
	case 'I', 'L':
		return '1'
	case 'O':
		return '0'
	default:
		return symbol
	}
}
