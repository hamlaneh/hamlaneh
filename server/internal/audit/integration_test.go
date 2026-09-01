package audit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// appendN records n entries through the recorder and returns the log.
func appendN(ctx context.Context, t *testing.T, store testdb.Store, chain *audit.Chain, n int) []storage.AuditEntry {
	t.Helper()

	actor, err := store.CreateUser(ctx, storage.NewUser{
		Username: "auditor" + uuid.NewString()[:8], DisplayName: "The Auditor",
		PasswordHash: "fake-hash", Locale: "en", IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("create the acting user: %v", err)
	}

	rec := audit.NewRecorder(chain, store)
	ip := netip.MustParseAddr("192.0.2.7")
	for i := range n {
		rec.Record(ctx, audit.Record{
			Action:      fmt.Sprintf("test.action%d", i),
			ActorID:     &actor.ID,
			TargetID:    &actor.ID,
			TargetLabel: actor.Username,
			Detail:      map[string]any{"index": i, "note": "detail with a jsonb round trip"},
			IP:          ip,
		})
	}

	entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: n + 1})
	if err != nil {
		t.Fatalf("read the log back: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("read back %d entries, want %d — recording dropped some", len(entries), n)
	}
	return entries
}

// TestRecordedChainVerifies is the baseline the two tamper tests below are
// only meaningful against: everything this server writes, read back through
// jsonb and inet and timestamptz, still verifies.
func TestRecordedChainVerifies(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)

	entries := appendN(ctx, t, store, chain, 5)
	if err := chain.VerifyRange(entries); err != nil {
		t.Fatalf("a chain this server wrote does not verify: %v", err)
	}
	if entries[0].Detail == nil {
		t.Error("the detail did not survive the round trip")
	}
	if entries[0].Actor == nil {
		t.Error("the actor was not joined into the page")
	}
}

// TestEditingARowBreaksVerification is the whole feature. Somebody with
// database access changes one column of one row; the log must stop
// verifying, and must say where.
func TestEditingARowBreaksVerification(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)

	entries := appendN(ctx, t, store, chain, 5)
	before := entries[len(entries)-3] // seq 3, newest first

	// One bound value per case, so the same statement runs on both drivers:
	// what each engine spells differently — a jsonb cast, an interval, a
	// decode(repeat(...)) — is a Go value here instead.
	tests := map[string]struct {
		statement string
		value     any
	}{
		"the action":     {`UPDATE audit_entries SET action = ? WHERE seq = 3`, "test.something.else"},
		"the target":     {`UPDATE audit_entries SET target_label = ? WHERE seq = 3`, "somebody.else"},
		"the actor":      {`UPDATE audit_entries SET actor_id = ? WHERE seq = 3`, nil},
		"the detail":     {`UPDATE audit_entries SET detail = ? WHERE seq = 3`, `{"index": 99}`},
		"the address":    {`UPDATE audit_entries SET ip = ? WHERE seq = 3`, "198.51.100.9"},
		"the time":       {`UPDATE audit_entries SET occurred_at = ? WHERE seq = 3`, before.OccurredAt.Add(time.Hour)},
		"the link":       {`UPDATE audit_entries SET prev_hash = ? WHERE seq = 3`, bytes.Repeat([]byte{0x00}, 32)},
		"the seal (all)": {`UPDATE audit_entries SET entry_hash = ? WHERE seq = 3`, bytes.Repeat([]byte{0xff}, 32)},
	}

	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			// Deliberately NOT parallel: each case edits the same row and
			// puts it back, so they have to take turns.
			raw.Exec(ctx, t, tamper.statement, tamper.value)
			t.Cleanup(func() { restoreEntry(ctx, t, raw, before) })

			tampered, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: 10})
			if err != nil {
				t.Fatalf("read the log back: %v", err)
			}

			var brk *audit.BreakError
			verifyErr := chain.VerifyRange(tampered)
			if !errors.As(verifyErr, &brk) {
				t.Fatalf("verification of a tampered log returned %v, want a *BreakError", verifyErr)
			}
			if brk.Seq != 3 {
				t.Errorf("break reported at seq %d, want 3 — the row that was edited", brk.Seq)
			}
		})
	}
}

// TestDeletingARowBreaksVerification: a removed entry leaves every remaining
// seal intact, so only the linkage can see it — and it is seen at the entry
// that used to follow the one now missing.
func TestDeletingARowBreaksVerification(t *testing.T) {
	t.Parallel()

	store, raw := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)

	appendN(ctx, t, store, chain, 5)
	raw.Exec(ctx, t, `DELETE FROM audit_entries WHERE seq = 3`)

	remaining, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: 10})
	if err != nil {
		t.Fatalf("read the log back: %v", err)
	}
	if len(remaining) != 4 {
		t.Fatalf("read back %d entries after the delete, want 4", len(remaining))
	}

	var brk *audit.BreakError
	if verifyErr := chain.VerifyRange(remaining); !errors.As(verifyErr, &brk) {
		t.Fatalf("verification after a delete returned %v, want a *BreakError", verifyErr)
	}
	if brk.Seq != 4 {
		t.Errorf("break reported at seq %d, want 4 — the entry that followed the deleted row", brk.Seq)
	}
}

// TestConcurrentAppendsProduceOneValidChain is the ordering guarantee: two
// appends racing must not both link to the same predecessor. Run with
// -count above 1; a chain that forks fails here rather than months later,
// as a break nobody can tell from a tamper.
func TestConcurrentAppendsProduceOneValidChain(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)
	rec := audit.NewRecorder(chain, store)

	const writers = 12
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func() {
			defer wg.Done()
			rec.Record(ctx, audit.Record{
				Action: fmt.Sprintf("test.concurrent%d", i),
				Detail: map[string]any{"writer": i},
			})
		}()
	}
	wg.Wait()

	entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: writers + 1})
	if err != nil {
		t.Fatalf("read the log back: %v", err)
	}
	if len(entries) != writers {
		t.Fatalf("recorded %d entries, want %d", len(entries), writers)
	}
	if err = chain.VerifyRange(entries); err != nil {
		t.Fatalf("%d concurrent appends produced a broken chain: %v", writers, err)
	}

	// The linkage check above proves no two entries claim the same
	// predecessor. This pins the other half of it: the page order a reader
	// gets IS the chain order, which is what makes that check meaningful
	// across a page boundary.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Seq <= entries[i].Seq {
			t.Fatalf("page is not newest-first by seq at %d: %d then %d",
				i, entries[i-1].Seq, entries[i].Seq)
		}
		if entries[i-1].OccurredAt.Before(entries[i].OccurredAt) {
			t.Errorf("entry seq %d is stamped before seq %d it follows",
				entries[i-1].Seq, entries[i].Seq)
		}
	}
}

// restoreEntry puts a row back exactly as it was, so the next subtest starts
// from an intact chain.
//
// The detail and the address are bound as strings rather than as the []byte
// and *netip.Addr the entry carries: the columns are jsonb and inet on
// PostgreSQL and plain TEXT on SQLite, and a string is the one encoding both
// take. It restores the same bytes either way — jsonb re-normalises what it
// already normalised on the way out, and SQLite stores text byte for byte.
func restoreEntry(ctx context.Context, t *testing.T, raw *testdb.Raw, e storage.AuditEntry) {
	t.Helper()

	var detail, ip any
	if e.Detail != nil {
		detail = string(e.Detail)
	}
	if e.IP != nil {
		ip = e.IP.String()
	}
	raw.Exec(ctx, t,
		`UPDATE audit_entries
		 SET action = ?, actor_id = ?, target_id = ?, target_label = ?,
		     detail = ?, ip = ?, occurred_at = ?, prev_hash = ?, entry_hash = ?
		 WHERE seq = ?`,
		e.Action, e.ActorID, e.TargetID, e.TargetLabel,
		detail, ip, e.OccurredAt, e.PrevHash, e.EntryHash, e.Seq)
}
