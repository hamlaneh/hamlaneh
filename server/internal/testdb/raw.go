package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the PostgreSQL leg
	_ "modernc.org/sqlite"             // database/sql driver for the SQLite leg

	"github.com/hamlaneh/hamlaneh/server/internal/sqlitestore"
)

// Raw is a driver-neutral connection for the rows a test needs that the Store
// API cannot write — a message with an explicit created_at, a tampered audit
// entry, a soft-deleted row — and for reading back the columns a test asserts
// on directly.
//
// It exists so that a test which needs one of those does not thereby become a
// PostgreSQL test. Before it, every such test held a *pgx.Conn and could only
// ever run on one driver; the storage suite's raw fixtures were more than half
// of it, and half a suite skipped on SQLite would have made the driver matrix
// a formality.
//
// It is NOT a query builder and NOT an abstraction over SQL. It is one
// connection, one dialect subset, and two small conversions:
//
//   - Placeholders are ?, positional. The PostgreSQL side rebinds them to $n.
//   - A time.Time argument is encoded the way each driver stores a timestamp,
//     and a *time.Time scan destination is decoded the same way.
//   - A bool argument becomes 0/1 on SQLite, which has no boolean type.
//
// Everything else is the test's own SQL, and it has to be SQL both engines
// parse. Where it cannot be — information_schema, pg_trgm, citext casts,
// least()/greatest(), bool_and() — the test is asserting something about
// PostgreSQL itself, and RequiresPostgres plus a line in the allow-list is
// the honest answer rather than a wider harness.
type Raw struct {
	db     *sql.DB
	driver string
	dsn    string
}

// DSN is the PostgreSQL connection string this scratch database lives at, and
// is empty on SQLite. Only a test that has called RequiresPostgres has any
// business with it — it is here for the handful that drive pgx directly.
func (r *Raw) DSN() string { return r.dsn }

// Driver reports which driver this Raw talks to, for the rare assertion that
// legitimately differs (a column type, say) while the rest of the test does
// not.
func (r *Raw) Driver() string { return r.driver }

// Exec runs a statement and fails the test if it errors.
func (r *Raw) Exec(ctx context.Context, t *testing.T, query string, args ...any) {
	t.Helper()

	if _, err := r.db.ExecContext(ctx, r.rebind(query), r.convertArgs(args)...); err != nil {
		t.Fatalf("raw exec %q: %v", query, err)
	}
}

// ExecErr runs a statement and returns its error, for the tests that assert a
// constraint refuses something.
func (r *Raw) ExecErr(ctx context.Context, query string, args ...any) error {
	_, err := r.db.ExecContext(ctx, r.rebind(query), r.convertArgs(args)...)
	return err
}

// QueryRow reads one row. Scan accepts the same destinations on both drivers:
// a *time.Time or **time.Time is decoded from whichever encoding the driver
// stores.
func (r *Raw) QueryRow(ctx context.Context, query string, args ...any) *RawRow {
	return &RawRow{
		row:    r.db.QueryRowContext(ctx, r.rebind(query), r.convertArgs(args)...),
		driver: r.driver,
	}
}

// Query reads many rows, with the same scan rules as QueryRow.
func (r *Raw) Query(ctx context.Context, t *testing.T, query string, args ...any) *RawRows {
	t.Helper()

	rows, err := r.db.QueryContext(ctx, r.rebind(query), r.convertArgs(args)...)
	if err != nil {
		t.Fatalf("raw query %q: %v", query, err)
	}
	t.Cleanup(func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Errorf("close raw rows: %v", closeErr)
		}
	})
	return &RawRows{rows: rows, driver: r.driver}
}

// RawRow is one row of a Raw read.
type RawRow struct {
	row    *sql.Row
	driver string
}

// Scan reads the row into dest, converting timestamps per driver.
func (r *RawRow) Scan(dest ...any) error {
	return r.row.Scan(convertDests(r.driver, dest)...)
}

// RawRows is a Raw iteration.
type RawRows struct {
	rows   *sql.Rows
	driver string
}

// Next advances to the next row.
func (r *RawRows) Next() bool { return r.rows.Next() }

// Scan reads the current row into dest, converting timestamps per driver.
func (r *RawRows) Scan(dest ...any) error {
	return r.rows.Scan(convertDests(r.driver, dest)...)
}

// Err reports an iteration failure.
func (r *RawRows) Err() error { return r.rows.Err() }

// rebind turns the ? placeholders this harness speaks into the $n PostgreSQL
// wants. It is a positional walk, not a parser: a ? inside a string literal
// would be rewritten too. No test needs one, and a harness that parsed SQL to
// find out would be exactly the abstraction this type refuses to be.
func (r *Raw) rebind(query string) string {
	if r.driver != DriverPostgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// convertArgs encodes the two Go types the drivers store differently.
func (r *Raw) convertArgs(args []any) []any {
	if r.driver != DriverSQLite {
		return args
	}
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case time.Time:
			out[i] = v.UTC().Format(sqlitestore.TimeLayout)
		case *time.Time:
			if v == nil {
				out[i] = nil
				continue
			}
			out[i] = v.UTC().Format(sqlitestore.TimeLayout)
		case bool:
			// SQLite has no boolean type; every boolean column in the tree is
			// INTEGER holding 0 or 1.
			if v {
				out[i] = 1
			} else {
				out[i] = 0
			}
		default:
			out[i] = a
		}
	}
	return out
}

// convertDests swaps a timestamp destination for one that knows the driver's
// encoding. Everything else is passed through — booleans, blobs, uuids and
// numbers scan identically on both.
func convertDests(driver string, dest []any) []any {
	if driver != DriverSQLite {
		return dest
	}
	out := make([]any, len(dest))
	for i, d := range dest {
		switch v := d.(type) {
		case *time.Time:
			out[i] = rawTimeScan{dst: v}
		case **time.Time:
			out[i] = rawNullTimeScan{dst: v}
		default:
			out[i] = d
		}
	}
	return out
}

// rawTimeScan decodes a stored timestamp on the SQLite leg.
type rawTimeScan struct{ dst *time.Time }

// Scan implements sql.Scanner.
func (s rawTimeScan) Scan(src any) error {
	var text string
	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	case time.Time:
		*s.dst = v.UTC()
		return nil
	case nil:
		return errors.New("scan timestamp: unexpected NULL")
	default:
		return fmt.Errorf("scan timestamp: unexpected %T", src)
	}
	t, err := time.Parse(sqlitestore.TimeLayout, text)
	if err != nil {
		return fmt.Errorf("scan timestamp %q: %w", text, err)
	}
	*s.dst = t.UTC()
	return nil
}

// rawNullTimeScan decodes a nullable stored timestamp on the SQLite leg.
type rawNullTimeScan struct{ dst **time.Time }

// Scan implements sql.Scanner.
func (s rawNullTimeScan) Scan(src any) error {
	if src == nil {
		*s.dst = nil
		return nil
	}
	var t time.Time
	if err := (rawTimeScan{dst: &t}).Scan(src); err != nil {
		return err
	}
	*s.dst = &t
	return nil
}
