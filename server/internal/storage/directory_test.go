package storage_test

import (
	"context"
	"slices"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// seedDirectoryUser creates one directory fixture: a username, and the display
// name the filter searches alongside it.
func seedDirectoryUser(ctx context.Context, t *testing.T, store testdb.Store,
	username, displayName string,
) {
	t.Helper()

	nu := newUser(username)
	nu.DisplayName = displayName
	mustCreateUser(ctx, t, store, nu)
}

// mustListDirectory reads one page of the directory.
func mustListDirectory(ctx context.Context, t *testing.T, store testdb.Store,
	params storage.ListDirectoryParams,
) []storage.User {
	t.Helper()

	users, err := store.ListDirectory(ctx, params)
	if err != nil {
		t.Fatalf("ListDirectory(%+v): %v", params, err)
	}
	return users
}

// TestListDirectoryOrderIntegration pins the order the pickers read in and the
// keyset paging that walks it.
//
// The fixture set's case-insensitive order is deliberately NOT its byte order:
// username is citext, so both the ORDER BY and the cursor's (username, id) >
// comparison fold case, and a page boundary is exactly where a disagreement
// between the two would silently drop a row instead of failing.
func TestListDirectoryOrderIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	// Byte order would be Alpha, CHARLIE, bravo, delta — uppercase first.
	// Case-insensitively it is Alpha, bravo, CHARLIE, delta. Created in a
	// third order, so neither insertion order nor byte order can pass for the
	// right answer.
	for _, name := range []string{"delta", "CHARLIE", "Alpha", "bravo"} {
		seedDirectoryUser(ctx, t, store, name, "")
	}
	want := []string{"Alpha", "bravo", "CHARLIE", "delta"}

	t.Run("orders by username, case-insensitively", func(t *testing.T) {
		got := usernamesOf(mustListDirectory(ctx, t, store, storage.ListDirectoryParams{Limit: 10}))
		if !slices.Equal(got, want) {
			t.Errorf("directory order = %v, want %v", got, want)
		}
	})

	t.Run("keyset paging crosses a case boundary without losing a row", func(t *testing.T) {
		// Page size 2 puts the boundary between "bravo" and "CHARLIE": a
		// case-sensitive > would place CHARLIE before the cursor and skip it.
		var walked []string
		var after *storage.DirectoryCursor
		for pages := 0; ; pages++ {
			if pages > len(want) {
				t.Fatal("pagination never terminates")
			}
			page := mustListDirectory(ctx, t, store,
				storage.ListDirectoryParams{After: after, Limit: 2})
			if len(page) == 0 {
				break
			}
			walked = append(walked, usernamesOf(page)...)
			last := page[len(page)-1]
			after = &storage.DirectoryCursor{Username: last.Username, UserID: last.ID}
		}
		if !slices.Equal(walked, want) {
			t.Errorf("keyset walk = %v, want %v", walked, want)
		}
	})

	t.Run("paging past the last row is a clean empty page", func(t *testing.T) {
		all := mustListDirectory(ctx, t, store, storage.ListDirectoryParams{Limit: 10})
		last := all[len(all)-1]

		page := mustListDirectory(ctx, t, store, storage.ListDirectoryParams{
			After: &storage.DirectoryCursor{Username: last.Username, UserID: last.ID},
			Limit: 10,
		})
		if len(page) != 0 {
			t.Errorf("page after the last row = %v, want empty", usernamesOf(page))
		}
	})
}

// TestListDirectoryFilterIntegration pins what the picker's search box matches.
//
// The three metacharacter cases are the reason escapeLike exists: %, _ and \
// are LIKE syntax, and a filter that reached the query unescaped would answer a
// search for "100%" with the whole directory. Each carries a decoy row that
// matches only if that character was treated as syntax.
func TestListDirectoryFilterIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	// Needles land in exactly one field: "ami" is inside a username only,
	// "ilv" inside a display name only.
	for _, u := range []struct{ username, displayName string }{
		{"sarah.amini", "Sarah A."},
		{"bob", "Roberto Silva"},
		{"pct.literal", "100% cotton"},
		{"pct.decoy", "1000 threads"},
		{"und.literal", "a_b marker"},
		{"und.decoy", "axb marker"},
		{"esc.literal", `c\d marker`},
		{"esc.decoy", "cd marker"},
	} {
		seedDirectoryUser(ctx, t, store, u.username, u.displayName)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"matches the middle of a username", "ami", []string{"sarah.amini"}},
		{"matches the middle of a display name", "ilv", []string{"bob"}},
		{"is case-insensitive on the username", "AMI", []string{"sarah.amini"}},
		{"is case-insensitive on the display name", "ROBERTO", []string{"bob"}},
		{"percent is a literal percent, not a wildcard", "100%", []string{"pct.literal"}},
		{"a lone percent does not match everybody", "%", []string{"pct.literal"}},
		{"underscore is a literal underscore, not a wildcard", "a_b", []string{"und.literal"}},
		{"a lone underscore does not match any single character", "_", []string{"und.literal"}},
		{"backslash is a literal backslash, not an escape", `c\d`, []string{"esc.literal"}},
		{"a needle nobody carries matches nothing", "no-such-person", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usernamesOf(mustListDirectory(ctx, t, store,
				storage.ListDirectoryParams{Query: tt.query, Limit: 50}))
			if !slices.Equal(got, tt.want) {
				t.Errorf("filter %q matched %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
