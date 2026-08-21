package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

func TestGetCurrentUser(t *testing.T) {
	t.Parallel()

	email := "member@example.com"
	user := fixtureUser()
	user.Email = &email
	user.MustChangePassword = true

	rec := do(t, authedStore(user), request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var got api.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract User shape: %v", err)
	}
	if got.Username != "member" {
		t.Errorf("username = %q, want member", got.Username)
	}
	if got.Email == nil || string(*got.Email) != email {
		t.Errorf("email = %v, want %q", got.Email, email)
	}
	if !got.MustChangePassword {
		t.Error("must_change_password not surfaced")
	}
	if strings.Contains(rec.Body.String(), "argon2id") {
		t.Error("response leaked the password hash")
	}
}

func adminStore() *fakeStore {
	admin := fixtureUser()
	admin.IsAdmin = true
	return authedStore(admin)
}

func TestAdminListUsersValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{"limit zero", "?limit=0"},
		{"limit over max", "?limit=101"},
		{"limit negative", "?limit=-5"},
		{"cursor not base64", "?cursor=%21%21%21"},
		{"cursor without separator", "?cursor=" + url.QueryEscape("bm9zZXBhcmF0b3I")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := adminStore()
			rec := do(t, store, request(http.MethodGet, "/api/v1/admin/users"+tt.query, "", withSessionCookie("tok")))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestAdminListUsersPagination(t *testing.T) {
	t.Parallel()

	// Three stored users; page size 2 → first page carries a next_cursor
	// encoding user 2's keyset position, second page (After = user 2) has no
	// cursor.
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	all := make([]storage.User, 3)
	for i := range all {
		all[i] = fixtureUser()
		all[i].ID = uuid.MustParse(fmt.Sprintf("%08d-0000-0000-0000-000000000000", i+1))
		all[i].Username = "user" + string(rune('a'+i))
		all[i].CreatedAt = base.Add(time.Duration(i) * time.Second)
	}

	store := adminStore()
	var gotParams []storage.ListUsersParams
	store.listUsers = func(_ context.Context, params storage.ListUsersParams) ([]storage.User, error) {
		gotParams = append(gotParams, params)
		start := 0
		if params.After != nil {
			for i, u := range all {
				if u.ID == params.After.ID {
					start = i + 1
				}
			}
		}
		end := min(start+params.Limit, len(all))
		return all[start:end], nil
	}
	handler := httpserver.Handler(store)

	first := doHandler(t, handler, request(http.MethodGet, "/api/v1/admin/users?limit=2", "", withSessionCookie("tok")))
	if first.Code != http.StatusOK {
		t.Fatalf("first page: got status %d (body %s)", first.Code, first.Body.String())
	}
	var page1 api.UserPage
	if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
		t.Fatalf("first page is not a UserPage: %v", err)
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
		"/api/v1/admin/users?limit=2&cursor="+url.QueryEscape(*page1.NextCursor), "", withSessionCookie("tok")))
	if second.Code != http.StatusOK {
		t.Fatalf("second page: got status %d (body %s)", second.Code, second.Body.String())
	}
	var page2 api.UserPage
	if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
		t.Fatalf("second page is not a UserPage: %v", err)
	}
	if len(page2.Users) != 1 {
		t.Fatalf("second page has %d users, want 1", len(page2.Users))
	}
	if page2.NextCursor != nil {
		t.Error("second page has a next_cursor although the set is exhausted")
	}

	// The cursor decoded to exactly the keyset position of the last row of
	// page one.
	if gotParams[1].After == nil {
		t.Fatal("second call reached storage without a cursor")
	}
	if gotParams[1].After.ID != all[1].ID || !gotParams[1].After.CreatedAt.Equal(all[1].CreatedAt) {
		t.Errorf("cursor decoded to (%v, %s), want (%v, %s)",
			gotParams[1].After.CreatedAt, gotParams[1].After.ID, all[1].CreatedAt, all[1].ID)
	}

	seen := map[string]bool{}
	for _, u := range append(page1.Users, page2.Users...) {
		if seen[u.Username] {
			t.Errorf("user %s appeared on two pages", u.Username)
		}
		seen[u.Username] = true
	}
	if len(seen) != 3 {
		t.Errorf("pagination walked %d distinct users, want 3", len(seen))
	}
}

func TestAdminCreateUserValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"empty body object", `{}`},
		{"username too short", `{"username":"ab","password":"a valid password"}`},
		{"username too long", `{"username":"` + strings.Repeat("a", 33) + `","password":"a valid password"}`},
		{"username uppercase", `{"username":"NotLower","password":"a valid password"}`},
		{"username starts with dash", `{"username":"-dash","password":"a valid password"}`},
		{"username has spaces", `{"username":"has space","password":"a valid password"}`},
		{"password too short", `{"username":"newuser","password":"elevenchars"}`},
		{"password too long", `{"username":"newuser","password":"` + strings.Repeat("a", 1025) + `"}`},
		{"invalid email", `{"username":"newuser","password":"a valid password","email":"not-an-email"}`},
		{"display name too long", `{"username":"newuser","password":"a valid password","display_name":"` + strings.Repeat("d", 121) + `"}`},
		{"invalid locale", `{"username":"newuser","password":"a valid password","locale":"de"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := adminStore()
			rec := do(t, store, request(http.MethodPost, "/api/v1/admin/users", tt.body,
				withSessionCookie("tok"), withCSRF()))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestAdminCreateUser(t *testing.T) {
	t.Parallel()

	t.Run("created user must change password and gets defaults", func(t *testing.T) {
		t.Parallel()
		store := adminStore()
		var created storage.NewUser
		store.createUser = func(_ context.Context, nu storage.NewUser) (storage.User, error) {
			created = nu
			u := fixtureUser()
			u.Username = nu.Username
			u.Email = nu.Email
			u.DisplayName = nu.DisplayName
			u.Locale = nu.Locale
			u.IsAdmin = nu.IsAdmin
			u.MustChangePassword = nu.MustChangePassword
			return u, nil
		}

		body := `{"username":"newuser","password":"an initial password","email":"new@example.com"}`
		rec := do(t, store, request(http.MethodPost, "/api/v1/admin/users", body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}

		if !created.MustChangePassword {
			t.Error("created user does not have must_change_password set")
		}
		if created.IsAdmin {
			t.Error("is_admin defaulted to true")
		}
		if created.Locale != "en" {
			t.Errorf("locale defaulted to %q, want en", created.Locale)
		}
		if !strings.HasPrefix(created.PasswordHash, "$argon2id$") {
			t.Errorf("stored hash %q is not argon2id", created.PasswordHash)
		}
		if created.PasswordHash == "an initial password" {
			t.Error("password stored in plaintext")
		}

		var got api.User
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not the contract User shape: %v", err)
		}
		if !got.MustChangePassword {
			t.Error("response does not surface must_change_password")
		}
	})

	t.Run("duplicate username is 409 username_taken", func(t *testing.T) {
		t.Parallel()
		store := adminStore()
		store.createUser = func(context.Context, storage.NewUser) (storage.User, error) {
			return storage.User{}, storage.ErrUsernameTaken
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/admin/users",
			`{"username":"taken","password":"an initial password"}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "username_taken")
	})

	t.Run("duplicate email is 409 email_taken", func(t *testing.T) {
		t.Parallel()
		store := adminStore()
		store.createUser = func(context.Context, storage.NewUser) (storage.User, error) {
			return storage.User{}, storage.ErrEmailTaken
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/admin/users",
			`{"username":"newuser","password":"an initial password","email":"taken@example.com"}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusConflict, "email_taken")
	})
}
