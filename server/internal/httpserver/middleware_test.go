package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// TestProtectedRoutesRequireSession pins the 401 for anonymous requests on
// every session-gated endpoint.
func TestProtectedRoutesRequireSession(t *testing.T) {
	t.Parallel()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/auth/change-password"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			t.Parallel()
			rec := do(t, &fakeStore{}, request(ep.method, ep.path, ""))
			wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
		})
	}
}

// TestInvalidSessionIs401 pins that an unknown/expired/revoked access token
// (storage.ErrNotFound) answers 401, without clearing cookies.
func TestInvalidSessionIs401(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		sessionUserByAccessHash: func(context.Context, []byte) (storage.Session, storage.User, error) {
			return storage.Session{}, storage.User{}, storage.ErrNotFound
		},
	}
	rec := do(t, store, request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("stale-token")))

	wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	if len(responseCookies(rec)) != 0 {
		t.Error("plain 401 modified cookies; only logout and refresh failures may clear them")
	}
}

func TestCSRF(t *testing.T) {
	t.Parallel()

	member := fixtureUser()

	t.Run("mutation with session cookie but no header is rejected", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(member), request(http.MethodPost, "/api/v1/auth/logout", "",
			withSessionCookie("tok"),
			func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: "csrf-value"})
			},
		))
		wantError(t, rec, http.StatusForbidden, "csrf_failed")
	})

	t.Run("mutation with wrong header value is rejected", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(member), request(http.MethodPost, "/api/v1/auth/logout", "",
			withSessionCookie("tok"),
			func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: "csrf-value"})
				r.Header.Set(session.CSRFHeader, "some-other-value")
			},
		))
		wantError(t, rec, http.StatusForbidden, "csrf_failed")
	})

	t.Run("mutation missing the csrf cookie is rejected", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(member), request(http.MethodPost, "/api/v1/auth/logout", "",
			withSessionCookie("tok"),
			func(r *http.Request) { r.Header.Set(session.CSRFHeader, "header-only") },
		))
		wantError(t, rec, http.StatusForbidden, "csrf_failed")
	})

	t.Run("GET is unaffected by missing csrf header", func(t *testing.T) {
		t.Parallel()
		store := authedStore(member)
		rec := do(t, store, request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Errorf("GET with session but no CSRF header: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("login is exempt", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{
			userByIdentifier: func(context.Context, string) (storage.User, error) {
				return storage.User{}, storage.ErrNotFound
			},
		}
		// Even with a (stale) session cookie attached and no CSRF header,
		// login proceeds to credential checking instead of 403.
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/login",
			loginBody("ghost", "irrelevant-password"), withSessionCookie("stale")))
		wantError(t, rec, http.StatusUnauthorized, "invalid_credentials")
	})

	t.Run("mutation without any session cookie skips csrf", func(t *testing.T) {
		t.Parallel()
		// No ambient authority to forge: the request fails authentication,
		// not CSRF.
		rec := do(t, &fakeStore{}, request(http.MethodPost, "/api/v1/auth/logout", ""))
		wantError(t, rec, http.StatusUnauthorized, "not_authenticated")
	})
}

func TestMustChangePasswordGate(t *testing.T) {
	t.Parallel()

	locked := fixtureUser()
	locked.MustChangePassword = true

	t.Run("users/me stays reachable", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(locked), request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("logout stays reachable", func(t *testing.T) {
		t.Parallel()
		store := authedStore(locked)
		store.revokeFamily = func(context.Context, uuid.UUID) error { return nil }
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/logout", "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Errorf("got %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("change-password stays reachable", func(t *testing.T) {
		t.Parallel()
		store := authedStore(locked)
		store.updatePassword = func(context.Context, uuid.UUID, string, uuid.UUID) error { return nil }
		body := `{"current_password":"` + fixturePassword + `","new_password":"a brand new passphrase"}`
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Errorf("got %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin list is gated even for admins", func(t *testing.T) {
		t.Parallel()
		lockedAdmin := fixtureUser()
		lockedAdmin.IsAdmin = true
		lockedAdmin.MustChangePassword = true
		rec := do(t, authedStore(lockedAdmin), request(http.MethodGet, "/api/v1/admin/users", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusForbidden, "password_change_required")
	})

	t.Run("admin create is gated", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(locked), request(http.MethodPost, "/api/v1/admin/users", `{}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusForbidden, "password_change_required")
	})
}

// TestAdminGate pins that members cannot reach admin endpoints (403
// forbidden) while admins can.
func TestAdminGate(t *testing.T) {
	t.Parallel()

	t.Run("member is forbidden from admin list", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(fixtureUser()), request(http.MethodGet, "/api/v1/admin/users", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusForbidden, "forbidden")
	})

	t.Run("member is forbidden from admin create", func(t *testing.T) {
		t.Parallel()
		rec := do(t, authedStore(fixtureUser()), request(http.MethodPost, "/api/v1/admin/users", `{}`,
			withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusForbidden, "forbidden")
	})

	t.Run("admin passes the gate", func(t *testing.T) {
		t.Parallel()
		admin := fixtureUser()
		admin.IsAdmin = true
		store := authedStore(admin)
		store.listUsers = func(context.Context, storage.ListUsersParams) ([]storage.User, error) {
			return []storage.User{admin}, nil
		}
		rec := do(t, store, request(http.MethodGet, "/api/v1/admin/users", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// totpPendingStore wires a fakeStore whose session carries
// totp_enrollment_required — the state a sign-in mints under an enforced
// two-step policy the account does not satisfy.
func totpPendingStore(user storage.User) *fakeStore {
	return &fakeStore{
		sessionUserByAccessHash: func(context.Context, []byte) (storage.Session, storage.User, error) {
			sess := fixtureSession()
			sess.TotpEnrollmentRequired = true
			return sess, user, nil
		},
	}
}

// TestTotpEnrollmentGate pins the enrolment gate route by route: a flagged
// session reaches logout, reading and patching users/me, and the four TOTP
// enrolment endpoints — and nothing else, the WebSocket upgrade included.
func TestTotpEnrollmentGate(t *testing.T) {
	t.Parallel()

	t.Run("users/me stays reachable and reports the flag", func(t *testing.T) {
		t.Parallel()
		rec := do(t, totpPendingStore(fixtureUser()),
			request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var user struct {
			TotpEnrollmentRequired bool `json:"totp_enrollment_required"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
			t.Fatalf("users/me body: %v", err)
		}
		if !user.TotpEnrollmentRequired {
			t.Error("users/me does not report totp_enrollment_required for a flagged session")
		}
	})

	t.Run("patching users/me stays reachable", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(fixtureUser())
		store.updateUserProfile = func(_ context.Context, _ uuid.UUID, _ storage.UserProfileUpdate) (storage.User, error) {
			return fixtureUser(), nil
		}
		rec := do(t, store, request(http.MethodPatch, "/api/v1/users/me", `{"locale":"fa"}`,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("logout stays reachable", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(fixtureUser())
		store.revokeFamily = func(context.Context, uuid.UUID) error { return nil }
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/logout", "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Errorf("got %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("totp status stays reachable", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(fixtureUser())
		store.totpByUser = func(context.Context, uuid.UUID) (storage.Totp, error) {
			return storage.Totp{}, storage.ErrNotFound
		}
		rec := do(t, store, request(http.MethodGet, "/api/v1/users/me/totp", "",
			withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("totp setup stays reachable", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(fixtureUser())
		store.startTotpSetup = func(context.Context, uuid.UUID, []byte, time.Duration) error { return nil }
		rec := do(t, store, request(http.MethodPost, "/api/v1/users/me/totp/setup", "",
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusOK {
			t.Errorf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})

	// Everything below must be refused by the gate itself — no store method
	// beyond authentication is wired, so a cell that reached its handler
	// would fail on the fake, not with the gate's own 403.
	gated := []struct {
		name   string
		method string
		path   string
	}{
		{"disable", http.MethodPost, "/api/v1/users/me/totp/disable"},
		{"recovery codes", http.MethodPost, "/api/v1/users/me/totp/recovery-codes"},
		{"change-password", http.MethodPost, "/api/v1/auth/change-password"},
		{"messaging", http.MethodGet, "/api/v1/channels"},
		{"websocket upgrade", http.MethodGet, "/api/v1/ws"},
		{"session list", http.MethodGet, "/api/v1/users/me/sessions"},
	}
	for _, tt := range gated {
		t.Run(tt.name+" is gated", func(t *testing.T) {
			t.Parallel()
			mods := []func(*http.Request){withSessionCookie("tok")}
			if tt.method != http.MethodGet {
				mods = append(mods, withCSRF())
			}
			rec := do(t, totpPendingStore(fixtureUser()), request(tt.method, tt.path, "", mods...))
			wantError(t, rec, http.StatusForbidden, "totp_enrollment_required")
		})
	}

	t.Run("admin routes are gated even for admins", func(t *testing.T) {
		t.Parallel()
		admin := fixtureUser()
		admin.IsAdmin = true
		rec := do(t, totpPendingStore(admin),
			request(http.MethodGet, "/api/v1/admin/users", "", withSessionCookie("tok")))
		wantError(t, rec, http.StatusForbidden, "totp_enrollment_required")
	})

	t.Run("refresh never consults the gate", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(fixtureUser())
		store.rotateSession = func(context.Context, []byte, storage.SessionTokens) (storage.Session, storage.RotateOutcome, error) {
			return fixtureSession(), storage.RotateOutcomeRotated, nil
		}
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/refresh", "",
			func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: session.RefreshCookie, Value: "rtok"})
			}))
		if rec.Code != http.StatusNoContent {
			t.Errorf("got %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestGateSequencing pins the contract's rule for a session carrying BOTH
// flags: only the password gate applies until the password is changed.
// Without the sequencing the two allow-lists intersect at almost nothing and
// the account could satisfy neither demand.
func TestGateSequencing(t *testing.T) {
	t.Parallel()

	both := fixtureUser()
	both.MustChangePassword = true

	t.Run("change-password stays reachable with both flags", func(t *testing.T) {
		t.Parallel()
		store := totpPendingStore(both)
		store.updatePassword = func(context.Context, uuid.UUID, string, uuid.UUID) error { return nil }
		body := `{"current_password":"` + fixturePassword + `","new_password":"a brand new passphrase"}`
		rec := do(t, store, request(http.MethodPost, "/api/v1/auth/change-password", body,
			withSessionCookie("tok"), withCSRF()))
		if rec.Code != http.StatusNoContent {
			t.Errorf("got %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("enrolment endpoints answer the password gate with both flags", func(t *testing.T) {
		t.Parallel()
		rec := do(t, totpPendingStore(both),
			request(http.MethodPost, "/api/v1/users/me/totp/setup", "",
				withSessionCookie("tok"), withCSRF()))
		wantError(t, rec, http.StatusForbidden, "password_change_required")
	})

	t.Run("users/me reports both flags", func(t *testing.T) {
		t.Parallel()
		rec := do(t, totpPendingStore(both),
			request(http.MethodGet, "/api/v1/users/me", "", withSessionCookie("tok")))
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var user struct {
			MustChangePassword     bool `json:"must_change_password"`
			TotpEnrollmentRequired bool `json:"totp_enrollment_required"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
			t.Fatalf("users/me body: %v", err)
		}
		if !user.MustChangePassword || !user.TotpEnrollmentRequired {
			t.Errorf("users/me reports must_change=%v totp_pending=%v, want both true",
				user.MustChangePassword, user.TotpEnrollmentRequired)
		}
	})
}
