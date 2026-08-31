// Package testdb provisions throwaway databases for integration tests, on
// either of the two storage drivers.
//
// # The driver matrix
//
// CLAUDE.md commits the storage suite to both drivers from Phase 4 on, and
// ADR 012 decision 3 says how: one behavioural suite, run twice, with the
// driver selected by this harness rather than by the tests. HAMLANEH_TEST_DRIVER
// picks it:
//
//	HAMLANEH_TEST_DRIVER=sqlite   — a file in the test's own temporary
//	                                directory. No container, no environment,
//	                                which is why this is the default and why
//	                                the storage suite finally runs on a bare
//	                                developer machine.
//	HAMLANEH_TEST_DRIVER=postgres — a freshly-created scratch database on the
//	                                PostgreSQL named by HAMLANEH_TEST_DSN,
//	                                dropped when the test finishes. Selected
//	                                automatically when that DSN is set, so an
//	                                existing CI job needs no change.
//
// A handful of tests assert PostgreSQL MECHANISM rather than contract
// outcome. They call RequiresPostgres, which is counted and checked against a
// per-package allow-list — see postgresonly.go. Every other test must pass on
// both drivers.
//
// The PostgreSQL leg needs a disposable server whose role may CREATE DATABASE
// (the postgres:17-alpine container default), for example:
//
//	HAMLANEH_TEST_DSN='postgres://user:pass@127.0.0.1:5544/db?sslmode=disable' go test ./...
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/linkpreview"
	"github.com/hamlaneh/hamlaneh/server/internal/passwordreset"
	"github.com/hamlaneh/hamlaneh/server/internal/scim"
	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// EnvDSN is the environment variable holding the PostgreSQL admin DSN.
const EnvDSN = "HAMLANEH_TEST_DSN"

// EnvDriver selects which driver the suite runs against.
const EnvDriver = "HAMLANEH_TEST_DRIVER"

// The two drivers, by the names EnvDriver takes.
const (
	DriverPostgres = "postgres"
	DriverSQLite   = "sqlite"
)

const setupTimeout = 60 * time.Second

// Store is what the storage suite drives.
//
// It is deliberately NOT a new abstraction over storage. ADR 012's acceptance
// note refuses a producer-side twin of the concrete pgx struct — a hundred-odd
// methods with one caller and two implementations to keep in step — and this
// is not one: it is the union of the CONSUMER interfaces that already exist,
// each declared by the package that needs it, plus a short residue named
// below. Assembling it this way has a second benefit worth the two extra
// lines: the residue is exactly the list of storage methods that no consumer
// interface names yet, which is a fact worth being able to read.
type Store interface {
	// The HTTP layer's 80-odd reads and writes, and the four narrow
	// interfaces the other consumers declare for themselves.
	//
	// internal/wsgateway declares a fifth, and it is deliberately NOT embedded
	// here: its five reads are all already in httpserver.Store, and importing
	// that package would close an import cycle through its own in-package
	// tests, which use this harness.
	httpserver.Store
	bootstrap.Store
	scim.Store
	passwordreset.Store
	linkpreview.Store

	// The residue. Each of these IS named by a consumer interface, but by an
	// unexported one inside internal/httpserver (messageWriter and
	// messageSearcher in message_handlers.go and search_handler.go,
	// attachmentReader and previewImageReader in files_origin.go), so it
	// cannot be embedded from here. Exporting those would widen that
	// package's API for a test harness, which is the wrong direction.
	MessageByID(ctx context.Context, channelID, messageID uuid.UUID) (storage.Message, error)
	UpdateMessageContent(ctx context.Context, channelID, messageID uuid.UUID, content string, mls *storage.MessageMls) (storage.Message, error)
	SoftDeleteMessage(ctx context.Context, channelID, messageID, deletedBy uuid.UUID) (storage.Message, error)
	SearchMessages(ctx context.Context, params storage.SearchMessagesParams) (storage.SearchPage, error)
	SearchFiles(ctx context.Context, params storage.SearchFilesParams) (storage.FileSearchPage, error)
	AttachmentByID(ctx context.Context, id uuid.UUID) (storage.Attachment, error)
	LinkPreviewImageExists(ctx context.Context, blobID uuid.UUID) (bool, error)

	// And these are named by nothing outside storage itself: the page-wide
	// reads the message loader uses, the orphan sweep, and the caller-less
	// channel read.
	ChannelByID(ctx context.Context, id uuid.UUID) (storage.Channel, error)
	AttachmentsByMessages(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID][]storage.Attachment, error)
	LinkPreviewsByMessage(ctx context.Context, messageIDs []uuid.UUID) (map[uuid.UUID]storage.LinkPreview, error)
	SweepOrphanAttachments(ctx context.Context, age time.Duration) ([]uuid.UUID, error)

	Close()
}

// Both drivers satisfy the whole surface at compile time, which is what makes
// "one suite, two drivers" a fact the compiler checks rather than a promise.
var (
	_ Store = (*storage.Store)(nil)
	_ Store = (*sqlitestore.Store)(nil)
)

// Driver reports which driver this run uses. It reads the environment once
// per call, which is cheap and keeps the harness stateless.
func Driver() string {
	if d := os.Getenv(EnvDriver); d != "" {
		return d
	}
	// A DSN with no explicit driver is an existing PostgreSQL job: honour it
	// rather than silently running a different database than it asked for.
	if os.Getenv(EnvDSN) != "" {
		return DriverPostgres
	}
	return DriverSQLite
}

// announce prints the driver once per test binary.
//
// Without it a run is silent about which database it used, and the two
// commands a person types to cover both drivers differ by one environment
// variable. Running the second one without the first is indistinguishable
// from running the first one twice -- both print the same passing output --
// so "green on both drivers" is a claim nothing in the output can support.
// That is not hypothetical: it happened during the audit that added this.
var announce sync.Once

// New creates a throwaway database, opens a migrated store on it, and returns
// the store plus a driver-neutral raw connection to the same database.
//
// The Raw handle is for the rows the Store API cannot write and the columns a
// test asserts on directly; see raw.go for what it does and does not do. Its
// DSN is the PostgreSQL connection string, empty on SQLite, and belongs only
// to tests that have called RequiresPostgres. Cleanup disposes of the
// database when the test finishes.
func New(t *testing.T) (Store, *Raw) {
	t.Helper()

	driver := Driver()
	announce.Do(func() {
		fmt.Fprintf(os.Stderr, "testdb: running against %s\n", driver)
	})

	switch driver {
	case DriverSQLite:
		return newSQLite(t)
	case DriverPostgres:
		return newPostgres(t)
	default:
		t.Fatalf("testdb: unknown %s %q, want %q or %q", EnvDriver, driver, DriverSQLite, DriverPostgres)
		return nil, nil
	}
}

// newSQLite opens a home-mode database in the test's own temporary directory.
// There is nothing to provision and nothing to drop: the file goes when the
// directory does, and the Close registered here runs before that removal
// because cleanups run in reverse order.
func newSQLite(t *testing.T) (Store, *Raw) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	path := filepath.Join(t.TempDir(), "hamlaneh.db")
	store, err := sqlitestore.Open(ctx, path)
	if err != nil {
		t.Fatalf("testdb: open sqlite database: %v", err)
	}
	t.Cleanup(store.Close)

	return store, openRaw(t, DriverSQLite, "sqlite", sqlitestore.DSN(path), "")
}

// openRaw opens the harness's own handle on the database the store just
// opened, with the driver's own pragmas so the two see the same rules.
func openRaw(t *testing.T, driver, sqlDriver, connString, dsn string) *Raw {
	t.Helper()

	db, err := sql.Open(sqlDriver, connString)
	if err != nil {
		t.Fatalf("testdb: open raw %s connection: %v", driver, err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("testdb: close raw connection: %v", closeErr)
		}
	})
	return &Raw{db: db, driver: driver, dsn: dsn}
}

// newPostgres creates a scratch database on the server named by EnvDSN and
// opens a migrated store on it. Tests are skipped loudly when the DSN is
// unset, which is what an unconfigured PostgreSQL leg is.
func newPostgres(t *testing.T) (Store, *Raw) {
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

	return store, openRaw(t, DriverPostgres, "pgx", dsn, dsn)
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
