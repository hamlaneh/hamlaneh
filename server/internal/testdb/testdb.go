// Package testdb provisions throwaway PostgreSQL databases for integration
// tests. Each test gets its own freshly-migrated database, so tests never
// see each other's rows and packages can run in parallel against one
// PostgreSQL instance.
//
// Tests are skipped unless HAMLANEH_TEST_DSN points at a disposable
// PostgreSQL whose role may CREATE DATABASE (the postgres:17-alpine
// container default), for example:
//
//	HAMLANEH_TEST_DSN='postgres://user:pass@127.0.0.1:5544/db?sslmode=disable' go test ./...
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// EnvDSN is the environment variable holding the admin DSN.
const EnvDSN = "HAMLANEH_TEST_DSN"

const setupTimeout = 60 * time.Second

// New creates a throwaway database, opens a migrated *storage.Store on it,
// and returns the store plus the database's DSN for tests that need raw
// assertions. Cleanup drops the database when the test finishes. Tests are
// skipped loudly when HAMLANEH_TEST_DSN is unset.
func New(t *testing.T) (*storage.Store, string) {
	t.Helper()

	adminDSN := os.Getenv(EnvDSN)
	if adminDSN == "" {
		t.Skipf("SKIPPING integration test: set %s to a disposable postgres:// DSN to run it", EnvDSN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	name := scratchName(t)
	adminConn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("testdb: connect to %s: %v", EnvDSN, err)
	}
	// Database names cannot be bound parameters; name is generated hex and
	// double-quoted, never user input.
	if _, execErr := adminConn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); execErr != nil {
		_ = adminConn.Close(ctx)
		t.Fatalf("testdb: create scratch database: %v", execErr)
	}
	if closeErr := adminConn.Close(ctx); closeErr != nil {
		t.Fatalf("testdb: close admin connection: %v", closeErr)
	}

	t.Cleanup(func() { dropDatabase(t, adminDSN, name) })

	dsn, err := replaceDatabase(adminDSN, name)
	if err != nil {
		t.Fatalf("testdb: build scratch DSN: %v", err)
	}

	store, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("testdb: open scratch database: %v", err)
	}
	t.Cleanup(store.Close)

	return store, dsn
}

// scratchName generates a unique database name like hamlaneh_test_a1b2c3d4.
func scratchName(t *testing.T) string {
	t.Helper()

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("testdb: generate database name: %v", err)
	}
	return "hamlaneh_test_" + hex.EncodeToString(buf)
}

// replaceDatabase swaps the database path of a postgres:// DSN.
func replaceDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DSN: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// dropDatabase force-drops the scratch database, disconnecting any
// stragglers (PostgreSQL 13+).
func dropDatabase(t *testing.T, adminDSN, name string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Errorf("testdb: connect to drop scratch database %s: %v", name, err)
		return
	}
	defer func() {
		if err := conn.Close(ctx); err != nil {
			t.Errorf("testdb: close drop connection: %v", err)
		}
	}()

	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name)); err != nil {
		t.Errorf("testdb: drop scratch database %s: %v", name, err)
	}
}
