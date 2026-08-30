package sqlitestore

import (
	"database/sql"
	"fmt"
	"time"
)

// timeLayout is the ONE on-disk encoding for every timestamp this driver
// writes, and the only one it knows how to read.
//
// Three properties are being bought, and all three break if a single write
// uses a different layout:
//
//   - Fixed width. Lexicographic comparison of these strings is chronological
//     comparison, which is what lets keyset pagination compare (created_at, id)
//     tuples as plain column comparisons the way the PostgreSQL driver does.
//     A value with three fractional digits and one with six do NOT compare
//     correctly against each other ("…123Z" > "…123456Z", because 'Z' > '4'),
//     so the fraction is always exactly six digits.
//   - UTC, always. SQLite has no time zone type; an offset in the text would
//     make two equal instants compare unequal.
//   - Microsecond resolution, matching PostgreSQL's timestamptz exactly, so a
//     value round-trips identically on both drivers and equality assertions in
//     the shared test suite mean the same thing.
//
// sqlNow below is the same layout produced inside SQLite, for the handful of
// column defaults in the migration tree.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// sqlNow renders the current time in timeLayout from inside SQLite. strftime's
// %f yields SS.SSS (millisecond resolution), so the trailing 000 pads it to
// the six fractional digits timeLayout requires.
//
// It is only ever a column DEFAULT. Every write this package performs binds
// its timestamp from Go instead (Store.clock), because the audit chain, the
// session TTLs and the search snippets all need the value the caller sees.
const sqlNow = `strftime('%Y-%m-%dT%H:%M:%f000Z','now')`

// negInfinity stands in for PostgreSQL's '-infinity'::timestamptz, which
// storage/channels.go coalesces a missing read position to so the unread
// count needs no NULL guard. Any real timestamp sorts after it: the year
// field is zero and the layout is fixed width.
const negInfinity = "0000-01-01T00:00:00.000000Z"

// clock returns the time a write should record. Tests pin Store.now; nothing
// else does.
func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// nowText is the bound parameter every write uses where the PostgreSQL driver
// writes now().
func (s *Store) nowText() string {
	return asTime(s.clock())
}

// asTime encodes a timestamp for binding.
func asTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// asNullTime encodes an optional timestamp for binding; nil becomes SQL NULL.
func asNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return asTime(*t)
}

// parseTime decodes a stored timestamp. Reading a value this package did not
// write is a bug, not a supported input, so the error names the offending
// text rather than guessing at another layout.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// timeScan scans a NOT NULL timestamp column into dst.
//
// It exists because database/sql's own time handling is not available here:
// the driver hands TEXT back as a string (modernc's _texttotime is off, which
// is what keeps this encoding entirely ours), so the decode has to be
// attached to the destination.
type timeScan struct{ dst *time.Time }

// Scan implements sql.Scanner.
func (t timeScan) Scan(src any) error {
	switch v := src.(type) {
	case string:
		parsed, err := parseTime(v)
		if err != nil {
			return err
		}
		*t.dst = parsed
		return nil
	case []byte:
		parsed, err := parseTime(string(v))
		if err != nil {
			return err
		}
		*t.dst = parsed
		return nil
	case time.Time:
		*t.dst = v.UTC()
		return nil
	default:
		return fmt.Errorf("scan timestamp: unexpected %T", src)
	}
}

// nullTimeScan scans a nullable timestamp column into dst, which is set to
// nil for SQL NULL.
type nullTimeScan struct{ dst **time.Time }

// Scan implements sql.Scanner.
func (t nullTimeScan) Scan(src any) error {
	if src == nil {
		*t.dst = nil
		return nil
	}
	var parsed time.Time
	if err := (timeScan{dst: &parsed}).Scan(src); err != nil {
		return err
	}
	*t.dst = &parsed
	return nil
}

// zeroTimeScan scans a nullable timestamp into a plain time.Time, leaving it
// zero for SQL NULL. It is for the columns the domain types model as a
// non-pointer that simply has no value yet.
type zeroTimeScan struct{ dst *time.Time }

// Scan implements sql.Scanner.
func (t zeroTimeScan) Scan(src any) error {
	if src == nil {
		*t.dst = time.Time{}
		return nil
	}
	return (timeScan{dst: t.dst}).Scan(src)
}

// boolValue binds a Go bool the way the schema stores one: SQLite has no
// boolean type, and every boolean column in the migration tree is INTEGER
// carrying 0 or 1.
func boolValue(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nullString binds an optional string; nil becomes SQL NULL.
func nullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// stringPtr decodes a nullable text column into *string.
func stringPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

// int64Ptr decodes a nullable integer column into *int64.
func int64Ptr(ni sql.NullInt64) *int64 {
	if !ni.Valid {
		return nil
	}
	v := ni.Int64
	return &v
}

// nullBytes binds an optional blob; a nil slice becomes SQL NULL rather than
// an empty blob, because the schema tells the two apart.
func nullBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

// compile-time proof that the scanners satisfy the interface they exist for.
var (
	_ sql.Scanner = timeScan{}
	_ sql.Scanner = nullTimeScan{}
	_ sql.Scanner = zeroTimeScan{}
)
