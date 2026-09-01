// Package audit is Hamlaneh's tamper-EVIDENT log of administrative actions.
//
// Every entry carries an HMAC-SHA256 over its own fields and over the hash
// of the entry before it, keyed by a server key held outside the database
// (HAMLANEH_AUDIT_KEY). Editing a row therefore stops it matching its own
// recorded hash, and removing one stops the row after it naming its
// predecessor — either way verification fails, and it names the entry the
// break starts at.
//
// Tamper-evident is not tamper-proof, and the difference is the whole
// honest claim. Anybody who can write to the database can rewrite the
// chain: delete the rows, recompute every hash after them, put the log back
// together. What they cannot do is rewrite it INVISIBLY without also
// holding the key, because every rewritten entry needs a fresh HMAC and the
// key is not in the database to be stolen along with the rows. Nothing here
// prevents a tamper. It makes one show.
//
// The chain is verified over what a reader is given, so a page's answer is
// about that page: a row removed from before the oldest entry returned is
// outside what those entries can say anything about.
package audit

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// EnvKey is the environment variable carrying the chain's key. deploy/
// install.sh generates it; the server refuses to start without it, because
// a key invented on each boot would fail verification of every entry
// recorded before the last restart, and a built-in default would let
// anybody who knows it rewrite the chain and re-seal it — which is the one
// thing the key exists to prevent.
const EnvKey = "HAMLANEH_AUDIT_KEY"

// minKeyLen is the shortest key accepted, in bytes — the same floor
// internal/filesign takes, for the same reason: HMAC-SHA256 stops improving
// past it, and shorter is how a "temporary" placeholder gets in.
const minKeyLen = 32

// appendTimeout bounds one append. The action being recorded has already
// happened by then, so this is the ceiling on how long a stuck database can
// hold the recording goroutine, not on anything the caller waits for.
const appendTimeout = 5 * time.Second

// A Chain computes and checks entry hashes with one instance-wide key. It
// is safe for concurrent use: the key is never mutated after New.
type Chain struct {
	key []byte
}

// New returns a Chain over key, which must be at least minKeyLen bytes. The
// slice is retained, not copied — callers must not mutate it.
func New(key []byte) (*Chain, error) {
	if len(key) < minKeyLen {
		return nil, fmt.Errorf("audit chain key is %d bytes, need at least %d", len(key), minKeyLen)
	}
	return &Chain{key: key}, nil
}

// FromEnv builds the Chain from EnvKey. A missing or too-short key is a
// startup error naming the variable and how to produce one: without it the
// log would be a table of rows nobody can check, which is worse than no log
// at all because it still looks like evidence.
func FromEnv() (*Chain, error) {
	key := os.Getenv(EnvKey)
	if key == "" {
		return nil, fmt.Errorf("%s is not set: generate one with `openssl rand -base64 32` and add it to deploy/.env (deploy/install.sh does this on a fresh install)", EnvKey)
	}
	c, err := New([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvKey, err)
	}
	return c, nil
}

// Seal returns the entry hash of e: the MAC over e.PrevHash and every field
// of e that the log records. It is both halves of the promise — the value
// stored on append, and the value recomputed on verification — so the two
// can never drift apart.
//
// Seq is deliberately not in the MAC: it is assigned by the database as the
// entry is written, and reordering rows by editing it is caught anyway,
// because prev_hash names a predecessor by content rather than by position.
// Actor is not in it either: it is the reader's copy of a name that can
// change, and a rename must not read as a tamper.
func (c *Chain) Seal(e storage.AuditEntry) []byte {
	m := hmac.New(sha256.New, c.key)
	// hash.Hash documents that Write never returns an error; the blanks
	// throughout this file are that contract, not a swallowed failure.
	_, _ = m.Write(e.PrevHash)
	field(m, true, []byte(e.ID.String()))
	field(m, true, []byte(e.Action))
	uuidField(m, e.ActorID)
	uuidField(m, e.TargetID)
	if e.TargetLabel != nil {
		field(m, true, []byte(*e.TargetLabel))
	} else {
		field(m, false, nil)
	}
	field(m, e.Detail != nil, canonicalJSON(e.Detail))
	if e.IP != nil {
		// Unmap so an IPv4 address hashes the same whether it arrived as
		// 1.2.3.4 or ::ffff:1.2.3.4 — the round trip through the inet
		// column may return either form, and neither is a tamper.
		field(m, true, []byte(e.IP.Unmap().String()))
	} else {
		field(m, false, nil)
	}
	// Microseconds, because that is the resolution the timestamptz column
	// keeps: hashing anything finer would MAC a value the database cannot
	// give back.
	field(m, true, []byte(strconv.FormatInt(e.OccurredAt.UnixMicro(), 10)))
	return m.Sum(nil)
}

// Verify reports the first entry that no longer matches the hash recorded
// for it, oldest first, or nil when every one of them does.
//
// It says nothing about rows BETWEEN the entries it is given, and cannot:
// a filtered page left rows out on purpose, so a gap there is the filter
// doing its job. VerifyRange is the check for a contiguous run.
func (c *Chain) Verify(entries []storage.AuditEntry) error {
	return c.verifyEach(bySeq(entries))
}

// VerifyRange is Verify over a CONTIGUOUS run of the chain — every entry
// between the oldest and the newest given, nothing filtered out — plus the
// check Verify cannot make: that each entry names the one before it. A
// deleted row breaks exactly here, at the entry that followed it.
func (c *Chain) VerifyRange(entries []storage.AuditEntry) error {
	ordered := bySeq(entries)
	if err := c.verifyEach(ordered); err != nil {
		return err
	}
	for i := 1; i < len(ordered); i++ {
		if !bytes.Equal(ordered[i].PrevHash, ordered[i-1].EntryHash) {
			return &BreakError{
				Seq: ordered[i].Seq,
				ID:  ordered[i].ID,
				Reason: fmt.Sprintf("does not follow entry seq %d: a row between them was removed or replaced",
					ordered[i-1].Seq),
			}
		}
	}
	return nil
}

func (c *Chain) verifyEach(ordered []storage.AuditEntry) error {
	for _, e := range ordered {
		if !hmac.Equal(e.EntryHash, c.Seal(e)) {
			return &BreakError{Seq: e.Seq, ID: e.ID, Reason: "does not match its recorded hash"}
		}
	}
	return nil
}

// BreakError says where verification failed. It is a typed error because
// "the log is broken" is not the useful part — which entry, is.
type BreakError struct {
	Seq    int64
	ID     uuid.UUID
	Reason string
}

func (e *BreakError) Error() string {
	return fmt.Sprintf("audit chain breaks at entry seq %d (%s): %s", e.Seq, e.ID, e.Reason)
}

// bySeq returns entries in chain order, oldest first, without disturbing
// the caller's slice.
//
// Sorting rather than trusting the order given is not defensive dressing:
// the read is ordered by (occurred_at, seq) and the caller hands pages over
// in that order, so this is the one place that turns display order into
// chain order.
func bySeq(entries []storage.AuditEntry) []storage.AuditEntry {
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(a, b storage.AuditEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	return ordered
}

// field writes one value into m, preceded by a presence byte and its length,
// so no two different entries can produce the same MAC input. Without the
// framing a target label of "x" beside an empty detail and a label of ""
// beside a detail of "x" would hash identically, and the length is what
// stops a label containing a separator from forging the field after it.
func field(m hash.Hash, present bool, b []byte) {
	var head [9]byte
	if present {
		head[0] = 1
	}
	binary.BigEndian.PutUint64(head[1:], uint64(len(b)))
	_, _ = m.Write(head[:])
	_, _ = m.Write(b)
}

func uuidField(m hash.Hash, id *uuid.UUID) {
	if id == nil {
		field(m, false, nil)
		return
	}
	field(m, true, []byte(id.String()))
}

// canonicalJSON re-encodes JSON into one form both sides of the chain can
// agree on: object keys sorted, no insignificant whitespace, numbers kept
// as the digits they were written with.
//
// It exists because detail is stored in a jsonb column, and jsonb is not a
// byte store — it reorders keys, drops whitespace and rewrites numbers. The
// bytes handed to the append are therefore not the bytes read back, and
// hashing either one directly would report a break on every entry that has
// a detail. Canonicalizing on both sides is what makes the two comparable.
//
// Invalid JSON is hashed as it stands: nothing this server writes can be
// invalid, and a row that is has already been reached directly — the whole
// point is that the hash then fails to match, which is what happens.
func canonicalJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers stay as their literal digits; decoding them as float64 would
	// round a large integer and make its canonical form depend on how it
	// arrived.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	// json.Marshal sorts map keys, which is the ordering half of canonical.
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// Record is one action to record. Action is the only required field: a nil
// actor is something the system did rather than a person, and a target is
// carried with the label it had AT THE TIME so the log still reads
// correctly after a rename or a deletion.
type Record struct {
	// Action is the namespaced verb, e.g. user.deactivated, invite.created.
	Action string
	// ActorID is the signed-in user who did it, nil for the system.
	ActorID *uuid.UUID
	// TargetID and TargetLabel are what it was done to, and what that was
	// called at the time. Either may be absent.
	TargetID    *uuid.UUID
	TargetLabel string
	// Detail is whatever else is worth keeping, e.g. the fields a settings
	// change touched. Nil for none.
	Detail map[string]any
	// IP is where the request came from; the zero Addr records none.
	IP netip.Addr
}

// Appender is the one storage call recording makes.
type Appender interface {
	AppendAuditEntry(ctx context.Context, e storage.AuditEntry, seal func(storage.AuditEntry) []byte) (storage.AuditEntry, error)
}

// A Recorder appends records to the log.
type Recorder struct {
	chain *Chain
	store Appender
}

// NewRecorder returns a Recorder writing chain-sealed entries to store.
func NewRecorder(chain *Chain, store Appender) *Recorder {
	return &Recorder{chain: chain, store: store}
}

// Record appends rec to the log.
//
// It returns nothing, and that is the design rather than an omission.
// Recording happens after the action it describes has already succeeded and
// been committed, so a failed append must not turn a completed request into
// an error: the user would be told their change failed when it did not, and
// would very reasonably do it again. A failure is logged at error level —
// where an operator's log pipeline sees it — and the request stands. With
// no error to return, a handler cannot get this wrong even by accident.
func (r *Recorder) Record(ctx context.Context, rec Record) {
	// The request context is cancelled the moment the response is written,
	// and this write outlives that. WithoutCancel keeps the values on it and
	// drops only the cancellation; the timeout is what stops a stuck
	// database holding this goroutine indefinitely.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), appendTimeout)
	defer cancel()

	e := storage.AuditEntry{
		ID:       uuid.New(),
		Action:   rec.Action,
		ActorID:  rec.ActorID,
		TargetID: rec.TargetID,
	}
	if rec.TargetLabel != "" {
		label := rec.TargetLabel
		e.TargetLabel = &label
	}
	if rec.IP.IsValid() {
		ip := rec.IP
		e.IP = &ip
	}
	if len(rec.Detail) > 0 {
		detail, err := json.Marshal(rec.Detail)
		if err != nil {
			// Losing the detail is worth the entry; losing the entry is not.
			slog.Error("audit: encode entry detail", "action", rec.Action, "error", err)
		} else {
			e.Detail = detail
		}
	}

	if _, err := r.store.AppendAuditEntry(ctx, e, r.chain.Seal); err != nil {
		slog.Error("audit: append entry", "action", rec.Action, "error", err)
	}
}
