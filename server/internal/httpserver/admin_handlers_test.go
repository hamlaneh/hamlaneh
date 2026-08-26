package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// adminID is the fixture admin's id, and the one an admin patching
// themselves names.
var adminID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// adminAPI builds an admin-authenticated request against the admin routes.
func adminAPI(method, path, body string) *http.Request {
	return request(method, path, body, withSessionCookie("tok"), withCSRF())
}

func TestUpdateUserAdminRefusesSelfDeactivation(t *testing.T) {
	t.Parallel()

	store := adminStore()
	// Wired so a call that got through would visibly succeed; the point is
	// that it never gets through.
	store.updateUserAdmin = func(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
		t.Error("the store was asked to deactivate the caller's own account")
		return storage.User{}, nil
	}

	rec := do(t, store, adminAPI(http.MethodPatch,
		"/api/v1/admin/users/"+adminID.String(), `{"is_active":false}`))
	wantError(t, rec, http.StatusConflict, "self_deactivation")
}

func TestUpdateUserAdminAllowsSelfDemotionToReachTheStore(t *testing.T) {
	t.Parallel()

	// Demoting yourself is not refused here — it is refused only when you
	// are the LAST admin, which is a fact about the whole instance and is
	// decided under a lock in the store.
	store := adminStore()
	asked := false
	store.updateUserAdmin = func(_ context.Context, id uuid.UUID, upd storage.AdminUserUpdate) (storage.User, error) {
		asked = true
		if id != adminID || upd.IsAdmin == nil || *upd.IsAdmin {
			t.Errorf("store asked for %s / %+v", id, upd)
		}
		return storage.User{}, storage.ErrLastAdmin
	}

	rec := do(t, store, adminAPI(http.MethodPatch,
		"/api/v1/admin/users/"+adminID.String(), `{"is_admin":false}`))
	if !asked {
		t.Error("the handler decided the last-admin question without asking the store")
	}
	wantError(t, rec, http.StatusConflict, "last_admin")
}

func TestUpdateUserAdminAnswers(t *testing.T) {
	t.Parallel()

	target := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tests := []struct {
		name       string
		body       string
		storeUser  storage.User
		storeErr   error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "no field named",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown user",
			body:       `{"is_admin":true}`,
			storeErr:   storage.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   "user_not_found",
		},
		{
			name:       "last admin",
			body:       `{"is_admin":false}`,
			storeErr:   storage.ErrLastAdmin,
			wantStatus: http.StatusConflict,
			wantCode:   "last_admin",
		},
		{
			name: "deactivated",
			body: `{"is_active":false}`,
			storeUser: storage.User{
				ID: target, Username: "leaver", DisplayName: "Leaver",
				PasswordHash: "argon2id$secret", IsActive: false,
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := adminStore()
			store.updateUserAdmin = func(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
				return tt.storeUser, tt.storeErr
			}

			rec := do(t, store, adminAPI(http.MethodPatch,
				"/api/v1/admin/users/"+target.String(), tt.body))
			if tt.wantCode != "" {
				wantError(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("got status %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var got api.AdminUser
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not the contract AdminUser shape: %v", err)
			}
			if got.IsActive {
				t.Error("is_active is true on a deactivated account")
			}
			if strings.Contains(rec.Body.String(), "argon2id") {
				t.Error("response leaked the password hash")
			}
		})
	}
}

func TestForcePasswordResetShowsThePasswordOnce(t *testing.T) {
	t.Parallel()

	var storedHash string
	store := adminStore()
	store.setTemporaryPassword = func(_ context.Context, _ uuid.UUID, hash string) (storage.User, error) {
		storedHash = hash
		return storage.User{Username: "forgetful"}, nil
	}

	rec := do(t, store, adminAPI(http.MethodPost,
		"/api/v1/admin/users/44444444-4444-4444-4444-444444444444/reset-password", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var got api.TemporaryCredentials
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract TemporaryCredentials shape: %v", err)
	}
	if got.Username != "forgetful" {
		t.Errorf("username = %q", got.Username)
	}
	if len(got.TemporaryPassword) < 12 {
		t.Errorf("temporary password %q is shorter than the account policy allows", got.TemporaryPassword)
	}
	// What was stored is the hash of what was shown — so the response is the
	// only place this password will ever exist in the clear.
	if storedHash == got.TemporaryPassword {
		t.Fatal("the temporary password was stored in the clear")
	}
	ok, _, err := password.Verify(got.TemporaryPassword, storedHash)
	if err != nil || !ok {
		t.Errorf("the stored hash does not verify the password that was shown (err %v)", err)
	}
}

func TestForcePasswordResetUnknownUser(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.setTemporaryPassword = func(context.Context, uuid.UUID, string) (storage.User, error) {
		return storage.User{}, storage.ErrNotFound
	}
	rec := do(t, store, adminAPI(http.MethodPost,
		"/api/v1/admin/users/44444444-4444-4444-4444-444444444444/reset-password", ""))
	wantError(t, rec, http.StatusNotFound, "user_not_found")
}

func TestOrgSettingsSavePerField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantPatch  storage.OrgSettingsPatch
		wantStatus int
		wantCode   string
	}{
		{
			name:      "one field",
			body:      `{"require_totp":true}`,
			wantPatch: storage.OrgSettingsPatch{RequireTotp: boolPtr(true)},
		},
		{
			name:      "name is trimmed",
			body:      `{"org_name":"  Nest  "}`,
			wantPatch: storage.OrgSettingsPatch{OrgName: stringPtr("Nest")},
		},
		{
			name:       "empty name",
			body:       `{"org_name":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown locale",
			body:       `{"default_locale":"de"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown registration mode",
			body:       `{"registration_mode":"anyone"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "session lifetime out of range",
			body:       `{"session_lifetime_hours":0}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got storage.OrgSettingsPatch
			store := adminStore()
			store.updateOrgSettings = func(_ context.Context, patch storage.OrgSettingsPatch) (storage.OrgSettings, error) {
				got = patch
				return storage.OrgSettings{
					OrgName: "Nest", DefaultLocale: "en", RegistrationMode: "invite",
					SessionLifetimeHours: 720, AccountsWithoutTotp: 3,
				}, nil
			}

			rec := do(t, store, adminAPI(http.MethodPatch, "/api/v1/admin/org", tt.body))
			if tt.wantCode != "" {
				wantError(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if !samePatch(got, tt.wantPatch) {
				t.Errorf("patch = %+v, want %+v — only the fields present may be changed", got, tt.wantPatch)
			}
		})
	}
}

func TestGetOrgSettingsReportsTheDerivedCount(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.orgSettings = func(context.Context) (storage.OrgSettings, error) {
		return storage.OrgSettings{
			OrgName: "Nest", DefaultLocale: "fa", RegistrationMode: "open",
			RequireTotp: true, SessionLifetimeHours: 24, AccountsWithoutTotp: 7,
		}, nil
	}

	rec := do(t, store, request(http.MethodGet, "/api/v1/admin/org", "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got api.OrgSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract OrgSettings shape: %v", err)
	}
	if got.AccountsWithoutTotp == nil || *got.AccountsWithoutTotp != 7 {
		t.Errorf("accounts_without_totp = %v, want 7", got.AccountsWithoutTotp)
	}
	if got.RegistrationMode != api.RegistrationModeOpen || got.DefaultLocale != api.OrgSettingsDefaultLocaleFa {
		t.Errorf("settings did not round-trip: %+v", got)
	}
}

// TestAdminActionsAreRecorded pins the actions this slice names, so the
// audit log's vocabulary cannot drift silently from the handlers'.
func TestAdminActionsAreRecorded(t *testing.T) {
	t.Parallel()

	target := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		wire   func(*fakeStore)
		want   []string
	}{
		{
			name: "promotion", method: http.MethodPatch,
			path: "/api/v1/admin/users/" + target.String(), body: `{"is_admin":true}`,
			wire: func(f *fakeStore) {
				f.updateUserAdmin = func(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
					return storage.User{ID: target, Username: "riser", IsAdmin: true, IsActive: true}, nil
				}
			},
			want: []string{"user.promoted"},
		},
		{
			name: "deactivation", method: http.MethodPatch,
			path: "/api/v1/admin/users/" + target.String(), body: `{"is_active":false}`,
			wire: func(f *fakeStore) {
				f.updateUserAdmin = func(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
					return storage.User{ID: target, Username: "leaver"}, nil
				}
			},
			want: []string{"user.deactivated"},
		},
		{
			name: "both at once", method: http.MethodPatch,
			path: "/api/v1/admin/users/" + target.String(), body: `{"is_admin":false,"is_active":true}`,
			wire: func(f *fakeStore) {
				f.updateUserAdmin = func(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
					return storage.User{ID: target, Username: "mixed", IsActive: true}, nil
				}
			},
			want: []string{"user.demoted", "user.reactivated"},
		},
		{
			name: "forced reset", method: http.MethodPost,
			path: "/api/v1/admin/users/" + target.String() + "/reset-password",
			wire: func(f *fakeStore) {
				f.setTemporaryPassword = func(context.Context, uuid.UUID, string) (storage.User, error) {
					return storage.User{ID: target, Username: "forgetful"}, nil
				}
			},
			want: []string{"user.password_reset_forced"},
		},
		{
			name: "settings", method: http.MethodPatch,
			path: "/api/v1/admin/org", body: `{"require_totp":true}`,
			wire: func(f *fakeStore) {
				f.updateOrgSettings = func(context.Context, storage.OrgSettingsPatch) (storage.OrgSettings, error) {
					return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "en", RegistrationMode: "invite", SessionLifetimeHours: 720}, nil
				}
			},
			want: []string{"org.settings_changed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := adminStore()
			tt.wire(store)
			rec := recordingAudit{}
			handler := httpserver.Handler(store, httpserver.WithAudit(&rec))
			handler.ServeHTTP(newRecorder(), adminAPI(tt.method, tt.path, tt.body))

			if got := rec.actions(); !equalStrings(got, tt.want) {
				t.Errorf("recorded %v, want %v", got, tt.want)
			}
			for _, ev := range rec.events {
				if ev.ActorID != adminID {
					t.Errorf("%s recorded actor %s, want the acting admin %s", ev.Action, ev.ActorID, adminID)
				}
			}
		})
	}
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

// samePatch compares two patches by value, not by pointer identity: what
// matters is which fields are present and what they say.
func samePatch(a, b storage.OrgSettingsPatch) bool {
	return sameStringPtr(a.OrgName, b.OrgName) &&
		sameStringPtr(a.DefaultLocale, b.DefaultLocale) &&
		sameStringPtr(a.RegistrationMode, b.RegistrationMode) &&
		sameBoolPtr(a.RequireTotp, b.RequireTotp) &&
		sameIntPtr(a.SessionLifetimeHours, b.SessionLifetimeHours)
}

func sameStringPtr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func sameBoolPtr(a, b *bool) bool     { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func sameIntPtr(a, b *int) bool       { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
