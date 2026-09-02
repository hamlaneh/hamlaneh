package httpserver_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// adminListenerAddr is a plausible address for the split. Nothing binds it —
// these tests drive the handlers directly — so the value matters only in
// that it is non-empty, which is what turns the split on (ADR 015).
const adminListenerAddr = "127.0.0.1:8081"

// scimMarker is what the stub provisioning surface answers with. A stub
// rather than the real internal/scim handler: what is under test is which
// listener reaches /scim/v2 at all, and a real one would answer 401 on both
// sides of the question, which proves nothing about routing.
const scimMarker = "provisioning-surface-reached"

func scimStub(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := w.Write([]byte(scimMarker)); err != nil {
			t.Errorf("writing the stub provisioning response: %v", err)
		}
	})
}

// splitHandlers builds both listeners' handlers over one router with the
// split on, and the single handler an unsplit install serves. store is
// shared, so the only difference between the three is which paths reach it.
func splitHandlers(t *testing.T, store httpserver.Store) (main, admin, unsplit http.Handler) {
	t.Helper()

	build := fixtureBuild()
	main, admin = httpserver.HandlersWithWebBuild(store, build,
		httpserver.WithSCIM(scimStub(t)),
		httpserver.WithAdminListener(adminListenerAddr))
	if admin == nil {
		t.Fatal("WithAdminListener named an address and no admin handler was built")
	}
	unsplit = httpserver.HandlerWithWebBuild(store, build, httpserver.WithSCIM(scimStub(t)))
	return main, admin, unsplit
}

// TestAdminListenerRoutesBothWays is the whole of ADR 015's routing claim,
// asserted in both directions at once: for every path, what the admin
// listener answers, what the main listener answers with the split ON, and
// what the one listener answers with the split OFF.
//
// The third column is the load-bearing one. It is what keeps "configured
// off, every route stays exactly where it is today" a fact rather than an
// intention, and it is the column that fails if the split ever leaks into
// the default.
func TestAdminListenerRoutesBothWays(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string // empty means GET
		path   string
		body   string
		// The three answers, in the order the listeners are built above.
		wantAdmin   int
		wantMain    int
		wantUnsplit int
	}{
		{
			name:        "the dashboard document",
			path:        "/admin",
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusNotFound,
			wantUnsplit: http.StatusOK,
		},
		{
			name:        "a bookmarked dashboard pane",
			path:        "/admin/invites",
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusNotFound,
			wantUnsplit: http.StatusOK,
		},
		{
			// 401, not 404: the surface IS here, and the session check is
			// what refuses an anonymous caller. That difference is the
			// point of the row below it.
			name:        "the admin API, anonymous",
			path:        "/api/v1/admin/invites",
			wantAdmin:   http.StatusUnauthorized,
			wantMain:    http.StatusNotFound,
			wantUnsplit: http.StatusUnauthorized,
		},
		{
			name:        "the provisioning surface",
			path:        "/scim/v2/Users",
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusNotFound,
			wantUnsplit: http.StatusOK,
		},
		{
			name:        "the dashboard's own bundle",
			path:        fixtureScript,
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
		{
			name:        "the brand mark the document references",
			path:        fixtureMark,
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
		{
			// The shared endpoints (ADR 015 as amended): served by BOTH
			// listeners, because the admin listener is a complete minimal
			// app and a page there must be able to exist. Being on two
			// listeners is not being moved — the main listener keeps every
			// one of them, exactly as it keeps /assets/.
			name:        "shared: the instance document",
			path:        "/api/v1/instance",
			wantAdmin:   http.StatusOK,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
		{
			// 401 on all three: present everywhere, and everywhere it is
			// the session check that answers.
			name:        "shared: who is signed in",
			path:        "/api/v1/users/me",
			wantAdmin:   http.StatusUnauthorized,
			wantMain:    http.StatusUnauthorized,
			wantUnsplit: http.StatusUnauthorized,
		},
		{
			// The second mandatory gate's own way out. Shared for the same
			// reason the first one is: a gate that cannot be passed on this
			// listener locks the tunnel-only operator out of everything
			// behind it (TestAdminListenerEnrolmentIsReachableAndStillGates).
			name:        "shared: the enrolment endpoint",
			path:        "/api/v1/users/me/totp",
			wantAdmin:   http.StatusUnauthorized,
			wantMain:    http.StatusUnauthorized,
			wantUnsplit: http.StatusUnauthorized,
		},
		{
			// 400, not 404: the route exists on every listener and read the
			// (empty) body. TestAdminListenerSignsInAndAdministers walks the
			// real sign-in on the admin port.
			name:        "shared: sign-in",
			method:      http.MethodPost,
			path:        "/api/v1/auth/login",
			wantAdmin:   http.StatusBadRequest,
			wantMain:    http.StatusBadRequest,
			wantUnsplit: http.StatusBadRequest,
		},
		{
			// The allow-list, from the other side: a contract route that
			// neither moved nor is shared is not reachable on the admin
			// listener merely because it is a contract route.
			name:        "an unmoved, unshared contract route",
			path:        "/api/v1/channels",
			wantAdmin:   http.StatusNotFound,
			wantMain:    http.StatusUnauthorized,
			wantUnsplit: http.StatusUnauthorized,
		},
		{
			name:        "a chat permalink",
			path:        "/c/anything",
			wantAdmin:   http.StatusNotFound,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
		{
			name:        "the liveness probe",
			path:        "/healthz",
			wantAdmin:   http.StatusNotFound,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
		{
			name:        "the sign-in document",
			path:        "/",
			wantAdmin:   http.StatusNotFound,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
		},
	}

	main, admin, unsplit := splitHandlers(t, &fakeStore{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			listeners := []struct {
				label   string
				handler http.Handler
				want    int
			}{
				{"admin listener", admin, tt.wantAdmin},
				{"main listener (split on)", main, tt.wantMain},
				{"the one listener (split off)", unsplit, tt.wantUnsplit},
			}
			for _, l := range listeners {
				method := tt.method
				if method == "" {
					method = http.MethodGet
				}
				rec := doHandler(t, l.handler, request(method, tt.path, tt.body))
				if rec.Code != l.want {
					t.Errorf("%s %s on %s = %d, want %d (body %s)",
						method, tt.path, l.label, rec.Code, l.want, rec.Body.String())
				}
			}
		})
	}
}

// TestAdminListenerRefusesWithoutRevealing pins the SHAPE of the main
// listener's refusal, which is the half a status code alone does not carry:
// a moved path answers as though it were never registered — the contract's
// JSON error under /api/, the plain 404 elsewhere — so a probe cannot tell
// a live admin API on another port from an instance that has none.
func TestAdminListenerRefusesWithoutRevealing(t *testing.T) {
	t.Parallel()

	main, _, _ := splitHandlers(t, &fakeStore{})

	// The contract's shape, and the same code an unrouted /api path gets.
	wantError(t, doHandler(t, main, request(http.MethodGet, "/api/v1/admin/invites", "")),
		http.StatusNotFound, "not_found")

	// And the moved surface that is not under /api answers the same plain
	// 404 a server built with no provisioning surface at all answers.
	rec := doHandler(t, main, request(http.MethodGet, "/scim/v2/Users", ""))
	if rec.Body.String() == scimMarker {
		t.Error("the provisioning surface answered on the main listener after the split")
	}
}

// TestAdminRoutesAreNotAuthRoutes guards the one way the shared set could
// quietly undo the split. /api/v1/auth is shared, so the main listener keeps
// it; /api/v1/admin moved, so the main listener must not. The two prefixes
// are neighbours under /api/v1 and a widened shared prefix would swallow the
// admin API back onto the port ADR 015 took it off — which would look like
// nothing at all until somebody probed for it.
func TestAdminRoutesAreNotAuthRoutes(t *testing.T) {
	t.Parallel()

	main, admin, _ := splitHandlers(t, &fakeStore{})

	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/org", "/api/v1/admin/audit"} {
		// On the admin listener the surface is present, so the session
		// check is what answers.
		if rec := doHandler(t, admin, request(http.MethodGet, path, "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s on the admin listener = %d, want 401 (body %s)",
				path, rec.Code, rec.Body.String())
		}
		// On the main listener it is gone, however much the shared
		// /api/v1/auth prefix sits beside it.
		if rec := doHandler(t, main, request(http.MethodGet, path, "")); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s on the main listener = %d, want 404: an admin route is not an auth route",
				path, rec.Code)
		}
	}
}

// TestAdminListenerSignsInAndAdministers walks the tunnel-only operator's
// whole path on one listener: the one who binds the admin port to this
// machine, reaches it over SSH, and never opens the chat port at all. Sign
// in there, then use an admin route there, and get the real answer.
//
// This is what the whole /api/v1/auth prefix is shared FOR, and the test
// that fails if a later trim of that prefix leaves that operator locked out.
func TestAdminListenerSignsInAndAdministers(t *testing.T) {
	t.Parallel()

	_, admin, _ := splitHandlers(t, adminLoginStore())

	sc := login(t, admin, "member", fixturePassword)

	rec := doHandler(t, admin, request(http.MethodGet, "/api/v1/admin/invites", "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("an admin who signed in on the admin listener got %d from its own admin API, want 200 (body %s)",
			rec.Code, rec.Body.String())
	}
}

// adminLoginStore authenticates the fixture admin by password and then by
// the session that sign-in mints, and answers the invites read they are here
// to make.
func adminLoginStore() *fakeStore {
	admin := fixtureUser()
	admin.IsAdmin = true

	store := authedStore(admin)
	store.userByIdentifier = func(_ context.Context, identifier string) (storage.User, error) {
		if strings.EqualFold(identifier, admin.Username) {
			return admin, nil
		}
		return storage.User{}, storage.ErrNotFound
	}
	store.createSession = func(context.Context, storage.NewSession) (storage.Session, error) {
		return fixtureSession(), nil
	}
	store.listOpenInvites = func(context.Context, storage.ListInvitesParams) ([]storage.Invite, error) {
		return nil, nil
	}
	return store
}

// TestAdminListenerEnrolmentIsReachableAndStillGates pins BOTH halves of a
// mandatory gate on the admin listener, and the second half is the one that
// matters. Reachability alone would pass on a listener that had quietly
// stopped gating — the enrolment endpoint answering proves only that a path
// is routed, not that the wall behind it still stands.
//
// The session here is an ADMIN's, flagged for enrolment: the role check
// would let it through, and the gate in front of that check must not.
func TestAdminListenerEnrolmentIsReachableAndStillGates(t *testing.T) {
	t.Parallel()

	admin := fixtureUser()
	admin.IsAdmin = true
	store := totpPendingStore(admin)
	store.totpByUser = func(context.Context, uuid.UUID) (storage.Totp, error) {
		return storage.Totp{}, storage.ErrNotFound
	}
	// Wired so an admin route that got past the gate would answer 200, and
	// the 403 below cannot be an unwired store in disguise.
	store.listOpenInvites = func(context.Context, storage.ListInvitesParams) ([]storage.Invite, error) {
		return nil, nil
	}

	_, adminListener, _ := splitHandlers(t, store)

	// Reachable: the way out of the gate answers here.
	rec := doHandler(t, adminListener, request(http.MethodGet, "/api/v1/users/me/totp", "",
		withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("enrolment on the admin listener = %d, want 200: the gate has no exit on this port (body %s)",
			rec.Code, rec.Body.String())
	}

	// Still gated: until enrolment completes, the admin surface on this same
	// listener is refused by the gate and not by the port.
	wantError(t, doHandler(t, adminListener, request(http.MethodGet, "/api/v1/admin/invites", "",
		withSessionCookie("tok"))), http.StatusForbidden, "totp_enrollment_required")
}

// TestAdminListenerDoesNotAuthorize is the failure ADR 015 forbids by name:
// the port is a deployment boundary, never an authorization decision. Every
// principal gets the same answer on the admin listener that it gets on the
// unsplit one, because both run the same securityMiddleware over the same
// router and the same single authz.Can call site.
//
// The admin row is the control. Without it the anonymous and member rows
// would pass on a listener that answered 404 to everybody.
func TestAdminListenerDoesNotAuthorize(t *testing.T) {
	t.Parallel()

	// An admin who gets through reaches the store and gets an answer, so
	// "past the gate" is a 200 and not an error that might have any cause.
	wireInvites := func(s *fakeStore) *fakeStore {
		s.listOpenInvites = func(context.Context, storage.ListInvitesParams) ([]storage.Invite, error) {
			return nil, nil
		}
		return s
	}

	tests := []struct {
		name  string
		store func() *fakeStore
		mods  []func(*http.Request)
		want  int
	}{
		{
			name:  "anonymous is refused by the session check",
			store: func() *fakeStore { return wireInvites(&fakeStore{}) },
			want:  http.StatusUnauthorized,
		},
		{
			name:  "a non-admin session is refused by the role check",
			store: func() *fakeStore { return wireInvites(authedStore(fixtureUser())) },
			mods:  []func(*http.Request){withSessionCookie("tok")},
			want:  http.StatusForbidden,
		},
		{
			name:  "an admin session is let through",
			store: func() *fakeStore { return wireInvites(adminStore()) },
			mods:  []func(*http.Request){withSessionCookie("tok")},
			want:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, admin, unsplit := splitHandlers(t, tt.store())

			got := doHandler(t, admin, request(http.MethodGet, "/api/v1/admin/invites", "", tt.mods...))
			if got.Code != tt.want {
				t.Errorf("GET /api/v1/admin/invites on the admin listener = %d, want %d (body %s)",
					got.Code, tt.want, got.Body.String())
			}

			// The same request against an install that never split: the
			// port changed nothing about who may use the surface.
			same := doHandler(t, unsplit, request(http.MethodGet, "/api/v1/admin/invites", "", tt.mods...))
			if same.Code != got.Code {
				t.Errorf("the admin listener answered %d where the unsplit listener answered %d: "+
					"the port changed an authorization outcome", got.Code, same.Code)
			}
		})
	}
}
