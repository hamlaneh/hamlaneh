package testdb

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The named-skip gate (ADR 012, decision 3).
//
// Almost every storage test is a test of the CONTRACT — what a method
// returns, which sentinel a conflict maps to, what outcome holds under
// concurrency — and those run, and must pass, on both drivers. A handful are
// tests of PostgreSQL MECHANISM: that a removal does not queue behind an add,
// that pgx registers the citext type, that information_schema reports a
// particular shape. Single-writer SQLite makes some of those false by design
// and the rest meaningless.
//
// Pretending they were interface tests — by skipping them silently, or by
// contorting them until they pass vacuously — is the dishonest shape this
// gate exists to prevent. So a skip is declared out loud with a reason, it is
// COUNTED, and the count is diffed against a checked-in allow-list in both
// directions: a test that starts skipping without being listed fails the
// build, and a listed test that no longer skips fails it too. A new
// PostgreSQL-only test is thereby a reviewed, named decision, never a silent
// erosion of the matrix.

var postgresOnly = struct {
	mu    sync.Mutex
	seen  map[string]string
	dirty bool
}{seen: map[string]string{}}

// RequiresPostgres skips the calling test unless the run is on the PostgreSQL
// driver, and records that it did so.
//
// reason says what PostgreSQL mechanism the test asserts, in a few words; it
// appears in the skip message and is what a reviewer reads when the
// allow-list changes. It is required.
//
// Call it FIRST in the test, before New: New itself skips when the PostgreSQL
// leg has no DSN configured, and a registration that happens after that skip
// would leave the allow-list looking stale.
func RequiresPostgres(t *testing.T, reason string) {
	t.Helper()

	if strings.TrimSpace(reason) == "" {
		t.Fatal("testdb.RequiresPostgres: a reason is required — it is what the allow-list review reads")
	}

	postgresOnly.mu.Lock()
	postgresOnly.seen[t.Name()] = reason
	postgresOnly.mu.Unlock()

	if driver := Driver(); driver != DriverPostgres {
		t.Skipf("SKIPPING on the %s driver (PostgreSQL mechanism): %s", driver, reason)
	}
}

// CheckPostgresOnlyAllowList compares the tests that called RequiresPostgres
// during this run against the allow-list at path, and reports any difference
// in either direction. A package's TestMain calls it after m.Run:
//
//	func TestMain(m *testing.M) {
//	    code := m.Run()
//	    if err := testdb.CheckPostgresOnlyAllowList("postgres_only_tests.txt"); err != nil {
//	        fmt.Fprintln(os.Stderr, err)
//	        code = 1
//	    }
//	    os.Exit(code)
//	}
//
// The allow-list is one test name per line; blank lines and lines beginning
// with # are ignored. Names are exactly what t.Name() reports, so a subtest
// is written Parent/subtest.
//
// The comparison is skipped when the run was filtered (-run) or was only
// listing tests, because the registry is then incomplete by construction and
// failing on it would make every focused run a false alarm.
func CheckPostgresOnlyAllowList(path string) error {
	if runWasFiltered() {
		return nil
	}

	want, err := readAllowList(path)
	if err != nil {
		return err
	}

	postgresOnly.mu.Lock()
	got := make([]string, 0, len(postgresOnly.seen))
	reasons := make(map[string]string, len(postgresOnly.seen))
	for name, reason := range postgresOnly.seen {
		got = append(got, name)
		reasons[name] = reason
	}
	postgresOnly.mu.Unlock()

	sort.Strings(got)

	var unlisted, stale []string
	for _, name := range got {
		if !slices.Contains(want, name) {
			unlisted = append(unlisted, fmt.Sprintf("  + %s  (%s)", name, reasons[name]))
		}
	}
	for _, name := range want {
		if _, ok := reasons[name]; !ok {
			stale = append(stale, "  - "+name)
		}
	}
	if len(unlisted) == 0 && len(stale) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s does not match the tests that called testdb.RequiresPostgres.\n", path)
	if len(unlisted) > 0 {
		b.WriteString("These tests skip on SQLite but are not in the allow-list.\n")
		b.WriteString("Add them only if they really assert PostgreSQL mechanism rather than contract:\n")
		b.WriteString(strings.Join(unlisted, "\n") + "\n")
	}
	if len(stale) > 0 {
		b.WriteString("These are in the allow-list but no longer skip.\n")
		b.WriteString("Delete the lines — the matrix just got wider, which is the good direction:\n")
		b.WriteString(strings.Join(stale, "\n") + "\n")
	}
	return fmt.Errorf("%s", b.String())
}

// readAllowList parses the checked-in file.
func readAllowList(path string) ([]string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a test-only path a TestMain passes in
	if err != nil {
		return nil, fmt.Errorf("read the PostgreSQL-only allow-list %s: %w", path, err)
	}

	names := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, line)
	}
	sort.Strings(names)
	return names, nil
}

// runWasFiltered reports whether this run saw only part of the package's
// tests, which makes the registry incomplete and the comparison meaningless.
func runWasFiltered() bool {
	for _, name := range []string{"test.run", "test.list", "test.skip"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return true
		}
	}
	return false
}
