package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// testKey is a fixed key for these tests. Nothing in the repository may ship
// a working default (CLAUDE.md), which is exactly why this one lives in a
// test file and nowhere else.
const testKey = "audit chain test key, at least 32 bytes long"

func newChain(t *testing.T) *audit.Chain {
	t.Helper()
	c, err := audit.New([]byte(testKey))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	return c
}

// sealed builds a valid chain out of entries: each one links to the one
// before it and carries the hash it would have been stored with.
func sealed(c *audit.Chain, entries ...storage.AuditEntry) []storage.AuditEntry {
	prev := make([]byte, 32)
	out := make([]storage.AuditEntry, 0, len(entries))
	for i, e := range entries {
		e.Seq = int64(i + 1)
		if e.ID == uuid.Nil {
			e.ID = uuid.New()
		}
		if e.OccurredAt.IsZero() {
			e.OccurredAt = time.Date(2026, 8, 26, 12, 0, i, 0, time.UTC)
		}
		e.PrevHash = prev
		e.EntryHash = c.Seal(e)
		prev = e.EntryHash
		out = append(out, e)
	}
	return out
}

func TestNewRejectsAShortKey(t *testing.T) {
	t.Parallel()

	if _, err := audit.New([]byte("too short")); err == nil {
		t.Fatal("audit.New accepted a 9-byte key, want a refusal")
	}
}

// Not parallel: t.Setenv and t.Parallel are mutually exclusive, and the
// environment is process-wide anyway.
func TestFromEnvNamesTheVariable(t *testing.T) {
	t.Setenv(audit.EnvKey, "")
	_, err := audit.FromEnv()
	if err == nil {
		t.Fatal("FromEnv with no key returned no error, want a startup failure")
	}
	if !strings.Contains(err.Error(), audit.EnvKey) {
		t.Errorf("FromEnv error %q does not name %s", err, audit.EnvKey)
	}

	t.Setenv(audit.EnvKey, testKey)
	if _, err = audit.FromEnv(); err != nil {
		t.Errorf("FromEnv with a valid key: %v", err)
	}
}

// TestSealCoversEveryRecordedField is the property the whole feature rests
// on: change anything the log records, and the seal no longer matches.
func TestSealCoversEveryRecordedField(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	actor := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	target := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	label := "sarah.amini"
	ip := netip.MustParseAddr("192.0.2.7")
	base := storage.AuditEntry{
		ID:          uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		Action:      "user.deactivated",
		ActorID:     &actor,
		TargetID:    &target,
		TargetLabel: &label,
		Detail:      []byte(`{"reason":"left the company"}`),
		IP:          &ip,
		OccurredAt:  time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
		PrevHash:    make([]byte, 32),
	}
	want := c.Seal(base)

	otherUUID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	otherLabel := "somebody.else"
	otherIP := netip.MustParseAddr("198.51.100.9")
	otherPrev := append(make([]byte, 31), 1)

	tests := map[string]func(e *storage.AuditEntry){
		"id":           func(e *storage.AuditEntry) { e.ID = otherUUID },
		"action":       func(e *storage.AuditEntry) { e.Action = "user.reactivated" },
		"actor":        func(e *storage.AuditEntry) { e.ActorID = &otherUUID },
		"actor null":   func(e *storage.AuditEntry) { e.ActorID = nil },
		"target":       func(e *storage.AuditEntry) { e.TargetID = &otherUUID },
		"target null":  func(e *storage.AuditEntry) { e.TargetID = nil },
		"label":        func(e *storage.AuditEntry) { e.TargetLabel = &otherLabel },
		"label null":   func(e *storage.AuditEntry) { e.TargetLabel = nil },
		"detail":       func(e *storage.AuditEntry) { e.Detail = []byte(`{"reason":"nothing happened"}`) },
		"detail null":  func(e *storage.AuditEntry) { e.Detail = nil },
		"ip":           func(e *storage.AuditEntry) { e.IP = &otherIP },
		"ip null":      func(e *storage.AuditEntry) { e.IP = nil },
		"occurred at":  func(e *storage.AuditEntry) { e.OccurredAt = e.OccurredAt.Add(time.Microsecond) },
		"predecessor":  func(e *storage.AuditEntry) { e.PrevHash = otherPrev },
		"seq (not in)": func(e *storage.AuditEntry) { e.Seq = 99 },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mutated := base
			mutate(&mutated)
			got := c.Seal(mutated)
			// Seq is deliberately outside the MAC (Seal says why), so it is
			// the one mutation that must NOT change the hash.
			if name == "seq (not in)" {
				if string(got) != string(want) {
					t.Error("changing seq changed the seal; seq is not one of the sealed fields")
				}
				return
			}
			if string(got) == string(want) {
				t.Errorf("changing %s left the seal unchanged", name)
			}
		})
	}
}

// TestSealIgnoresTheActorCopy pins the other half of that boundary: the
// display name a page carries is not sealed, so renaming somebody does not
// read as a tamper.
func TestSealIgnoresTheActorCopy(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	actor := uuid.New()
	e := storage.AuditEntry{
		ID: uuid.New(), Action: "invite.created", ActorID: &actor,
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond), PrevHash: make([]byte, 32),
	}
	before := c.Seal(e)

	e.Actor = &storage.AuditActor{ID: actor, Username: "renamed", DisplayName: "Renamed Person"}
	if string(c.Seal(e)) != string(before) {
		t.Error("the joined actor changed the seal; a rename must not read as a tamper")
	}
}

// TestSealSurvivesTheJSONBRoundTrip is why canonicalization exists. jsonb is
// not a byte store: it reorders keys and re-spaces the text, so the bytes
// written are not the bytes read back, and hashing either directly would
// report a break on every entry that carries a detail.
func TestSealSurvivesTheJSONBRoundTrip(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	base := storage.AuditEntry{
		ID: uuid.New(), Action: "org.updated",
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond), PrevHash: make([]byte, 32),
	}

	written := base
	written.Detail = []byte(`{"session_lifetime_hours":720,"registration_mode":"invite","require_totp":true}`)

	readBack := base
	// What PostgreSQL hands back: keys reordered (shortest first), a space
	// after every colon and comma.
	readBack.Detail = []byte(`{"require_totp": true, "registration_mode": "invite", "session_lifetime_hours": 720}`)

	if string(c.Seal(written)) != string(c.Seal(readBack)) {
		t.Error("the same detail sealed differently before and after a jsonb round trip")
	}
}

// TestSealKeepsLargeIntegersExact guards the canonicalization itself:
// decoding numbers as float64 would round a large integer, and the entry
// would stop verifying the moment it was read back.
func TestSealKeepsLargeIntegersExact(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	base := storage.AuditEntry{
		ID: uuid.New(), Action: "user.updated",
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond), PrevHash: make([]byte, 32),
	}
	written, readBack := base, base
	written.Detail = []byte(`{"n":9007199254740993}`)
	readBack.Detail = []byte(`{"n": 9007199254740993}`)

	if string(c.Seal(written)) != string(c.Seal(readBack)) {
		t.Error("a 2^53+1 integer did not survive canonicalization")
	}
}

func TestVerifyAcceptsAnUntouchedChain(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	entries := sealed(c,
		storage.AuditEntry{Action: "user.created"},
		storage.AuditEntry{Action: "invite.created"},
		storage.AuditEntry{Action: "user.deactivated"},
	)

	if err := c.Verify(entries); err != nil {
		t.Errorf("Verify on an untouched chain: %v", err)
	}
	if err := c.VerifyRange(entries); err != nil {
		t.Errorf("VerifyRange on an untouched chain: %v", err)
	}
}

func TestVerifyReportsTheEditedEntry(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	entries := sealed(c,
		storage.AuditEntry{Action: "user.created"},
		storage.AuditEntry{Action: "user.deactivated"},
		storage.AuditEntry{Action: "invite.created"},
	)
	entries[1].Action = "user.reactivated"

	var brk *audit.BreakError
	err := c.Verify(entries)
	if !errors.As(err, &brk) {
		t.Fatalf("Verify over an edited entry returned %v, want a *BreakError", err)
	}
	if brk.Seq != 2 {
		t.Errorf("break reported at seq %d, want 2", brk.Seq)
	}
}

// TestVerifyRangeReportsTheRemovedRow is the deletion half: an entry that no
// longer matches anything is caught by its own seal, but a row that is
// simply gone leaves every remaining seal intact. Only the linkage sees it.
func TestVerifyRangeReportsTheRemovedRow(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	entries := sealed(c,
		storage.AuditEntry{Action: "user.created"},
		storage.AuditEntry{Action: "user.deactivated"},
		storage.AuditEntry{Action: "invite.created"},
	)
	remaining := []storage.AuditEntry{entries[0], entries[2]}

	if err := c.Verify(remaining); err != nil {
		t.Errorf("Verify over a gap reported %v; entry seals alone cannot see a removal", err)
	}

	var brk *audit.BreakError
	err := c.VerifyRange(remaining)
	if !errors.As(err, &brk) {
		t.Fatalf("VerifyRange over a gap returned %v, want a *BreakError", err)
	}
	if brk.Seq != 3 {
		t.Errorf("break reported at seq %d, want 3 — the entry that followed the removed row", brk.Seq)
	}
}

// TestVerifyIsIndependentOfTheOrderGiven: pages arrive newest first, and
// verification has to walk the chain's own order regardless.
func TestVerifyIsIndependentOfTheOrderGiven(t *testing.T) {
	t.Parallel()

	c := newChain(t)
	entries := sealed(c,
		storage.AuditEntry{Action: "user.created"},
		storage.AuditEntry{Action: "user.deactivated"},
	)
	newestFirst := []storage.AuditEntry{entries[1], entries[0]}

	if err := c.VerifyRange(newestFirst); err != nil {
		t.Errorf("VerifyRange on a newest-first page: %v", err)
	}
	// And the caller's slice is left alone.
	if newestFirst[0].Seq != 2 {
		t.Error("VerifyRange reordered the caller's slice")
	}
}

func TestVerifyFailsUnderAnotherKey(t *testing.T) {
	t.Parallel()

	entries := sealed(newChain(t), storage.AuditEntry{Action: "user.created"})
	other, err := audit.New([]byte("a completely different key, 32+ bytes"))
	if err != nil {
		t.Fatalf("audit.New: %v", err)
	}
	if err = other.Verify(entries); err == nil {
		t.Error("a chain sealed with one key verified under another")
	}
}

// stubAppender records what it was handed and answers with err. It keeps
// the context it was called on so the cancellation assertion below can look
// at it, which is the one thing a test may hold a context for.
type stubAppender struct {
	err    error
	got    storage.AuditEntry
	gotErr error
	calls  int
}

func (s *stubAppender) AppendAuditEntry(ctx context.Context, e storage.AuditEntry,
	seal func(storage.AuditEntry) []byte,
) (storage.AuditEntry, error) {
	s.calls++
	s.got = e
	s.gotErr = ctx.Err()
	if s.err != nil {
		return storage.AuditEntry{}, s.err
	}
	e.EntryHash = seal(e)
	return e, nil
}

// TestRecordSurvivesAFailedAppend is the rule the interface enforces by
// shape: an audit write that fails must not fail the request that caused it,
// so Record has no error to return and none to be tempted by.
func TestRecordSurvivesAFailedAppend(t *testing.T) {
	t.Parallel()

	store := &stubAppender{err: errors.New("database is on fire")}
	rec := audit.NewRecorder(newChain(t), store)

	rec.Record(context.Background(), audit.Record{Action: "user.deactivated"})

	if store.calls != 1 {
		t.Fatalf("append called %d times, want 1", store.calls)
	}
	// Reaching here at all is the assertion: Record returned normally with
	// the append refusing.
}

// TestRecordOutlivesTheRequestContext: the response is written before the
// entry is, so a recorder bound to the request's cancellation would drop
// exactly the entries worth having.
func TestRecordOutlivesTheRequestContext(t *testing.T) {
	t.Parallel()

	store := &stubAppender{}
	rec := audit.NewRecorder(newChain(t), store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec.Record(ctx, audit.Record{Action: "user.deactivated"})

	if store.calls != 1 {
		t.Fatalf("append called %d times, want 1", store.calls)
	}
	if store.gotErr != nil {
		t.Errorf("the append ran on a cancelled context (%v); recording must outlive the request", store.gotErr)
	}
}

func TestRecordMapsAbsentFieldsToNulls(t *testing.T) {
	t.Parallel()

	store := &stubAppender{}
	rec := audit.NewRecorder(newChain(t), store)

	rec.Record(context.Background(), audit.Record{Action: "system.started"})

	got := store.got
	switch {
	case got.ID == uuid.Nil:
		t.Error("recorded entry has no id")
	case got.ActorID != nil:
		t.Error("an absent actor was recorded as a value, want null")
	case got.TargetID != nil:
		t.Error("an absent target was recorded as a value, want null")
	case got.TargetLabel != nil:
		t.Error("an empty label was recorded as a value, want null")
	case got.Detail != nil:
		t.Error("an absent detail was recorded as a value, want null")
	case got.IP != nil:
		t.Error("an invalid address was recorded as a value, want null")
	}
}

func TestRecordCarriesEveryFieldItIsGiven(t *testing.T) {
	t.Parallel()

	store := &stubAppender{}
	rec := audit.NewRecorder(newChain(t), store)
	actor, target := uuid.New(), uuid.New()

	rec.Record(context.Background(), audit.Record{
		Action:      "user.deactivated",
		ActorID:     &actor,
		TargetID:    &target,
		TargetLabel: "sarah.amini",
		Detail:      map[string]any{"reason": "left the company"},
		IP:          netip.MustParseAddr("192.0.2.7"),
	})

	got := store.got
	if got.Action != "user.deactivated" || got.ActorID == nil || *got.ActorID != actor {
		t.Fatalf("recorded %+v, want the action and actor it was given", got)
	}
	if got.TargetID == nil || *got.TargetID != target {
		t.Errorf("recorded target %v, want %v", got.TargetID, target)
	}
	if got.TargetLabel == nil || *got.TargetLabel != "sarah.amini" {
		t.Errorf("recorded label %v, want sarah.amini", got.TargetLabel)
	}
	if got.IP == nil || got.IP.String() != "192.0.2.7" {
		t.Errorf("recorded address %v, want 192.0.2.7", got.IP)
	}
	var detail map[string]any
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatalf("recorded detail does not decode: %v", err)
	}
	if detail["reason"] != "left the company" {
		t.Errorf("recorded detail %v, want the reason it was given", detail)
	}
}
