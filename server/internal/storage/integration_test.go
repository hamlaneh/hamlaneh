package storage_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestStorageIntegration exercises Open against a real PostgreSQL: connect,
// migrate, verify the schema, and prove that migrating twice is a no-op.
//
// It needs a disposable database and is skipped unless HAMLANEH_TEST_DSN is
// set, for example:
//
//	HAMLANEH_TEST_DSN='postgres://user:pass@127.0.0.1:5544/db?sslmode=disable' go test ./internal/storage/
func TestStorageIntegration(t *testing.T) {
	dsn := os.Getenv("HAMLANEH_TEST_DSN")
	if dsn == "" {
		t.Skip("SKIPPING storage integration test: set HAMLANEH_TEST_DSN to a disposable postgres:// DSN to run it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if readyErr := store.Ready(ctx); readyErr != nil {
		t.Errorf("Ready right after Open: %v", readyErr)
	}

	// A separate pool for assertions; Store deliberately does not expose its
	// internals.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open assertion pool: %v", err)
	}
	defer pool.Close()

	t.Run("users table has the expected columns", func(t *testing.T) {
		rows, err := pool.Query(ctx,
			`SELECT column_name FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'users'
			 ORDER BY column_name`)
		if err != nil {
			t.Fatalf("query users columns: %v", err)
		}
		defer rows.Close()

		got := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan column name: %v", err)
			}
			got = append(got, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate users columns: %v", err)
		}

		want := []string{
			"created_at", "display_name", "email", "id", "is_active", "is_admin",
			"locale", "must_change_password", "password_hash",
			// Migration 0014: the two columns that make an account
			// directory-managed.
			"scim_external_id", "scim_user_name",
			"updated_at", "username",
		}
		if !slices.Equal(got, want) {
			t.Errorf("users columns:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("schema version is recorded clean", func(t *testing.T) {
		version, dirty := schemaVersion(ctx, t, pool)
		if version < 1 {
			t.Errorf("schema_migrations version = %d, want >= 1", version)
		}
		if dirty {
			t.Error("schema_migrations reports a dirty migration")
		}
	})

	t.Run("opening again re-migrates as a no-op", func(t *testing.T) {
		before, _ := schemaVersion(ctx, t, pool)

		again, err := storage.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("second Open: %v", err)
		}
		defer again.Close()

		if err := again.Ready(ctx); err != nil {
			t.Errorf("Ready after second Open: %v", err)
		}

		after, dirty := schemaVersion(ctx, t, pool)
		if after != before {
			t.Errorf("schema version changed on re-migrate: %d -> %d", before, after)
		}
		if dirty {
			t.Error("re-migrating left the schema dirty")
		}
	})
}

func schemaVersion(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (version int64, dirty bool) {
	t.Helper()

	err := pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return version, dirty
}
