package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestUpdateCurrentUserValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"unknown locale", `{"locale":"de"}`},
		{"empty locale", `{"locale":""}`},
		{"malformed body", `{`},
		// display_name is not on this request. The decoder drops unknown
		// members, which is this codebase's contract everywhere, so sending
		// one is a no-op rather than a refusal — pinned in TestUpdateCurrentUser.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The store is deliberately unwired: a rejected request must not
			// reach it, and errFakeUnwired would surface as a 500 if it did.
			store := authedStore(fixtureUser())
			rec := do(t, store, request(http.MethodPatch, "/api/v1/users/me", tt.body,
				withSessionCookie("tok"), withCSRF()))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestUpdateCurrentUser(t *testing.T) {
	t.Parallel()

	// patchUser returns the patch storage received, plus the response, for a
	// body sent by fixtureUser.
	patchUser := func(t *testing.T, body string) (storage.UserProfileUpdate, *httptest.ResponseRecorder) {
		t.Helper()
		var got storage.UserProfileUpdate
		var gotID uuid.UUID
		store := authedStore(fixtureUser())
		store.updateUserProfile = func(_ context.Context, userID uuid.UUID, upd storage.UserProfileUpdate) (storage.User, error) {
			got, gotID = upd, userID
			u := fixtureUser()
			if upd.Locale != nil {
				u.Locale = *upd.Locale
			}
			if upd.DisplayName != nil {
				u.DisplayName = *upd.DisplayName
			}
			return u, nil
		}
		rec := do(t, store, request(http.MethodPatch, "/api/v1/users/me", body,
			withSessionCookie("tok"), withCSRF()))
		if gotID != fixtureUser().ID {
			t.Errorf("patched user %s, want the caller %s", gotID, fixtureUser().ID)
		}
		return got, rec
	}

	t.Run("a locale patch names the locale and nothing else", func(t *testing.T) {
		t.Parallel()
		got, rec := patchUser(t, `{"locale":"fa"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Locale == nil || *got.Locale != "fa" {
			t.Errorf("storage got locale %v, want fa", got.Locale)
		}
		// The field the request never mentioned stays nil all the way down:
		// that is what keeps a language change from blanking a display name.
		if got.DisplayName != nil {
			t.Errorf("storage got display_name %q for a locale-only patch", *got.DisplayName)
		}

		var updated api.User
		if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
			t.Fatalf("body is not the contract User shape: %v", err)
		}
		if updated.Locale != api.UserLocaleFa {
			t.Errorf("response locale = %q, want fa", updated.Locale)
		}
	})

	t.Run("a display_name in the body is ignored, not applied", func(t *testing.T) {
		t.Parallel()
		// The request carries locale and nothing else. The decoder drops
		// unknown members rather than refusing them — the whole codebase
		// works that way — so the risk worth pinning is not a 400, it is a
		// name silently reaching storage from a field the contract removed.
		got, rec := patchUser(t, `{"locale":"fa","display_name":"someone else"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got.DisplayName != nil {
			t.Errorf("storage got display_name %q from a field the request does not carry", *got.DisplayName)
		}
		if got.Locale == nil || *got.Locale != "fa" {
			t.Errorf("storage got locale %v, want fa", got.Locale)
		}
	})

	t.Run("an empty patch changes nothing", func(t *testing.T) {
		t.Parallel()
		got, rec := patchUser(t, `{}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		if got.Locale != nil || got.DisplayName != nil {
			t.Errorf("empty patch reached storage as %+v, want every field nil", got)
		}
	})

	t.Run("storage failure is a 500, not a partial success", func(t *testing.T) {
		t.Parallel()
		store := authedStore(fixtureUser())
		store.updateUserProfile = func(context.Context, uuid.UUID, storage.UserProfileUpdate) (storage.User, error) {
			return storage.User{}, errors.New("connection reset")
		}
		rec := do(t, store, request(http.MethodPatch, "/api/v1/users/me", `{"locale":"fa"}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusInternalServerError, "internal_error")
	})
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
	var page1 api.AdminUserPage
	if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
		t.Fatalf("first page is not an AdminUserPage: %v", err)
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
	var page2 api.AdminUserPage
	if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
		t.Fatalf("second page is not an AdminUserPage: %v", err)
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
		// PostgreSQL cannot store a NUL at all, so without this the row is
		// refused inside the driver and the caller gets a 500 for what is
		// plainly a bad request. A newline is refused for a different reason:
		// the name gets one line wherever it is shown.
		{"display name carries a NUL", `{"username":"newuser","password":"a valid password","display_name":"Ali\u0000Reza"}`},
		{"display name spans two lines", `{"username":"newuser","password":"a valid password","display_name":"Ali\nReza"}`},
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

// TestAdminUserListCarriesTheColumnsTheTableDraws is a contract regression.
//
// The list first answered with the reduced User shape, which carries neither
// is_active nor has_totp — while the row action beside it answered AdminUser,
// which carries both. So the dashboard's users table could not render its own
// active/inactive column from its own list endpoint: it learned a user's
// state only for the row it had just changed. The shapes disagreed, and the
// screen was the thing caught in between.
func TestAdminUserListCarriesTheColumnsTheTableDraws(t *testing.T) {
	t.Parallel()

	deactivated := fixtureUser()
	deactivated.ID = uuid.MustParse("bbbbbbbb-0000-4000-8000-00000000000b")
	deactivated.Username = "offboarded"
	deactivated.IsActive = false

	store := adminStore()
	store.listUsers = func(context.Context, storage.ListUsersParams) ([]storage.User, error) {
		return []storage.User{deactivated}, nil
	}

	rec := doHandler(t, httpserver.Handler(store),
		request(http.MethodGet, "/api/v1/admin/users", "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d (body %s)", rec.Code, rec.Body.String())
	}

	// Asserted on the encoded key set, not a decoded struct: decoding into
	// AdminUser would happily succeed against a payload that omitted the
	// field entirely, which is exactly the bug.
	var raw struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not a page of objects: %v", err)
	}
	if len(raw.Users) != 1 {
		t.Fatalf("got %d rows, want 1", len(raw.Users))
	}
	row := raw.Users[0]
	for _, key := range []string{"is_active", "is_admin", "must_change_password"} {
		if _, ok := row[key]; !ok {
			t.Errorf("the row carries no %q; the table draws a column from it", key)
		}
	}
	if row["is_active"] != false {
		t.Errorf("is_active = %v for a deactivated account, want false", row["is_active"])
	}
}
