package httpserver_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// directoryUser is a stored user carrying everything the directory must never
// publish: an address, the admin flag, a password change still owed, and the
// hash itself.
func directoryUser(n int, username string) storage.User {
	email := username + "@example.com"
	return storage.User{
		ID:                 uuid.MustParse(fmt.Sprintf("%08d-0000-0000-0000-000000000000", n)),
		Username:           username,
		Email:              &email,
		DisplayName:        "Display " + username,
		PasswordHash:       "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		Locale:             "en",
		IsAdmin:            true,
		MustChangePassword: true,
	}
}

// TestListUsersServesUserSummariesOnly is the leak test. The stored rows carry
// an email, the admin flag, a pending password change and an argon2id hash;
// the contract's UserSummary carries none of them, and the assertion is on the
// key set of the encoded JSON rather than on a decoded struct — decoding into
// api.UserSummary would silently drop any extra field instead of failing.
func TestListUsersServesUserSummariesOnly(t *testing.T) {
	t.Parallel()

	store := authedStore(fixtureUser())
	store.listDirectory = func(context.Context, storage.ListDirectoryParams) ([]storage.User, error) {
		return []storage.User{directoryUser(1, "alice"), directoryUser(2, "bob")}, nil
	}

	rec := do(t, store, request(http.MethodGet, "/api/v1/users", "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var raw struct {
		Users      []map[string]any `json:"users"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not a page of objects: %v", err)
	}
	if len(raw.Users) != 2 {
		t.Fatalf("page has %d users, want 2 (body %s)", len(raw.Users), rec.Body.String())
	}
	if raw.NextCursor != nil {
		t.Errorf("next_cursor = %q although the directory is exhausted", *raw.NextCursor)
	}

	for _, got := range raw.Users {
		for key := range got {
			switch key {
			case "id", "username", "display_name":
			default:
				t.Errorf("directory row carries %q; UserSummary is id, username and display_name only", key)
			}
		}
	}
	for _, secret := range []string{"email", "is_admin", "must_change_password", "locale", "argon2id", "example.com"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response body contains %q: %s", secret, rec.Body.String())
		}
	}

	// And it is the contract shape, not merely a small one.
	var page api.UserSummaryPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not the contract UserSummaryPage shape: %v", err)
	}
	if page.Users[0].Username != "alice" || page.Users[0].DisplayName != "Display alice" {
		t.Errorf("first row = %+v, want alice", page.Users[0])
	}
}

// TestListUsersValidation pins every way a request is refused before storage
// is asked anything. The store has no directory wired, so a check that failed
// to fire would answer 500 rather than quietly pass — which is also the
// malformed-cursor case: the contract says 400, and a cursor that reached the
// decoder's error path unhandled would be a 500.
func TestListUsersValidation(t *testing.T) {
	t.Parallel()

	cursor := func(raw string) string {
		return url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(raw)))
	}

	tests := []struct {
		name  string
		query string
	}{
		{"limit zero", "?limit=0"},
		{"limit over max", "?limit=101"},
		{"limit negative", "?limit=-5"},
		{"empty filter", "?q="},
		{"filter over max length", "?q=" + strings.Repeat("a", 65)},
		{"cursor not base64", "?cursor=%21%21%21"},
		{"cursor without separator", "?cursor=" + cursor("noseparator")},
		{"cursor id is not a uuid", "?cursor=" + cursor("alice|not-a-uuid")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, authedStore(fixtureUser()),
				request(http.MethodGet, "/api/v1/users"+tt.query, "", withSessionCookie("tok")))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestListUsersPagination walks the directory through its own cursor: the
// filter survives the page boundary, the cursor decodes to exactly the keyset
// position of the last row served, and no user is served twice or skipped.
func TestListUsersPagination(t *testing.T) {
	t.Parallel()

	all := []storage.User{
		directoryUser(1, "alice"), directoryUser(2, "amir"), directoryUser(3, "amos"),
	}

	store := authedStore(fixtureUser())
	var gotParams []storage.ListDirectoryParams
	store.listDirectory = func(_ context.Context, params storage.ListDirectoryParams) ([]storage.User, error) {
		gotParams = append(gotParams, params)
		start := 0
		if params.After != nil {
			for i, u := range all {
				if u.ID == params.After.UserID {
					start = i + 1
				}
			}
		}
		return all[start:min(start+params.Limit, len(all))], nil
	}
	handler := httpserver.Handler(store)

	first := doHandler(t, handler,
		request(http.MethodGet, "/api/v1/users?q=am&limit=2", "", withSessionCookie("tok")))
	if first.Code != http.StatusOK {
		t.Fatalf("first page: got status %d (body %s)", first.Code, first.Body.String())
	}
	var page1 api.UserSummaryPage
	if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
		t.Fatalf("first page is not a UserSummaryPage: %v", err)
	}
	if len(page1.Users) != 2 {
		t.Fatalf("first page has %d users, want 2", len(page1.Users))
	}
	if page1.NextCursor == nil {
		t.Fatal("first page has no next_cursor although a third user exists")
	}
	if gotParams[0].Limit != 3 {
		t.Errorf("storage asked for limit %d, want limit+1 = 3", gotParams[0].Limit)
	}

	second := doHandler(t, handler, request(http.MethodGet,
		"/api/v1/users?q=am&limit=2&cursor="+url.QueryEscape(*page1.NextCursor), "",
		withSessionCookie("tok")))
	if second.Code != http.StatusOK {
		t.Fatalf("second page: got status %d (body %s)", second.Code, second.Body.String())
	}
	var page2 api.UserSummaryPage
	if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
		t.Fatalf("second page is not a UserSummaryPage: %v", err)
	}
	if len(page2.Users) != 1 {
		t.Fatalf("second page has %d users, want 1", len(page2.Users))
	}
	if page2.NextCursor != nil {
		t.Error("second page has a next_cursor although the directory is exhausted")
	}

	if gotParams[1].After == nil {
		t.Fatal("second call reached storage without a cursor")
	}
	if gotParams[1].After.Username != all[1].Username || gotParams[1].After.UserID != all[1].ID {
		t.Errorf("cursor decoded to (%q, %s), want (%q, %s)",
			gotParams[1].After.Username, gotParams[1].After.UserID, all[1].Username, all[1].ID)
	}
	for i, params := range gotParams {
		if params.Query != "am" {
			t.Errorf("call %d reached storage with filter %q, want am", i, params.Query)
		}
	}

	seen := map[string]bool{}
	for _, u := range append(page1.Users, page2.Users...) {
		if seen[u.Username] {
			t.Errorf("user %s appeared on two pages", u.Username)
		}
		seen[u.Username] = true
	}
	if len(seen) != len(all) {
		t.Errorf("pagination walked %d distinct users, want %d", len(seen), len(all))
	}
}
