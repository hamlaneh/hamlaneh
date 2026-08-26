package audit_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// rawConn opens a second connection to the scratch database, for the SQL a
// test has to run BEHIND the application: these tests exist to prove what
// happens when somebody edits the table directly, and going through the
// store would prove nothing, because the store has no way to do it.
func rawConn(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// appendN records n entries through the recorder and returns the log.
func appendN(ctx context.Context, t *testing.T, store *storage.Store, chain *audit.Chain, n int) []storage.AuditEntry {
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

	store, dsn := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)
	conn := rawConn(ctx, t, dsn)

	entries := appendN(ctx, t, store, chain, 5)

	tests := map[string]string{
		"the action":     `UPDATE audit_entries SET action = 'test.something.else' WHERE seq = 3`,
		"the target":     `UPDATE audit_entries SET target_label = 'somebody.else' WHERE seq = 3`,
		"the actor":      `UPDATE audit_entries SET actor_id = NULL WHERE seq = 3`,
		"the detail":     `UPDATE audit_entries SET detail = '{"index": 99}'::jsonb WHERE seq = 3`,
		"the address":    `UPDATE audit_entries SET ip = '198.51.100.9' WHERE seq = 3`,
		"the time":       `UPDATE audit_entries SET occurred_at = occurred_at + interval '1 hour' WHERE seq = 3`,
		"the link":       `UPDATE audit_entries SET prev_hash = decode(repeat('00', 32), 'hex') WHERE seq = 3`,
		"the seal (all)": `UPDATE audit_entries SET entry_hash = decode(repeat('ff', 32), 'hex') WHERE seq = 3`,
	}

	for name, statement := range tests {
		t.Run(name, func(t *testing.T) {
			// Deliberately NOT parallel: each case edits the same row and
			// puts it back, so they have to take turns.
			before := entries[len(entries)-3] // seq 3, newest first
			if _, err := conn.Exec(ctx, statement); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			t.Cleanup(func() { restoreEntry(ctx, t, conn, before) })

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

	store, dsn := testdb.New(t)
	ctx := context.Background()
	chain := newChain(t)
	conn := rawConn(ctx, t, dsn)

	appendN(ctx, t, store, chain, 5)
	if _, err := conn.Exec(ctx, `DELETE FROM audit_entries WHERE seq = 3`); err != nil {
		t.Fatalf("delete an entry: %v", err)
	}

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
func restoreEntry(ctx context.Context, t *testing.T, conn *pgx.Conn, e storage.AuditEntry) {
	t.Helper()
	_, err := conn.Exec(ctx,
		`UPDATE audit_entries
		 SET action = $2, actor_id = $3, target_id = $4, target_label = $5,
		     detail = $6, ip = $7, occurred_at = $8, prev_hash = $9, entry_hash = $10
		 WHERE seq = $1`,
		e.Seq, e.Action, e.ActorID, e.TargetID, e.TargetLabel,
		e.Detail, e.IP, e.OccurredAt, e.PrevHash, e.EntryHash)
	if err != nil {
		t.Fatalf("restore entry seq %d: %v", e.Seq, err)
	}
}
