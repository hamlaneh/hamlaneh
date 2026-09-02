package httpserver_test

import (
	"context"
	"net/http"
	"testing"

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
		name string
		path string
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
			// The allow-list, from the other side: a contract route that
			// did not move is not reachable on the admin listener merely
			// because it is a contract route.
			name:        "an unmoved contract route",
			path:        "/api/v1/instance",
			wantAdmin:   http.StatusNotFound,
			wantMain:    http.StatusOK,
			wantUnsplit: http.StatusOK,
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
				rec := doHandler(t, l.handler, request(http.MethodGet, tt.path, ""))
				if rec.Code != l.want {
					t.Errorf("GET %s on %s = %d, want %d (body %s)",
						tt.path, l.label, rec.Code, l.want, rec.Body.String())
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
