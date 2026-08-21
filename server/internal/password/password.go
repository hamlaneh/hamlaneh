// Package password hashes and verifies user passwords with argon2id.
//
// Hashes are stored as PHC-formatted strings
// ($argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>), so the parameters travel
// with each hash and can be raised later: Verify reports when a stored hash
// uses outdated parameters and callers rehash on the next successful login.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Current argon2id parameters (Phase 1.1 security design). Raising them is
// safe: old hashes keep verifying and Verify reports needsRehash.
const (
	timeCost   = 3
	memoryKiB  = 64 * 1024
	threads    = 4
	keyLength  = 32
	saltLength = 16
)

// Parameter ceilings for hashes read back from the database. They bound the
// memory and CPU a single Verify can consume even if a stored hash is
// corrupted or hostile, while leaving generous room for future upgrades.
const (
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxTimeCost  = 64
	maxThreads   = 64
)

// ErrMalformedHash reports a stored hash that is not a well-formed argon2id
// PHC string. It should never happen for hashes this package wrote.
var ErrMalformedHash = errors.New("password: malformed argon2id hash")

// phcEncoding is the base64 variant PHC strings use: standard alphabet, no
// padding.
var phcEncoding = base64.RawStdEncoding

// Hash derives an argon2id hash of password with the current parameters and
// a fresh random salt, returning it as a PHC-formatted string.
func Hash(password string) string {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		// crypto/rand.Read never returns an error (its documented contract
		// since Go 1.24); a broken platform RNG must not produce a hash.
		panic(fmt.Sprintf("password: read random salt: %v", err))
	}

	key := argon2.IDKey([]byte(password), salt, timeCost, memoryKiB, threads, keyLength)
	return formatPHC(timeCost, memoryKiB, threads, salt, key)
}

// formatPHC renders argon2id parameters, salt, and key as a PHC string.
func formatPHC(time, memory uint32, parallelism uint8, salt, key []byte) string {
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, time, parallelism,
		phcEncoding.EncodeToString(salt),
		phcEncoding.EncodeToString(key),
	)
}

// Verify reports whether password matches the PHC-formatted encodedHash. The
// comparison is constant-time. needsRehash is true when the hash was made
// with parameters other than the current ones — callers should rehash the
// password on the next opportunity (login) to complete a parameter upgrade.
func Verify(password, encodedHash string) (ok, needsRehash bool, err error) {
	params, salt, want, err := parsePHC(encodedHash)
	if err != nil {
		return false, false, err
	}

	// parsePHC bounds the key length, so the conversion cannot overflow.
	got := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, uint32(len(want))) // #nosec G115 -- parsePHC bounds the key length; the conversion cannot overflow
	ok = subtle.ConstantTimeCompare(got, want) == 1

	needsRehash = params.time != timeCost ||
		params.memoryKiB != memoryKiB ||
		params.threads != threads ||
		len(want) != keyLength
	return ok, needsRehash, nil
}

// dummyHash is a hash of an unknowable random password, computed once on
// first use. Login flows verify the presented password against it when the
// account does not exist, so unknown-user and wrong-password attempts do the
// same argon2 work and stay indistinguishable by timing.
var dummyHash = sync.OnceValue(func() string {
	// rand.Text provides ~128 bits of entropy; nobody can log in as a
	// nonexistent user by guessing it.
	return Hash(rand.Text())
})

// CompareDummy burns the same argon2 work as a real verification, comparing
// password against a hash no input can match. Call it on login when no
// account matches the identifier so response timing cannot reveal whether an
// account exists. It reports the comparison result, which is always false.
func CompareDummy(password string) bool {
	ok, _, err := Verify(password, dummyHash())
	return err == nil && ok
}

type phcParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

// parsePHC splits and validates an argon2id PHC string into its parameters,
// salt, and key. Anything unexpected — wrong variant, wrong version, missing
// fields, out-of-bound parameters — is ErrMalformedHash.
func parsePHC(encodedHash string) (phcParams, []byte, []byte, error) {
	var params phcParams

	fields := strings.Split(encodedHash, "$")
	if len(fields) != 6 || fields[0] != "" || fields[1] != "argon2id" {
		return params, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params, nil, nil, ErrMalformedHash
	}

	var memory, time, parallelism uint32
	if _, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &memory, &time, &parallelism); err != nil {
		return params, nil, nil, ErrMalformedHash
	}
	if memory == 0 || memory > maxMemoryKiB ||
		time == 0 || time > maxTimeCost ||
		parallelism == 0 || parallelism > maxThreads {
		return params, nil, nil, ErrMalformedHash
	}

	salt, err := phcEncoding.DecodeString(fields[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return params, nil, nil, ErrMalformedHash
	}
	key, err := phcEncoding.DecodeString(fields[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return params, nil, nil, ErrMalformedHash
	}

	params = phcParams{memoryKiB: memory, time: time, threads: uint8(parallelism)}
	return params, salt, key, nil
}
