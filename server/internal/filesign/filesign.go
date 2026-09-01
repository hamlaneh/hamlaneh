// Package filesign mints and checks the signed, expiring URLs that address
// uploaded files.
//
// The files origin is cookie-less by design (docs/adr/003-file-serving-and-
// egress.md): a request for an attachment carries no session and therefore
// no ambient authority at all. Authorization happens where a URL is MINTED —
// every serialization of an Attachment for a reader entitled to the channel
// signs fresh URLs — and this package is the only thing that decides whether
// a presented URL is one this instance really minted and is still live.
//
// The signature is the credential, so the properties below are the whole
// security of file reads:
//
//   - the id and the variant (blobstore.Variant — the same vocabulary the
//     bytes are stored under) are inside the MAC, so a thumbnail link cannot
//     be replayed as a link to the full original, nor one file's link as
//     another's
//   - the expiry is inside the MAC, so it cannot be pushed out by editing
//     the query
//   - verification is constant-time, and every failure is the same failure:
//     the caller answers 404 and never says which check refused
//
// The accepted cost, stated in the ADR: anyone handed a fresh URL can fetch
// those bytes until it expires, and revoking channel membership does not
// recall URLs already minted. That is the same property a pre-signed S3 URL
// has, and it is why TTL is an hour rather than a day.
package filesign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
)

// TTL is how long a minted URL stays valid. An hour is long enough that a
// message list stays clickable while the reader has it open, and short
// enough that a link pasted elsewhere stops working the same afternoon.
const TTL = time.Hour

// EnvKey is the environment variable carrying the signing key. deploy/
// install.sh generates it; the server refuses to start without it, because
// a server that invented its own key on each boot would invalidate every
// URL it had ever minted on every restart, and one with a built-in default
// would have no security at all.
const EnvKey = "HAMLANEH_FILE_URL_KEY"

// minKeyLen is the shortest key accepted, in bytes. HMAC-SHA256's security
// stops improving past the 32 bytes of its output size, and shorter is how a
// "temporary" placeholder gets in. (SHA-256's block is 64 bytes; that is the
// size HMAC pads the key to, not a security level.)
const minKeyLen = 32

// The signed query parameters.
const (
	paramExpires   = "exp"
	paramSignature = "sig"
)

// A Signer mints and verifies file URLs with one instance-wide key. It is
// safe for concurrent use: the key is never mutated after New.
type Signer struct {
	key []byte
}

// New returns a Signer over key, which must be at least minKeyLen bytes.
// The slice is retained, not copied — callers must not mutate it.
func New(key []byte) (*Signer, error) {
	if len(key) < minKeyLen {
		return nil, fmt.Errorf("file URL signing key is %d bytes, need at least %d", len(key), minKeyLen)
	}
	return &Signer{key: key}, nil
}

// FromEnv builds the Signer from EnvKey. A missing or too-short key is a
// startup error naming the variable and how to produce one: this is the
// credential every file read is checked against, so guessing at it is not
// among the options.
func FromEnv() (*Signer, error) {
	key := os.Getenv(EnvKey)
	if key == "" {
		return nil, fmt.Errorf("%s is not set: generate one with `openssl rand -base64 32` and add it to deploy/.env (deploy/install.sh does this on a fresh install)", EnvKey)
	}
	s, err := New([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvKey, err)
	}
	return s, nil
}

// FileURL returns the path and query that serve variant v of id, valid for
// TTL from now. The result is origin-relative: the caller prefixes the files
// origin it publishes (files.<domain>, or the app origin on an install that
// has none). It is the method httpserver.URLSigner is satisfied by.
func (s *Signer) FileURL(id uuid.UUID, v blobstore.Variant) string {
	return s.FileURLAt(id, v, time.Now())
}

// FileURLAt is FileURL with the clock supplied. Production calls FileURL;
// tests use this to mint a URL that has already expired, which is the only
// way to exercise that refusal without sleeping for an hour.
func (s *Signer) FileURLAt(id uuid.UUID, v blobstore.Variant, now time.Time) string {
	expires := now.Add(TTL).Unix()
	q := url.Values{
		paramExpires:   {strconv.FormatInt(expires, 10)},
		paramSignature: {base64.RawURLEncoding.EncodeToString(s.mac(id, v, expires))},
	}
	return Path(id, v) + "?" + q.Encode()
}

// Valid reports whether query carries a signature this instance minted for
// exactly (id, v) and has not yet expired.
//
// Every rejection returns the same false — a missing parameter, an
// unparseable expiry, a tampered signature, a swapped id or variant, a key
// from another instance, a passed expiry. The caller answers 404 for all of
// them, so the difference is never observable, and nothing here may grow a
// reason code that makes it so.
func (s *Signer) Valid(id uuid.UUID, v blobstore.Variant, query url.Values) bool {
	expires, err := strconv.ParseInt(query.Get(paramExpires), 10, 64)
	if err != nil {
		return false
	}
	presented, err := base64.RawURLEncoding.DecodeString(query.Get(paramSignature))
	if err != nil {
		return false
	}
	// hmac.Equal is the constant-time comparison; a byte-by-byte one here
	// would leak the correct prefix and make forgery a search.
	if !hmac.Equal(presented, s.mac(id, v, expires)) {
		return false
	}
	return !time.Now().After(time.Unix(expires, 0))
}

// Path is the request path serving variant v of id, without the signature.
// It is what FileURLAt mints against.
func Path(id uuid.UUID, v blobstore.Variant) string {
	return pathOf(id.String(), v)
}

// RoutePattern is the same path with the id left as the router's wildcard:
// the pattern files_origin.go registers on.
//
// It exists so the minted URL and the route that serves it are ONE statement
// rather than two that have to be kept equal by whoever edits either. Before
// it, "/files/" was written out at both ends, and moving one of them would
// have 404'd every URL this package had already signed while the whole test
// suite stayed green — the routes were reached by literal in the tests too.
func RoutePattern(v blobstore.Variant) string {
	return pathOf("{id}", v)
}

// pathOf is the one place the files origin's path space is spelled.
func pathOf(id string, v blobstore.Variant) string {
	if v == blobstore.Thumbnail {
		return "/files/" + id + "/thumb"
	}
	return "/files/" + id
}

// mac authenticates the three fields joined by newlines. Neither a UUID's
// text form nor a variant name can contain one, so no two distinct triples
// can ever produce the same signed input — which is what stops a crafted id
// from borrowing another file's signature.
func (s *Signer) mac(id uuid.UUID, v blobstore.Variant, expires int64) []byte {
	m := hmac.New(sha256.New, s.key)
	// hash.Hash documents that Write never returns an error. The blanks are
	// that contract, not a swallowed failure.
	_, _ = m.Write([]byte(id.String()))
	_, _ = m.Write([]byte("\n" + string(v) + "\n"))
	_, _ = m.Write([]byte(strconv.FormatInt(expires, 10)))
	return m.Sum(nil)
}
