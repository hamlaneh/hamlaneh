// Package sqlitestore is the home-mode storage driver: the same contract the
// PostgreSQL driver in internal/storage serves, implemented over a single
// SQLite file so one downloadable binary runs the whole stack on one machine
// (ADR 012, PLAN §4).
//
// It is a second implementation, not a portable dialect. internal/storage
// keeps every PostgreSQL-specific choice it made for server mode — row-lock
// strengths, advisory locks, pg_trgm, arrays, LATERAL — because those were
// chosen for the deployment that carries real load. Nothing here asks that
// driver to give any of them up.
//
// # The single writer, and why the machinery is not missing
//
// Almost everything internal/storage's lock-order essay is about — FOR NO KEY
// UPDATE on the channels row, FOR UPDATE SKIP LOCKED on key packages,
// pg_advisory_xact_lock on the audit chain and the admin set — exists to
// serialize concurrent writers that PostgreSQL would otherwise let run
// simultaneously. SQLite has no concurrent writers to serialize: one write
// transaction holds the database's write lock, and every other write waits.
// Under that rule the same outcomes hold with no lock clause at all:
//
//   - The last-member race cannot happen: two removals of one channel cannot
//     overlap, so a count-then-delete is correct without locking the channel
//     row. What is lost is the PostgreSQL-specific concurrency SHAPE (there,
//     a removal deliberately never queues behind an adder); here everything
//     queues, briefly, at household scale.
//   - A key-package claim cannot double-spend: the delete-returning statement
//     runs alone. SKIP LOCKED's never-wait property is gone; its outcome —
//     one claim, one honest empty answer, no deadlock — is kept.
//   - The audit chain's and the last-admin rule's advisory locks vanish: they
//     are what a single writer does by existing.
//
// Every method that drops such a clause says so at its own site, naming what
// the PostgreSQL driver does there and why the outcome matches. Those
// comments are the ones a future reader needs most, so they are not
// summarised away into this header.
//
// # Deliberate divergences, all of them
//
//   - Timestamps are TEXT in one fixed-width UTC encoding (codec.go), so
//     lexicographic order is chronological and keyset pagination compares
//     columns directly. PostgreSQL's '-infinity' becomes a sentinel string
//     that sorts before every real timestamp.
//   - UUIDs are TEXT in canonical lowercase form. Hexadecimal is monotonic,
//     so text ordering matches PostgreSQL's byte ordering of the uuid type —
//     which the DM pair canonicalization (dm_user_a < dm_user_b) depends on.
//   - citext becomes the CITEXT collation this package registers in Go
//     (collation.go), so case-insensitivity is defined by our code and is
//     identical on every operating system.
//   - The audit chain's GENERATED ALWAYS AS IDENTITY becomes INTEGER PRIMARY
//     KEY AUTOINCREMENT, which SQLite promises is monotonic and never reused.
//   - Search keeps migration 0006's semantics exactly and implements them as
//     a scan (search.go names the ceiling and the upgrade path).
//   - Conflicts are classified from SQLite's extended result codes and the
//     column list in the message, where the PostgreSQL driver reads pgerrcode
//     plus a constraint name (errors.go).
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	// The CGO-free SQLite translation. CGO_ENABLED=0 is the deciding
	// property: home mode ships as static binaries for Windows, macOS and
	// Linux, and a cgo SQLite would turn "cross-compile three targets" into
	// "maintain three C toolchains" (ADR 012).
	_ "modernc.org/sqlite"
)

// Store is a handle to a home-mode database: a SQLite connection pool plus
// the schema version this binary expects. It serves the same consumer
// interfaces *storage.Store does (httpserver.Store, bootstrap.Store, and the
// narrow ones the scim, passwordreset and linkpreview packages define).
type Store struct {
	db          *sql.DB
	wantVersion uint64

	// path is the database file, kept for error messages only.
	path string

	// now is the clock every write reads. It is a field so tests can pin it;
	// production leaves it nil and gets time.Now.
	//
	// The PostgreSQL driver takes its timestamps from the database's own
	// now(), so application clock skew cannot stretch a token's life. In home
	// mode the application and the database are one process on one machine,
	// so there is exactly one clock and this IS that guarantee.
	now func() time.Time
}

// Open opens (creating if necessary) the SQLite database at path and brings
// the schema up to date. The caller owns the returned Store and must Close it.
//
// There is no retry loop and no readiness ping: the database is a file this
// process opens, not a service that may still be starting.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlitestore: no database path configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path %q: %w", path, err)
	}

	wantVersion, err := latestMigrationVersion(migrationFiles, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("embedded migrations: %w", err)
	}

	connString := dsn(abs)
	if migErr := runMigrations(connString); migErr != nil {
		return nil, migErr
	}

	db, err := sql.Open("sqlite", connString)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", abs, err)
	}

	// One connection, which is what makes the single-writer story exact
	// rather than approximate.
	//
	// The busy timeout in the DSN converts contention into a wait, but
	// SQLite's busy handler is not fair: it backs off and retries, so under
	// many simultaneous writers a particular one can lose every race and time
	// out with SQLITE_BUSY while the database was never busy for long. That
	// is not a hypothetical — it is what the authorization matrix produced,
	// hundreds of parallel cases against one file, and it would reach a
	// household as an occasional 500 with no cause a user could see.
	//
	// database/sql's own queue IS fair: with a single connection every
	// statement waits its turn in order and no caller can be starved. The
	// busy timeout stays as the backstop for the one writer this process does
	// not own — a backup tool, or a second instance somebody started by
	// mistake.
	//
	// ponytail: reads queue behind writes too. At household scale that is
	// microseconds and buys the fairness above; if a home instance ever
	// measures a read waiting on a write, the upgrade is the standard split —
	// a read pool of several connections beside this one-connection write
	// pool — which needs every method classified read or write first.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("open database %q: %w", abs, err), db.Close())
	}

	return &Store{db: db, wantVersion: wantVersion, path: abs}, nil
}

// DSN builds the connection string for the database at path.
//
// It is exported so the shared test harness (internal/testdb) can open its
// raw connection with exactly these pragmas — a raw handle with foreign keys
// off, or with a deferred transaction lock, would behave differently from the
// driver it is meant to be inspecting.
func DSN(path string) string { return dsn(path) }

// dsn builds the connection string. Every parameter here is load-bearing and
// none of them is a default we could rely on:
//
//   - _journal_mode=WAL: readers never block the writer and the writer never
//     blocks readers. Without it a sidebar read can be refused while a message
//     is being sent. WAL is a persistent property of the file, set on every
//     open so a database copied from elsewhere still gets it.
//   - _txlock=immediate: a write transaction takes the write lock at BEGIN
//     rather than at its first write. The default (deferred) starts read-only
//     and upgrades, and two transactions that both upgrade get SQLITE_BUSY
//     that no busy timeout can resolve — the classic SQLite deadlock, and the
//     one this driver's whole single-writer argument would otherwise rest on
//     top of. Every transaction this package opens is a write transaction.
//   - _busy_timeout=5000: a writer waits up to five seconds for the write
//     lock instead of failing instantly. Five seconds is far beyond any
//     statement this package runs and far below any HTTP timeout above it, so
//     it converts contention into a short wait and never into a hung request.
//   - _foreign_keys=on: SQLite defaults foreign keys OFF, per connection. The
//     schema's ON DELETE RESTRICT / CASCADE / SET NULL policy (migration 0003)
//     is a correctness rule, not decoration, so this is not optional.
//   - _synchronous=normal: the safe setting under WAL — a crash cannot corrupt
//     the database, only lose the last transactions that had not checkpointed.
//     FULL costs an fsync per commit for a durability margin a household chat
//     does not need; OFF is not on the table because it can corrupt.
func dsn(path string) string {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_txlock", "immediate")
	q.Set("_busy_timeout", "5000")
	q.Set("_foreign_keys", "on")
	q.Set("_synchronous", "normal")
	return "file:" + path + "?" + q.Encode()
}

// Close releases the connection pool.
func (s *Store) Close() {
	if err := s.db.Close(); err != nil {
		// Matching *storage.Store.Close, which returns nothing either: a
		// close failure at shutdown has no caller who could act on it.
		_ = err
	}
}

// Ready reports whether the database answers and its schema matches the
// migrations embedded in this binary. It backs the /readyz probe.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	var version uint64
	var dirty bool
	row := s.db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations")
	if err := row.Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty (a migration failed)", version)
	}
	if version != s.wantVersion {
		return fmt.Errorf("schema version is %d, binary expects %d", version, s.wantVersion)
	}
	return nil
}
