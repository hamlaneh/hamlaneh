package uservalidate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// fallbackPrefix and fallbackDigestLen build the base for a directory value
// with no usable ASCII in it — a Persian, Arabic, Chinese or Cyrillic local
// part, or an empty one.
//
// The digest is what makes the base distinct PER INPUT, and that is the
// whole point of it rather than a flourish. A single shared literal meant
// every non-Latin userName in a directory derived the same base, so they all
// collided, and past the caller's retry bound the account could not be
// provisioned at all. This product's primary non-English audience is exactly
// the one that would have hit it.
//
// It is not a transliteration, deliberately: mapping scripts onto ASCII
// invents a correspondence nobody chose and is wrong for at least one of
// them. An opaque handle is honest about having no reading of the name.
//
// Eight hex characters is 32 bits — far more than enough to keep one
// organisation's directory apart, and a genuine clash is handled by the
// caller's suffix loop like any other.
const (
	fallbackPrefix    = "user-"
	fallbackDigestLen = 8
)

// DeriveUsername derives a local account username from a value an external
// identity provider chose — usually an email address, which cannot satisfy
// the account rules above (lowercase, 3 to 32, starting alphanumeric).
//
// The account rules stay as they are and the provider's value is stored
// verbatim elsewhere; relaxing them to accept directory names would push the
// change through every screen that displays one (docs/api/scim.md §4).
//
// attempt is the collision counter: 0 derives the plain name, and each
// higher value appends its own suffix, truncating the base to keep the
// result inside the length bound. Callers retry with the next attempt when
// storage reports the name taken, which is why the suffix has to be part of
// this function rather than pasted on afterwards — pasting it on is what
// produces a 33-character username.
//
// The result always satisfies Username; TestDeriveUsernameAlwaysValidates
// and FuzzDeriveUsername pin that for every input.
//
// Both SCIM provisioning and just-in-time SSO provisioning need exactly this
// derivation, which is why it lives beside the rules it has to satisfy
// rather than inside either caller.
func DeriveUsername(raw string, attempt int) string {
	local, _, _ := strings.Cut(raw, "@")

	var b strings.Builder
	for _, r := range strings.ToLower(local) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			// WriteRune, not WriteByte(byte(r)): both are one byte for the
			// runes these guards admit, but the conversion is only correct
			// BECAUSE of the guards. Widening a guard later would make
			// WriteByte truncate silently, where WriteRune stays right.
			b.WriteRune(r)
		case r == '_' || r == '.' || r == '-':
			// Legal, but not as the first character; leading ones are
			// trimmed below rather than special-cased here.
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	// Every rune above wrote exactly one ASCII byte, so from here length in
	// bytes, runes and characters are the same thing and truncation is safe.
	base := strings.TrimLeft(b.String(), "_.-")
	if base == "" {
		base = fallbackBase(raw)
	}

	// A suffix is at most "-" plus the digits of an int, so 20 characters —
	// which leaves the truncation below a non-negative index for every
	// attempt an int can hold, and leaves the base non-empty.
	suffix := ""
	if attempt > 0 {
		suffix = "-" + strconv.Itoa(attempt)
	}
	if len(base)+len(suffix) > MaxUsernameLen {
		base = base[:MaxUsernameLen-len(suffix)]
	}

	// Truncation can leave the base empty (a long suffix) or shorter than
	// the floor; pad rather than fail, because a caller with a name to
	// provision has no better answer to fall back to.
	name := base + suffix
	for len(name) < MinUsernameLen {
		name += "0"
	}
	return name
}

// fallbackBase is the base for a value whose characters left nothing usable.
// It digests the WHOLE raw value, not the local part, so two directory names
// that differ only after the "@" still derive different accounts.
//
// The result is always a valid username start: the prefix begins with a
// letter, and hex is lowercase alphanumeric.
func fallbackBase(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fallbackPrefix + hex.EncodeToString(sum[:])[:fallbackDigestLen]
}
