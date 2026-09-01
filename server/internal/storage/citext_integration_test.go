package storage_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// TestRegisterCitextIntegration pins registerCitext's error contract: a
// missing citext type (the fresh, un-migrated database Open pings first) is
// tolerated, while any other LoadType failure propagates.
func TestRegisterCitextIntegration(t *testing.T) {
	testdb.RequiresPostgres(t, "pgx citext type registration — there is no type to register on SQLite, where citext is a Go collation the driver installs")
	t.Parallel()

	_, raw := testdb.New(t)
	ctx := context.Background()

	connect := func(t *testing.T) *pgx.Conn {
		t.Helper()
		conn, err := pgx.Connect(ctx, raw.DSN())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}

	t.Run("registers the type on a migrated database", func(t *testing.T) {
		if err := storage.RegisterCitext(ctx, connect(t)); err != nil {
			t.Errorf("RegisterCitext on a migrated database: %v", err)
		}
	})

	t.Run("propagates failures that are not undefined_object", func(t *testing.T) {
		conn := connect(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := storage.RegisterCitext(canceled, conn); err == nil {
			t.Error("RegisterCitext with a canceled context returned nil, want the failure propagated")
		}
	})

	// Last on purpose: it destroys the scratch schema.
	t.Run("tolerates the type missing before migrations", func(t *testing.T) {
		conn := connect(t)
		// Simulate the pre-migration state Open encounters on a fresh
		// database: no citext type. CASCADE also drops the dependent user
		// columns — fine, this scratch database is finished after this.
		if _, err := conn.Exec(ctx, `DROP EXTENSION citext CASCADE`); err != nil {
			t.Fatalf("drop citext extension: %v", err)
		}
		if err := storage.RegisterCitext(ctx, conn); err != nil {
			t.Errorf("RegisterCitext without the citext type: %v, want nil (tolerated)", err)
		}
	})
}
