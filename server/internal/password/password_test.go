package password

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// phcPattern pins the exact PHC shape the security design specifies:
// $argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 key>.
var phcPattern = regexp.MustCompile(
	`^\$argon2id\$v=19\$m=65536,t=3,p=4\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`,
)

func TestHash(t *testing.T) {
	t.Parallel()

	phc := Hash("correct horse battery staple")
	if !phcPattern.MatchString(phc) {
		t.Errorf("Hash produced %q, want it to match %s", phc, phcPattern)
	}

	again := Hash("correct horse battery staple")
	if phc == again {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()

	phc := Hash("correct horse battery staple")

	t.Run("matching password verifies", func(t *testing.T) {
		t.Parallel()
		ok, needsRehash, err := Verify("correct horse battery staple", phc)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !ok {
			t.Error("correct password did not verify")
		}
		if needsRehash {
			t.Error("fresh hash reported as needing rehash")
		}
	})

	t.Run("wrong password fails", func(t *testing.T) {
		t.Parallel()
		ok, _, err := Verify("wrong horse battery staple", phc)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if ok {
			t.Error("wrong password verified")
		}
	})

	t.Run("empty password fails", func(t *testing.T) {
		t.Parallel()
		ok, _, err := Verify("", phc)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if ok {
			t.Error("empty password verified")
		}
	})
}

// TestVerifyOutdatedParams pins rehash-on-login detection: a hash made with
// weaker-than-current parameters must still verify but report needsRehash.
func TestVerifyOutdatedParams(t *testing.T) {
	t.Parallel()

	// A valid argon2id hash of "legacy password" with t=2 (current is t=3),
	// built by this package's own primitives in a helper run and frozen here.
	legacy := hashWithParams(t, "legacy password", 2, memoryKiB, threads)

	ok, needsRehash, err := Verify("legacy password", legacy)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("password did not verify against its legacy-parameter hash")
	}
	if !needsRehash {
		t.Error("legacy-parameter hash not reported as needing rehash")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		phc  string
	}{
		{"empty", ""},
		{"not a phc string", "plainly-not-a-hash"},
		{"bcrypt prefix", "$2a$10$abcdefghijklmnopqrstuv"},
		{"argon2i variant", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"wrong version", "$argon2id$v=18$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"missing fields", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA"},
		{"garbage params", "$argon2id$v=19$m=what,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"zero memory", "$argon2id$v=19$m=0,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"absurd memory", "$argon2id$v=19$m=99999999,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"absurd time", "$argon2id$v=19$m=65536,t=9999,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"invalid salt base64", "$argon2id$v=19$m=65536,t=3,p=4$!!!$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"invalid key base64", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$!!!"},
		{"empty salt", "$argon2id$v=19$m=65536,t=3,p=4$$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Verify("any password", tt.phc)
			if !errors.Is(err, ErrMalformedHash) {
				t.Errorf("Verify(%q) error = %v, want ErrMalformedHash", tt.phc, err)
			}
		})
	}
}

// TestCompareDummy pins the enumeration defense: the dummy comparison always
// reports false and runs against a well-formed current-parameter hash, so it
// burns the same argon2 work as a real verification.
func TestCompareDummy(t *testing.T) {
	t.Parallel()

	for _, pw := range []string{"", "guess", strings.Repeat("a", 1024)} {
		if CompareDummy(pw) {
			t.Errorf("CompareDummy(%q) = true, want false", pw)
		}
	}

	// Structural timing check (no wall clocks): the dummy hash must parse as
	// a current-parameter argon2id hash, so Verify against it performs the
	// exact work a real verification does.
	if !phcPattern.MatchString(dummyHash()) {
		t.Errorf("dummy hash %q does not use the current parameters", dummyHash())
	}
	if _, needsRehash, err := Verify("any", dummyHash()); err != nil || needsRehash {
		t.Errorf("dummy hash verification: err=%v needsRehash=%v, want nil/false", err, needsRehash)
	}
}

// hashWithParams builds a PHC hash with explicit parameters for tests.
func hashWithParams(t *testing.T, password string, time, memory uint32, parallelism uint8) string {
	t.Helper()

	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte(password), salt, time, memory, parallelism, keyLength)
	return formatPHC(time, memory, parallelism, salt, key)
}
