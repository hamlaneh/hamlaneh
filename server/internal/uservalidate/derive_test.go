package uservalidate_test

import (
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

func TestDeriveUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		attempt int
		want    string
	}{
		{"email keeps the local part", "amir.dezyani@example.com", 0, "amir.dezyani"},
		{"uppercase folds down", "Amir.Dezyani@Example.COM", 0, "amir.dezyani"},
		{"spaces become dashes", "amir dezyani@example.com", 0, "amir-dezyani"},
		{"plus addressing survives", "amir+scim@example.com", 0, "amir-scim"},
		{"non-ascii is replaced", "üser@example.com", 0, "ser"},
		{"leading punctuation is trimmed", "_.-amir@example.com", 0, "amir"},
		{"a bare name is kept", "amir", 0, "amir"},
		{"a short name is padded to the floor", "a@example.com", 0, "a00"},
		{"a long local part is truncated", strings.Repeat("a", 40) + "@x.com", 0, strings.Repeat("a", 32)},
		{"a collision gets a suffix", "amir@example.com", 1, "amir-1"},
		{"a long name makes room for its suffix", strings.Repeat("a", 40), 7, strings.Repeat("a", 30) + "-7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := uservalidate.DeriveUsername(tt.raw, tt.attempt); got != tt.want {
				t.Errorf("DeriveUsername(%q, %d) = %q, want %q", tt.raw, tt.attempt, got, tt.want)
			}
		})
	}
}

// TestDeriveUsernameFallsBackPerInput is the fix for the bug that made this
// a Persian instance's problem specifically: a value with no usable ASCII in
// it used to derive one shared literal, so every non-Latin userName in a
// directory collided, and past the caller's retry bound the account could
// not be provisioned at all.
//
// The property is that DIFFERENT inputs sharing no usable ASCII still derive
// DIFFERENT bases. The digest is not asserted by value — that would pin
// sha256 rather than the property — only its shape and its distinctness.
func TestDeriveUsernameFallsBackPerInput(t *testing.T) {
	t.Parallel()

	// Four scripts and two empties: nothing here survives the ASCII filter.
	raws := []string{
		"امیر@example.com", "سارا@example.com", "زهرا@example.com",
		"李伟@example.com", "Пётр@example.com", "@example.com", "",
	}

	seen := map[string]string{}
	for _, raw := range raws {
		got := uservalidate.DeriveUsername(raw, 0)
		if err := uservalidate.Username(got); err != nil {
			t.Errorf("DeriveUsername(%q, 0) = %q, which Username rejects: %v", raw, got, err)
		}
		if !strings.HasPrefix(got, "user-") || len(got) != len("user-")+8 {
			t.Errorf("DeriveUsername(%q, 0) = %q, want the user-<8 hex> fallback shape", raw, got)
		}
		if first, clash := seen[got]; clash {
			t.Errorf("%q and %q both derive %q; the fallback is not per-input", first, raw, got)
		}
		seen[got] = raw
	}

	// The digest covers the whole value, so two names differing only after
	// the "@" are still two accounts.
	if a, b := uservalidate.DeriveUsername("امیر@a.example", 0),
		uservalidate.DeriveUsername("امیر@b.example", 0); a == b {
		t.Errorf("two directories' copies of one name both derive %q", a)
	}

	// And it is stable: the same value must derive the same account every
	// sync, or a provider's second run would make a second person.
	if a, b := uservalidate.DeriveUsername("امیر@example.com", 0),
		uservalidate.DeriveUsername("امیر@example.com", 0); a != b {
		t.Errorf("the same userName derived %q then %q", a, b)
	}
}

// TestDeriveUsernameAlwaysValidates is the property the whole function
// exists for: whatever a directory sends, what comes out is a username this
// server's own rules accept. Without it the derivation is one odd address
// away from a 500 at provisioning time.
func TestDeriveUsernameAlwaysValidates(t *testing.T) {
	t.Parallel()

	raws := []string{
		"", "@", "@@@", ".", "...", "-", "_", "a", "ab",
		"ALL.CAPS@EXAMPLE.COM", "user name with spaces@x", "用户@example.com",
		"a" + strings.Repeat("é", 100) + "@x", strings.Repeat("x", 400),
		"\x00\x01\x02@x", "amir@", "amir@@example.com",
	}
	for _, raw := range raws {
		for _, attempt := range []int{0, 1, 9, 99, 1000} {
			got := uservalidate.DeriveUsername(raw, attempt)
			if err := uservalidate.Username(got); err != nil {
				t.Errorf("DeriveUsername(%q, %d) = %q, which Username rejects: %v", raw, attempt, got, err)
			}
		}
	}
}

// TestDeriveUsernameSuffixesDiffer pins the collision loop's whole premise:
// two attempts on one value must not produce one name, or a caller retrying
// on ErrUsernameTaken would spin forever on the same conflict.
func TestDeriveUsernameSuffixesDiffer(t *testing.T) {
	t.Parallel()

	seen := map[string]int{}
	for attempt := range 20 {
		got := uservalidate.DeriveUsername("collide@example.com", attempt)
		if first, ok := seen[got]; ok {
			t.Fatalf("attempts %d and %d both derive %q", first, attempt, got)
		}
		seen[got] = attempt
	}
}

// FuzzDeriveUsername is the input-handling guarantee (CLAUDE.md): a
// directory's userName is untrusted text, and no value of it may produce a
// username the account rules reject — or a panic on the way there.
func FuzzDeriveUsername(f *testing.F) {
	for _, seed := range []string{
		"amir@example.com", "", "@", "üser@x", strings.Repeat("a", 100),
		".-_@x", "A@B", "a b c", "امیر@example.com", "李伟@example.com",
	} {
		f.Add(seed, 0)
		f.Add(seed, 3)
	}

	f.Fuzz(func(t *testing.T, raw string, attempt int) {
		got := uservalidate.DeriveUsername(raw, attempt)
		if err := uservalidate.Username(got); err != nil {
			t.Errorf("DeriveUsername(%q, %d) = %q, which Username rejects: %v", raw, attempt, got, err)
		}
		// Distinctness, for the inputs that used to collapse together: any
		// value that lands on the fallback must land on a DIFFERENT one from
		// a fixed non-Latin name it is not equal to. One counterexample is
		// enough to prove the shared-literal bug is back.
		const other = "امیر@example.com"
		if raw != other && got == uservalidate.DeriveUsername(other, attempt) {
			t.Errorf("DeriveUsername(%q, %d) collides with %q", raw, attempt, other)
		}
	})
}
