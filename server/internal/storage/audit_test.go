package storage_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// fakeSeal stands in for the real chain: these tests are about the query,
// not the cryptography, and storage never computes a hash itself. It still
// depends on the entry, so a test that reads an entry back can tell whether
// it was sealed with the values that were stored.
func fakeSeal(e storage.AuditEntry) []byte {
	out := make([]byte, 32)
	copy(out, e.Action)
	copy(out[16:], e.PrevHash[:8])
	return out
}

func appendEntry(ctx context.Context, t *testing.T, store *storage.Store, e storage.AuditEntry) storage.AuditEntry {
	t.Helper()
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	stored, err := store.AppendAuditEntry(ctx, e, fakeSeal)
	if err != nil {
		t.Fatalf("AppendAuditEntry(%s): %v", e.Action, err)
	}
	return stored
}

func TestAuditEntriesIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	alice := mustCreateUser(ctx, t, store, newUser("auditalice"))
	bob := mustCreateUser(ctx, t, store, newUser("auditbob"))
	label := "a channel that was renamed later"
	ip := netip.MustParseAddr("2001:db8::1")

	first := appendEntry(ctx, t, store, storage.AuditEntry{Action: "user.created", ActorID: &alice.ID})
	second := appendEntry(ctx, t, store, storage.AuditEntry{
		Action: "invite.created", ActorID: &bob.ID, TargetID: &alice.ID,
		TargetLabel: &label, Detail: []byte(`{"note":"welcome"}`), IP: &ip,
	})
	third := appendEntry(ctx, t, store, storage.AuditEntry{Action: "user.created", ActorID: &alice.ID})
	// Something the system did rather than a person.
	fourth := appendEntry(ctx, t, store, storage.AuditEntry{Action: "invite.redeemed"})

	t.Run("the first entry follows 32 zero bytes", func(t *testing.T) {
		if len(first.PrevHash) != 32 {
			t.Fatalf("first prev_hash is %d bytes, want 32", len(first.PrevHash))
		}
		for _, b := range first.PrevHash {
			if b != 0 {
				t.Fatalf("first prev_hash is %x, want 32 zero bytes", first.PrevHash)
			}
		}
	})

	t.Run("each entry links to the one before it", func(t *testing.T) {
		if string(second.PrevHash) != string(first.EntryHash) {
			t.Error("the second entry does not link to the first")
		}
		if string(third.PrevHash) != string(second.EntryHash) {
			t.Error("the third entry does not link to the second")
		}
		if first.Seq >= second.Seq || second.Seq >= third.Seq {
			t.Errorf("sequence is not increasing: %d, %d, %d", first.Seq, second.Seq, third.Seq)
		}
	})

	t.Run("newest first, with the actor joined", func(t *testing.T) {
		entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: 10})
		if err != nil {
			t.Fatalf("ListAuditEntries: %v", err)
		}
		if len(entries) != 4 {
			t.Fatalf("read back %d entries, want 4", len(entries))
		}
		if entries[0].ID != fourth.ID || entries[3].ID != first.ID {
			t.Errorf("page is not newest first: %v", entries[0].Action)
		}
		if entries[0].Actor != nil {
			t.Error("an entry with no actor came back with one")
		}
		if entries[2].Actor == nil || entries[2].Actor.Username != "auditbob" {
			t.Errorf("actor on the invite entry = %+v, want auditbob", entries[2].Actor)
		}
		if entries[2].TargetLabel == nil || *entries[2].TargetLabel != label {
			t.Errorf("target label = %v, want %q", entries[2].TargetLabel, label)
		}
		if entries[2].IP == nil || entries[2].IP.String() != "2001:db8::1" {
			t.Errorf("address = %v, want 2001:db8::1", entries[2].IP)
		}
		if entries[2].Detail == nil {
			t.Error("the detail came back null")
		}
	})

	t.Run("filters by action", func(t *testing.T) {
		entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Action: "user.created", Limit: 10})
		if err != nil {
			t.Fatalf("ListAuditEntries: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("read back %d entries, want the 2 user.created ones", len(entries))
		}
		for _, e := range entries {
			if e.Action != "user.created" {
				t.Errorf("the action filter let %q through", e.Action)
			}
		}
	})

	t.Run("filters by actor", func(t *testing.T) {
		entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{ActorID: &bob.ID, Limit: 10})
		if err != nil {
			t.Fatalf("ListAuditEntries: %v", err)
		}
		if len(entries) != 1 || entries[0].ID != second.ID {
			t.Fatalf("the actor filter returned %d entries, want only bob's", len(entries))
		}
	})

	t.Run("both filters together", func(t *testing.T) {
		entries, err := store.ListAuditEntries(ctx, storage.ListAuditParams{
			Action: "user.created", ActorID: &bob.ID, Limit: 10,
		})
		if err != nil {
			t.Fatalf("ListAuditEntries: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("read back %d entries, want none: bob created no user", len(entries))
		}
	})

	t.Run("pages on (occurred_at, seq)", func(t *testing.T) {
		page, err := store.ListAuditEntries(ctx, storage.ListAuditParams{Limit: 2})
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(page) != 2 || page[0].ID != fourth.ID {
			t.Fatalf("first page = %d entries starting at %v", len(page), page[0].Action)
		}

		last := page[len(page)-1]
		next, err := store.ListAuditEntries(ctx, storage.ListAuditParams{
			Before: &storage.AuditCursor{OccurredAt: last.OccurredAt, Seq: last.Seq},
			Limit:  10,
		})
		if err != nil {
			t.Fatalf("second page: %v", err)
		}
		if len(next) != 2 {
			t.Fatalf("second page = %d entries, want 2", len(next))
		}
		if next[0].ID != second.ID || next[1].ID != first.ID {
			t.Error("the cursor skipped or repeated an entry")
		}
	})
}
