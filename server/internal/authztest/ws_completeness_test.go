package authztest

import (
	"os"
	"strings"
	"testing"
)

// wsProtocolPath locates the WebSocket contract relative to this package.
const wsProtocolPath = "../../../docs/api/ws-protocol.md"

// Markers around the machine-readable operation table (ws-protocol.md §10).
const (
	wsTableBegin = "<!-- ws-operations:begin -->"
	wsTableEnd   = "<!-- ws-operations:end -->"
)

// wsTableColumns is the table's fixed shape: op, direction, scope, authz.
const wsTableColumns = 4

// loadWSDocRows parses the operation table out of ws-protocol.md. Anything
// structurally wrong — missing markers, a row of the wrong width, an unknown
// direction, a duplicate key, an empty table — fails loudly rather than
// quietly yielding fewer rows for the gate to check.
func loadWSDocRows(t *testing.T) []WSRow {
	t.Helper()

	raw, err := os.ReadFile(wsProtocolPath)
	if err != nil {
		t.Fatalf("read %s: %v", wsProtocolPath, err)
	}

	_, afterBegin, found := strings.Cut(string(raw), wsTableBegin)
	if !found {
		t.Fatalf("%s has no %s marker", wsProtocolPath, wsTableBegin)
	}
	table, _, found := strings.Cut(afterBegin, wsTableEnd)
	if !found {
		t.Fatalf("%s has no %s marker", wsProtocolPath, wsTableEnd)
	}

	rows := []WSRow{}
	seen := map[WSOperation]bool{}
	for _, line := range strings.Split(table, "\n") {
		cells, ok := markdownRowCells(line)
		if !ok {
			continue
		}
		if len(cells) != wsTableColumns {
			t.Fatalf("%s: row %q has %d columns, want %d (op, direction, scope, authz)",
				wsProtocolPath, line, len(cells), wsTableColumns)
		}
		if cells[0] == "op" && cells[1] == "direction" {
			continue // header
		}
		if isSeparatorRow(cells) {
			continue
		}

		direction := WSDirection(cells[1])
		if direction != C2S && direction != S2C {
			t.Fatalf("%s: row %q has direction %q, want %q or %q",
				wsProtocolPath, line, cells[1], C2S, S2C)
		}
		op := WSOperation{Op: cells[0], Direction: direction}
		if seen[op] {
			t.Fatalf("%s: operation %s is listed twice", wsProtocolPath, op)
		}
		seen[op] = true

		rows = append(rows, WSRow{Op: op, Scope: cells[2], Authz: WSAuthz(cells[3])})
	}

	if len(rows) == 0 {
		t.Fatalf("%s: the operation table parsed empty; the parser or the document is broken", wsProtocolPath)
	}
	return rows
}

// markdownRowCells splits one pipe-delimited table line into trimmed cells.
// ok is false for anything that is not a table row.
func markdownRowCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") || len(line) < 2 {
		return nil, false
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.Trim(strings.TrimSpace(part), "`"))
	}
	return cells, true
}

// isSeparatorRow reports whether every cell is a markdown alignment rule.
func isSeparatorRow(cells []string) bool {
	for _, cell := range cells {
		if cell == "" || strings.Trim(cell, "-:") != "" {
			return false
		}
	}
	return true
}

// TestWSRegistryCoversProtocol is the WebSocket completeness gate, the
// counterpart of TestRegistryCoversSpec for a contract OpenAPI cannot
// express: every operation documented in ws-protocol.md must have a registry
// entry, and the registry must not carry entries the document dropped.
func TestWSRegistryCoversProtocol(t *testing.T) {
	t.Parallel()

	missing, extra := DiffWSRegistry(loadWSDocRows(t), WSRegistry())
	if len(missing) > 0 {
		t.Errorf("operations in %s without a WS registry entry: %v\n"+
			"every WebSocket operation must register its authorization expectations in internal/authztest (CLAUDE.md testing policy)",
			wsProtocolPath, missing)
	}
	if len(extra) > 0 {
		t.Errorf("WS registry entries without a matching row in %s: %v", wsProtocolPath, extra)
	}
}

// TestWSRegistryMatchesDocumentedAuthz pins the registry to the document's
// authz column, so an edit that quietly relaxes a rule (member → session on
// a channel event, say) fails the build instead of shipping.
func TestWSRegistryMatchesDocumentedAuthz(t *testing.T) {
	t.Parallel()

	documented := map[WSOperation]WSAuthz{}
	for _, row := range loadWSDocRows(t) {
		documented[row.Op] = row.Authz
	}

	for _, e := range WSRegistry() {
		want, ok := documented[e.Op]
		if !ok {
			continue // reported by TestWSRegistryCoversProtocol
		}
		if e.Authz != want {
			t.Errorf("%s registers authz %q, but %s documents %q", e.Op, e.Authz, wsProtocolPath, want)
		}
	}
}

// TestWSRegistryEntriesAreComplete pins per-entry hygiene: no duplicates, a
// known authorization rule, and an explicit status.
func TestWSRegistryEntriesAreComplete(t *testing.T) {
	t.Parallel()

	known := map[WSAuthz]bool{}
	for _, rule := range WSAuthzRules() {
		known[rule] = true
	}

	seen := map[WSOperation]bool{}
	for _, e := range WSRegistry() {
		if seen[e.Op] {
			t.Errorf("%s registered twice", e.Op)
		}
		seen[e.Op] = true

		if !known[e.Authz] {
			t.Errorf("%s has authz %q, which is not one of %v", e.Op, e.Authz, WSAuthzRules())
		}
		if e.Status == WSStatusUnspecified {
			t.Errorf("%s has no status; say whether its rule is enforced yet", e.Op)
		}
	}
}

// TestDiffWSRegistryDetectsRemovals proves the WS completeness gate can turn
// red: a registry missing one documented operation must be reported, and an
// entry the document does not know must be reported as extra. This is the
// permanent form of the delete-an-entry-and-watch-it-fail drill.
func TestDiffWSRegistryDetectsRemovals(t *testing.T) {
	t.Parallel()

	rows := loadWSDocRows(t)
	full := WSRegistry()

	truncated := full[1:]
	dropped := full[0].Op
	missing, _ := DiffWSRegistry(rows, truncated)
	found := false
	for _, m := range missing {
		if m == dropped.String() {
			found = true
		}
	}
	if !found {
		t.Errorf("removing %s from the WS registry was not detected; missing = %v", dropped, missing)
	}

	bogus := append(append([]WSEntry{}, full...), wsStub("no_such_frame", S2C, WSSession))
	_, extra := DiffWSRegistry(rows, bogus)
	if len(extra) != 1 || extra[0] != "s2c no_such_frame" {
		t.Errorf("bogus WS registry entry not reported; extra = %v", extra)
	}
}
