package sqlitestore

import (
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestConflictClassification pins the error classification against real SQLite
// errors rather than against strings this test made up.
//
// It matters because the two drivers spell a conflict differently: PostgreSQL
// gives an SQLSTATE plus the constraint NAME, SQLite an extended result code
// plus the offending COLUMN list, nested inside a message that repeats the
// words "constraint failed". Getting the parse wrong would not fail loudly —
// it would quietly turn a 409 into a 500.
func TestConflictClassification(t *testing.T) {
	t.Parallel()

	db := openScratch(t)

	exec(t, db, `CREATE TABLE users (
		id TEXT PRIMARY KEY,
		username TEXT UNIQUE,
		email TEXT UNIQUE,
		scim_user_name TEXT UNIQUE,
		n INTEGER CHECK (n >= 0)
	)`)
	exec(t, db, `CREATE TABLE child (id TEXT PRIMARY KEY, parent TEXT REFERENCES users (id))`)
	exec(t, db, `CREATE TABLE pair (a TEXT, b TEXT)`)
	exec(t, db, `CREATE UNIQUE INDEX pair_key ON pair (a, b)`)
	exec(t, db, `INSERT INTO users (id, username, email, scim_user_name, n) VALUES ('u1', 'alice', 'a@example.test', 'alice@corp.test', 1)`)
	exec(t, db, `INSERT INTO pair (a, b) VALUES ('x', 'y')`)

	t.Run("a duplicate username maps to the username sentinel", func(t *testing.T) {
		t.Parallel()
		err := failing(t, db, `INSERT INTO users (id, username) VALUES ('u2', 'alice')`)
		if !isUniqueViolation(err) {
			t.Fatalf("isUniqueViolation(%v) = false", err)
		}
		if got := mapUserConflict(err); !errors.Is(got, storage.ErrUsernameTaken) {
			t.Errorf("mapUserConflict = %v, want ErrUsernameTaken", got)
		}
	})

	t.Run("a duplicate email maps to the email sentinel", func(t *testing.T) {
		t.Parallel()
		err := failing(t, db, `INSERT INTO users (id, email) VALUES ('u3', 'a@example.test')`)
		if got := mapUserConflict(err); !errors.Is(got, storage.ErrEmailTaken) {
			t.Errorf("mapUserConflict = %v, want ErrEmailTaken", got)
		}
	})

	t.Run("a duplicate SCIM userName maps to the SCIM sentinel", func(t *testing.T) {
		t.Parallel()
		err := failing(t, db, `INSERT INTO users (id, scim_user_name) VALUES ('u4', 'alice@corp.test')`)
		if got := mapUserConflict(err); !errors.Is(got, storage.ErrScimIdentifierTaken) {
			t.Errorf("mapUserConflict = %v, want ErrScimIdentifierTaken", got)
		}
	})

	t.Run("a composite index names every one of its columns", func(t *testing.T) {
		t.Parallel()
		err := failing(t, db, `INSERT INTO pair (a, b) VALUES ('x', 'y')`)
		want := []string{"pair.a", "pair.b"}
		if got := conflictColumns(err); !slices.Equal(got, want) {
			t.Errorf("conflictColumns = %v, want %v", got, want)
		}
	})

	t.Run("a primary key counts as a uniqueness conflict", func(t *testing.T) {
		t.Parallel()
		err := failing(t, db, `INSERT INTO users (id, username) VALUES ('u1', 'bob')`)
		if !isUniqueViolation(err) {
			t.Fatalf("isUniqueViolation(%v) = false", err)
		}
		if !conflictsOn(err, "users.id") {
			t.Errorf("conflictsOn(users.id) = false for %v", err)
		}
	})

	t.Run("the other constraint kinds classify separately", func(t *testing.T) {
		t.Parallel()

		fk := failing(t, db, `INSERT INTO child (id, parent) VALUES ('c1', 'nobody')`)
		if !isForeignKeyViolation(fk) {
			t.Errorf("isForeignKeyViolation(%v) = false", fk)
		}
		if conflictColumns(fk) != nil {
			t.Errorf("conflictColumns on a foreign-key error = %v, want nil", conflictColumns(fk))
		}

		check := failing(t, db, `INSERT INTO users (id, n) VALUES ('u5', -1)`)
		if !isCheckViolation(check) {
			t.Errorf("isCheckViolation(%v) = false", check)
		}
		if isUniqueViolation(check) {
			t.Errorf("isUniqueViolation(%v) = true, want false", check)
		}
	})

	t.Run("a trigger abort classifies as a check violation", func(t *testing.T) {
		t.Parallel()

		// Migration 0017 states the MLS both-or-neither rule as a trigger,
		// because SQLite cannot add a CHECK to an existing table. It must
		// reach callers as the same kind of failure.
		exec(t, db, `CREATE TABLE gated (id TEXT PRIMARY KEY, a INTEGER, b BLOB)`)
		exec(t, db, `CREATE TRIGGER gated_both BEFORE INSERT ON gated
		             WHEN (NEW.a IS NULL) <> (NEW.b IS NULL)
		             BEGIN SELECT RAISE(ABORT, 'CHECK constraint failed: gated_both'); END`)

		err := failing(t, db, `INSERT INTO gated (id, a, b) VALUES ('g1', 1, NULL)`)
		if !isCheckViolation(err) {
			t.Errorf("isCheckViolation(%v) = false", err)
		}
	})
}

// TestNotFoundMapsToTheSharedSentinel keeps both drivers answering a missing
// row with the same error, which is what lets the HTTP layer stay
// dialect-blind.
func TestNotFoundMapsToTheSharedSentinel(t *testing.T) {
	t.Parallel()

	if got := notFound(sql.ErrNoRows); !errors.Is(got, storage.ErrNotFound) {
		t.Errorf("notFound(sql.ErrNoRows) = %v, want storage.ErrNotFound", got)
	}
	other := errors.New("something else")
	if got := notFound(other); !errors.Is(got, other) {
		t.Errorf("notFound(other) = %v, want it unchanged", got)
	}
}

func openScratch(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close scratch database: %v", closeErr)
		}
	})
	// A shared-cache memory database lives only while a connection is open,
	// so the pool must not recycle its way down to none between statements.
	db.SetMaxIdleConns(1)
	return db
}

func exec(t *testing.T, db *sql.DB, query string) {
	t.Helper()

	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// failing runs a statement that must fail and returns the error.
func failing(t *testing.T, db *sql.DB, query string) error {
	t.Helper()

	_, err := db.Exec(query)
	if err == nil {
		t.Fatalf("exec %q: want an error, got none", query)
	}
	return err
}
