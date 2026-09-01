package filesign_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
	"github.com/hamlaneh/hamlaneh/server/internal/filesign"
)

// testKey is 32 bytes of fixed, non-secret material. It never leaves this
// file and gates nothing.
const testKey = "0123456789abcdef0123456789abcdef"

func newSigner(t *testing.T, key string) *filesign.Signer {
	t.Helper()
	s, err := filesign.New([]byte(key))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// queryOf mints a URL and returns its path and parsed query.
func queryOf(t *testing.T, s *filesign.Signer, id uuid.UUID, v blobstore.Variant, now time.Time) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(s.FileURLAt(id, v, now))
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	return u.Path, u.Query()
}

func TestFileURLRoundTrips(t *testing.T) {
	t.Parallel()

	s := newSigner(t, testKey)
	id := uuid.New()

	for _, v := range []blobstore.Variant{blobstore.Original, blobstore.Thumbnail} {
		path, q := queryOf(t, s, id, v, time.Now())

		if want := filesign.Path(id, v); path != want {
			t.Errorf("%s path = %q, want %q", v, path, want)
		}
		if !s.Valid(id, v, q) {
			t.Errorf("%s: a freshly minted URL did not verify", v)
		}
	}

	// The two variants must be distinguishable paths, or the route that
	// decides which bytes to open could not tell them apart either.
	if filesign.Path(id, blobstore.Original) == filesign.Path(id, blobstore.Thumbnail) {
		t.Error("original and thumb share a path")
	}
}

// TestValidRejects is the whole negative surface: every way a URL can fail
// to be one this instance minted for this exact resource, right now.
func TestValidRejects(t *testing.T) {
	t.Parallel()

	s := newSigner(t, testKey)
	other := newSigner(t, strings.Repeat("z", 32))
	id := uuid.New()
	otherID := uuid.New()
	now := time.Now()

	_, fresh := queryOf(t, s, id, blobstore.Original, now)

	tests := []struct {
		name    string
		id      uuid.UUID
		variant blobstore.Variant
		query   url.Values
	}{
		{
			// Tampered: one flipped character in the signature.
			name:    "tampered signature",
			id:      id,
			variant: blobstore.Original,
			query:   withParam(fresh, "sig", flipFirst(fresh.Get("sig"))),
		},
		{
			// Expired: minted two TTLs ago, so its own expiry has passed.
			name:    "expired URL",
			id:      id,
			variant: blobstore.Original,
			query:   mustQuery(t, s, id, blobstore.Original, now.Add(-2*filesign.TTL)),
		},
		{
			// Variant swap: a thumbnail's signature presented for the full
			// original. The variant is inside the MAC precisely for this.
			name:    "variant swapped to original",
			id:      id,
			variant: blobstore.Original,
			query:   mustQuery(t, s, id, blobstore.Thumbnail, now),
		},
		{
			name:    "variant swapped to thumb",
			id:      id,
			variant: blobstore.Thumbnail,
			query:   fresh,
		},
		{
			// Id swap: another attachment's live signature.
			name:    "id swapped",
			id:      otherID,
			variant: blobstore.Original,
			query:   fresh,
		},
		{
			// Pushing the expiry out by editing the query it is carried in.
			name:    "expiry extended",
			id:      id,
			variant: blobstore.Original,
			query:   withParam(fresh, "exp", strconv.FormatInt(now.Add(100*filesign.TTL).Unix(), 10)),
		},
		{
			// Signed by a different instance's key.
			name:    "foreign key",
			id:      id,
			variant: blobstore.Original,
			query:   mustQuery(t, other, id, blobstore.Original, now),
		},
		{name: "no query at all", id: id, variant: blobstore.Original, query: url.Values{}},
		{
			name:    "missing signature",
			id:      id,
			variant: blobstore.Original,
			query:   withParam(fresh, "sig", ""),
		},
		{
			name:    "unparseable expiry",
			id:      id,
			variant: blobstore.Original,
			query:   withParam(fresh, "exp", "soon"),
		},
		{
			name:    "signature is not base64",
			id:      id,
			variant: blobstore.Original,
			query:   withParam(fresh, "sig", "!!!!"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if s.Valid(tt.id, tt.variant, tt.query) {
				t.Error("Valid accepted a URL it must refuse")
			}
		})
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 31} {
		if _, err := filesign.New([]byte(strings.Repeat("k", n))); err == nil {
			t.Errorf("New accepted a %d-byte key", n)
		}
	}
	if _, err := filesign.New([]byte(testKey)); err != nil {
		t.Errorf("New rejected a 32-byte key: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	// Not parallel: t.Setenv mutates process state.
	t.Setenv(filesign.EnvKey, "")
	_, err := filesign.FromEnv()
	if err == nil {
		t.Fatal("FromEnv succeeded with no key set")
	}
	if !strings.Contains(err.Error(), filesign.EnvKey) {
		t.Errorf("error %q does not name %s, so nobody can act on it", err, filesign.EnvKey)
	}

	t.Setenv(filesign.EnvKey, "too-short")
	if _, err = filesign.FromEnv(); err == nil {
		t.Error("FromEnv accepted a short key")
	}

	t.Setenv(filesign.EnvKey, testKey)
	s, err := filesign.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	id := uuid.New()
	_, q := queryOf(t, s, id, blobstore.Original, time.Now())
	if !s.Valid(id, blobstore.Original, q) {
		t.Error("a signer built from the environment does not verify its own URL")
	}
}

func mustQuery(t *testing.T, s *filesign.Signer, id uuid.UUID, v blobstore.Variant, now time.Time) url.Values {
	t.Helper()
	_, q := queryOf(t, s, id, v, now)
	return q
}

// withParam copies q with one parameter replaced (removed, if empty).
func withParam(q url.Values, key, value string) url.Values {
	out := url.Values{}
	for k, vs := range q {
		out[k] = append([]string(nil), vs...)
	}
	if value == "" {
		out.Del(key)
		return out
	}
	out.Set(key, value)
	return out
}

// flipFirst changes one character of s, leaving the length and alphabet
// intact so the corruption is in the signature and not in its encoding.
func flipFirst(s string) string {
	if s == "" {
		return "a"
	}
	first := "a"
	if s[0] == 'a' {
		first = "b"
	}
	return first + s[1:]
}
