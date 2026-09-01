package sqlitestore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestMsgUUIDListIsPlaceholdersOnly is the check that keeps the `#nosec G202`
// suppressions on this package's IN-list queries honest.
//
// Five statements concatenate msgUUIDList's first return value straight into
// SQL. That is safe for exactly one reason: the string is a function of the
// SLICE LENGTH and of nothing else, so no caller-supplied byte can reach the
// statement text. If someone ever changes it to interpolate the ids — the
// obvious "simplification" — the suppressions would silently become real SQL
// injection. This fails first.
func TestMsgUUIDListIsPlaceholdersOnly(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 2, 3, 10} {
		ids := make([]uuid.UUID, n)
		for i := range ids {
			ids[i] = uuid.New()
		}

		list, args := msgUUIDList(ids)

		if len(args) != n {
			t.Errorf("n=%d: got %d args, want %d", n, len(args), n)
		}
		want := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
		if list != want {
			t.Errorf("n=%d: got %q, want %q", n, list, want)
		}
		// The real assertion: the statement text carries no data. Anything
		// outside the placeholder alphabet means a value leaked into SQL.
		if strings.Trim(list, "?, ") != "" {
			t.Errorf("n=%d: list %q contains something other than placeholders", n, list)
		}
		for _, id := range ids {
			if strings.Contains(list, id.String()) {
				t.Fatalf("n=%d: id %s was interpolated into the SQL text", n, id)
			}
		}
	}
}

// TestPositionCaseIsPlaceholdersAndIndices covers the other operand
// claimAttachments concatenates: a CASE whose arms are placeholders and whose
// results are loop indices. Same argument, same reason to pin it.
func TestPositionCaseIsPlaceholdersAndIndices(t *testing.T) {
	t.Parallel()

	if got, want := positionCase(0), "CASE id END"; got != want {
		t.Errorf("positionCase(0) = %q, want %q", got, want)
	}
	if got, want := positionCase(3), "CASE id WHEN ? THEN 0 WHEN ? THEN 1 WHEN ? THEN 2 END"; got != want {
		t.Errorf("positionCase(3) = %q, want %q", got, want)
	}
}
