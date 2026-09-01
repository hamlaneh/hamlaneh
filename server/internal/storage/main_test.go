package storage_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// postgresOnlyAllowList names every test in this package permitted to skip on
// the SQLite driver. It is checked in beside this file so a change to it is a
// change somebody reviewed.
const postgresOnlyAllowList = "postgres_only_tests.txt"

// TestMain runs the suite and then checks the named-skip gate (ADR 012,
// decision 3): the set of tests that called testdb.RequiresPostgres during
// this run must equal the allow-list, in both directions.
//
// It runs on BOTH legs of the driver matrix, which is the point. On SQLite it
// catches a test that started skipping without being listed; on PostgreSQL —
// where nothing skips but everything still registers — it catches a listed
// test that no longer needs to be, so the list cannot rot into a permanent
// exemption nobody rechecks.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := testdb.CheckPostgresOnlyAllowList(postgresOnlyAllowList); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
