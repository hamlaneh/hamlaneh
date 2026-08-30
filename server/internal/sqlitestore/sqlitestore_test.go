package sqlitestore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
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

// TestConnectionPragmas pins the four settings the driver's whole
// single-writer argument rests on. Each of them is off or wrong by default in
// SQLite, and each failure mode is silent:
//
//   - journal_mode WAL: without it a read is refused while a write is in
//     flight, so a sidebar load fails whenever a message is being sent.
//   - foreign_keys on: SQLite defaults them OFF, per connection. The schema's
//     ON DELETE RESTRICT / CASCADE / SET NULL policy is a correctness rule,
//     and without this pragma it is decoration.
//   - busy_timeout: a writer that finds the lock held fails instantly instead
//     of waiting, turning ordinary contention into a 500.
//   - synchronous NORMAL: the safe setting under WAL. This assertion is here
//     to catch a change to OFF, which can corrupt the database.
func TestConnectionPragmas(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hamlaneh.db")
	store, err := sqlitestore.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	db := openRaw(t, path)

	for _, tc := range []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // 1 is NORMAL
	} {
		var got string
		if err := db.QueryRowContext(t.Context(), "PRAGMA "+tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}

// TestForeignKeysAreEnforced proves the pragma above is not merely reported
// on but in force: a message whose channel does not exist must be refused.
func TestForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hamlaneh.db")
	store, err := sqlitestore.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	db := openRaw(t, path)
	_, err = db.ExecContext(t.Context(),
		`INSERT INTO messages (id, channel_id, author_id, client_msg_id, content, created_at)
		 VALUES ('1', 'no-such-channel', 'no-such-author', 'k', 'hi', '2026-01-01T00:00:00.000000Z')`)
	if err == nil {
		t.Fatal("inserted a message into a channel that does not exist; foreign keys are not enforced")
	}
}

// TestConcurrentWritersAllCommit is the test behind _txlock=immediate.
//
// SQLite's default (deferred) transaction starts read-only and upgrades at
// its first write. Two of those can both hold read locks and then both ask to
// upgrade, which is a deadlock SQLITE_BUSY reports and no busy timeout
// resolves — and it would surface as an intermittent 500 under exactly the
// load a household generates when two people act at once. Taking the write
// lock at BEGIN makes the second writer wait instead.
//
// So: many concurrent writers through the ordinary API, every one of which
// must commit. A failure here is not a slow test; it is the pragma being
// wrong.
func TestConcurrentWritersAllCommit(t *testing.T) {
	t.Parallel()

	store, err := sqlitestore.Open(t.Context(), filepath.Join(t.TempDir(), "hamlaneh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	const writers = 16
	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})

	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = store.CreateUser(t.Context(), storage.NewUser{
				Username:     fmt.Sprintf("writer%02d", i),
				PasswordHash: "argon2id$fixture",
				Locale:       "en",
			})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}

	got, err := store.CountUsers(t.Context())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if got != writers {
		t.Errorf("CountUsers = %d, want %d", got, writers)
	}
}

// TestCitextCollationGovernsUsernames pins the Go collation that stands in
// for PostgreSQL's citext (collation.go). Three things rest on it and all
// three are asserted here: the unique index refuses a username that differs
// only in case, a lookup finds an account whatever case it is asked in, and
// the fold reaches beyond ASCII — which SQLite's built-in NOCASE does not,
// and which is the whole reason the collation is written in Go.
func TestCitextCollationGovernsUsernames(t *testing.T) {
	t.Parallel()

	store, err := sqlitestore.Open(t.Context(), filepath.Join(t.TempDir(), "hamlaneh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	created, err := store.CreateUser(t.Context(), storage.NewUser{
		Username: "Alice", PasswordHash: "argon2id$fixture", Locale: "en",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := store.CreateUser(t.Context(), storage.NewUser{
		Username: "alice", PasswordHash: "argon2id$fixture", Locale: "en",
	}); !errors.Is(err, storage.ErrUsernameTaken) {
		t.Errorf("second CreateUser with a case variant = %v, want ErrUsernameTaken", err)
	}

	found, err := store.UserByIdentifier(t.Context(), "ALICE")
	if err != nil {
		t.Fatalf("UserByIdentifier in a different case: %v", err)
	}
	if found.ID != created.ID {
		t.Errorf("UserByIdentifier found %s, want %s", found.ID, created.ID)
	}

	// Non-ASCII: the emails an account may carry are not confined to ASCII,
	// and SQLite's NOCASE folds only A-Z. É and é must be one address.
	upper := "ÉLODIE@example.test"
	lower := "élodie@example.test"
	if _, err := store.CreateUser(t.Context(), storage.NewUser{
		Username: "elodie", Email: &upper, PasswordHash: "argon2id$fixture", Locale: "en",
	}); err != nil {
		t.Fatalf("CreateUser with a non-ASCII email: %v", err)
	}
	if _, err := store.CreateUser(t.Context(), storage.NewUser{
		Username: "elodie2", Email: &lower, PasswordHash: "argon2id$fixture", Locale: "en",
	}); !errors.Is(err, storage.ErrEmailTaken) {
		t.Errorf("CreateUser with the same email in another case = %v, want ErrEmailTaken", err)
	}
}

// TestSchemaShape is the SQLite counterpart of the PostgreSQL suite's
// information_schema assertions (ADR 012: ported to PRAGMA table_info where
// the assertion is about the schema). It reads the users table's columns back
// out of the database that the migration tree just built, so a migration that
// forgets a column fails here rather than at the first scan error.
func TestSchemaShape(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hamlaneh.db")
	store, err := sqlitestore.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	db := openRaw(t, path)
	rows, err := db.QueryContext(t.Context(),
		`SELECT name FROM pragma_table_info('users') ORDER BY name`)
	if err != nil {
		t.Fatalf("read users columns: %v", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("close rows: %v", closeErr)
		}
	}()

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
		"scim_external_id", "scim_user_name",
		"updated_at", "username",
	}
	if len(got) != len(want) {
		t.Fatalf("users columns:\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("users columns:\n got %v\nwant %v", got, want)
		}
	}
}

// openRaw opens a second connection on the same file, with the driver's own
// pragmas, for the assertions that read the schema back.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", sqlitestore.DSN(path))
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close raw connection: %v", closeErr)
		}
	})
	return db
}
