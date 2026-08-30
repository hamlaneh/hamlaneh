package sqlitestore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
)

// TestOpenMigratesAndIsReady is the driver's own smoke test: a first run
// creates the database file and brings the schema up, Ready agrees, and
// opening the same file again re-migrates as a no-op.
//
// It needs no container and no environment variable, which is the property
// that lets home mode's storage suite run on a developer's own machine.
func TestOpenMigratesAndIsReady(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	path := filepath.Join(t.TempDir(), "hamlaneh.db")

	store, err := sqlitestore.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	if err := store.Ready(ctx); err != nil {
		t.Errorf("Ready right after Open: %v", err)
	}

	again, err := sqlitestore.Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(again.Close)

	if err := again.Ready(ctx); err != nil {
		t.Errorf("Ready after second Open: %v", err)
	}
}

// TestOpenRefusesAnEmptyPath pins the no-silent-fallback rule at this end:
// a Store with nowhere to put its file must fail, never quietly pick one.
func TestOpenRefusesAnEmptyPath(t *testing.T) {
	t.Parallel()

	if _, err := sqlitestore.Open(t.Context(), ""); err == nil {
		t.Fatal("Open(\"\") = nil error, want a refusal")
	}
}
