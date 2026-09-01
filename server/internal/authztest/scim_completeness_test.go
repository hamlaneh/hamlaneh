package authztest

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/scim"
)

// scimContractPath locates the SCIM contract relative to this package.
const scimContractPath = "../../../docs/api/scim.md"

// Markers around the machine-readable operation table (scim.md §6).
const (
	scimTableBegin = "<!-- scim-operations:begin -->"
	scimTableEnd   = "<!-- scim-operations:end -->"
)

// scimTableColumns is the table's fixed shape: op, method, path, authz.
const scimTableColumns = 4

// loadSCIMDocRows parses the operation table out of scim.md. Anything
// structurally wrong — missing markers, a row of the wrong width, a
// duplicate op, an empty table — fails loudly rather than quietly yielding
// fewer rows for the gate to check. It reuses ws_completeness_test.go's cell
// splitter, because one parser for two documents is what keeps a table
// nobody can read from passing quietly in either of them.
func loadSCIMDocRows(t *testing.T) []SCIMRow {
	t.Helper()

	raw, err := os.ReadFile(scimContractPath)
	if err != nil {
		t.Fatalf("read %s: %v", scimContractPath, err)
	}

	_, afterBegin, found := strings.Cut(string(raw), scimTableBegin)
	if !found {
		t.Fatalf("%s has no %s marker", scimContractPath, scimTableBegin)
	}
	table, _, found := strings.Cut(afterBegin, scimTableEnd)
	if !found {
		t.Fatalf("%s has no %s marker", scimContractPath, scimTableEnd)
	}

	rows := []SCIMRow{}
	seen := map[SCIMOperation]bool{}
	for _, line := range strings.Split(table, "\n") {
		cells, ok := markdownRowCells(line)
		if !ok {
			continue
		}
		if len(cells) != scimTableColumns {
			t.Fatalf("%s: row %q has %d columns, want %d (op, method, path, authz)",
				scimContractPath, line, len(cells), scimTableColumns)
		}
		if cells[0] == "op" && cells[1] == "method" {
			continue // header
		}
		if isSeparatorRow(cells) {
			continue
		}

		op := SCIMOperation(cells[0])
		if seen[op] {
			t.Fatalf("%s: operation %s is listed twice", scimContractPath, op)
		}
		seen[op] = true

		rows = append(rows, SCIMRow{Op: op, Method: cells[1], Path: cells[2], Authz: SCIMAuthz(cells[3])})
	}

	if len(rows) == 0 {
		t.Fatalf("%s: the operation table parsed empty; the parser or the document is broken", scimContractPath)
	}
	return rows
}

// TestSCIMRegistryCoversContract is the SCIM completeness gate, the
// counterpart of TestRegistryCoversSpec for a contract OpenAPI cannot
// express: every operation documented in scim.md §6 must have a registry
// entry, and the registry must not carry entries the document dropped.
func TestSCIMRegistryCoversContract(t *testing.T) {
	t.Parallel()

	missing, extra := DiffSCIMRegistry(loadSCIMDocRows(t), SCIMRegistry())
	if len(missing) > 0 {
		t.Errorf("operations in %s without a SCIM registry entry: %v\n"+
			"every provisioning operation must register its authorization expectations in internal/authztest (CLAUDE.md testing policy)",
			scimContractPath, missing)
	}
	if len(extra) > 0 {
		t.Errorf("SCIM registry entries without a matching row in %s: %v", scimContractPath, extra)
	}
}

// TestSCIMRegistryMatchesContract pins the registry to the document's own
// columns, so an edit that moved a path or quietly relaxed a rule fails the
// build instead of shipping. Covering an operation is not the same as
// covering the operation the document describes.
func TestSCIMRegistryMatchesContract(t *testing.T) {
	t.Parallel()

	documented := map[SCIMOperation]SCIMRow{}
	for _, row := range loadSCIMDocRows(t) {
		documented[row.Op] = row
	}

	for _, e := range SCIMRegistry() {
		want, ok := documented[e.Op]
		if !ok {
			continue // reported by TestSCIMRegistryCoversContract
		}
		if e.Method != want.Method || e.Path != want.Path {
			t.Errorf("%s registers %s %s, but %s documents %s %s",
				e.Op, e.Method, e.Path, scimContractPath, want.Method, want.Path)
		}
		if e.Authz != want.Authz {
			t.Errorf("%s registers authz %q, but %s documents %q", e.Op, e.Authz, scimContractPath, want.Authz)
		}
	}
}

// TestSCIMRegistryEntriesAreComplete pins per-entry hygiene: no duplicates
// and a known authorization rule. An unspecified rule is a forgotten
// decision, and the registry is where it would be forgotten.
func TestSCIMRegistryEntriesAreComplete(t *testing.T) {
	t.Parallel()

	known := map[SCIMAuthz]bool{}
	for _, rule := range SCIMAuthzRules() {
		known[rule] = true
	}

	seen := map[SCIMOperation]bool{}
	for _, e := range SCIMRegistry() {
		if seen[e.Op] {
			t.Errorf("%s registered twice", e.Op)
		}
		seen[e.Op] = true

		if !known[e.Authz] {
			t.Errorf("%s has authz %q, which is not one of %v", e.Op, e.Authz, SCIMAuthzRules())
		}
	}
}

// TestSCIMRegistryMatchesTheMux closes the third side of the triangle. The
// document and the registry agreeing proves nothing if the mux serves
// something else, so this compares the registry against the routes
// internal/scim actually registers — which are built from one table, so a
// route that exists in code and not in that list is impossible rather than
// merely caught.
func TestSCIMRegistryMatchesTheMux(t *testing.T) {
	t.Parallel()

	served := map[string]bool{}
	for _, route := range scim.Routes() {
		served[route.Op+" "+route.String()] = true
	}
	registered := map[string]bool{}
	for _, e := range SCIMRegistry() {
		registered[string(e.Op)+" "+e.Method+" "+e.Path] = true
	}

	for route := range served {
		if !registered[route] {
			t.Errorf("internal/scim serves %q with no SCIM registry entry", route)
		}
	}
	for route := range registered {
		if !served[route] {
			t.Errorf("the SCIM registry declares %q, which internal/scim does not serve", route)
		}
	}
}

// TestDiffSCIMRegistryDetectsRemovals proves the SCIM completeness gate can
// turn red: a registry missing one documented operation must be reported,
// and an entry the document does not know must be reported as extra. This is
// the permanent form of the delete-an-entry-and-watch-it-fail drill.
func TestDiffSCIMRegistryDetectsRemovals(t *testing.T) {
	t.Parallel()

	rows := loadSCIMDocRows(t)
	full := SCIMRegistry()

	dropped := full[0].Op
	missing, _ := DiffSCIMRegistry(rows, full[1:])
	if !slicesContain(missing, dropped.String()) {
		t.Errorf("removing %s from the SCIM registry was not detected; missing = %v", dropped, missing)
	}

	bogus := append(append([]SCIMEntry{}, full...), scimEntry("noSuchOperation", "GET", "/scim/v2/Nowhere"))
	_, extra := DiffSCIMRegistry(rows, bogus)
	if len(extra) != 1 || extra[0] != "noSuchOperation" {
		t.Errorf("bogus SCIM registry entry not reported; extra = %v", extra)
	}
}

// slicesContain reports whether values holds want. sort.SearchStrings needs
// a sorted input, which the diff guarantees.
func slicesContain(values []string, want string) bool {
	i := sort.SearchStrings(values, want)
	return i < len(values) && values[i] == want
}
