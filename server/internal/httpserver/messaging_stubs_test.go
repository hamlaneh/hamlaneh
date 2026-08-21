package httpserver_test

import (
	"net/http"
	"testing"
)

// Fixture ids for the path-parameterised messaging routes. Nothing looks
// them up: the stubs answer before touching storage.
const (
	stubChannelID = "11111111-2222-3333-4444-555555555555"
	stubUserID    = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	stubMessageID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
)

// stubRoute is one Phase 1.2 route with a concrete request target.
type stubRoute struct {
	method string
	target string
}

// messagingStubRoutes is every Phase 1.2 operation the contract added ahead
// of its implementation.
func messagingStubRoutes() []stubRoute {
	base := "/api/v1/channels/" + stubChannelID
	return []stubRoute{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodPost, "/api/v1/channels"},
		{http.MethodGet, base},
		{http.MethodPatch, base},
		{http.MethodGet, base + "/members"},
		{http.MethodPost, base + "/members"},
		{http.MethodDelete, base + "/members/" + stubUserID},
		{http.MethodGet, base + "/messages"},
		{http.MethodPost, base + "/messages"},
		{http.MethodPatch, base + "/messages/" + stubMessageID},
		{http.MethodDelete, base + "/messages/" + stubMessageID},
		{http.MethodPut, base + "/read"},
		{http.MethodPost, "/api/v1/dms"},
		{http.MethodGet, "/api/v1/search?q=hello"},
		{http.MethodGet, "/api/v1/ws"},
	}
}

// TestMessagingStubsAnswer501 pins the placeholder contract: an
// authenticated member who has passed every route-level gate gets 501
// not_implemented in the contract's Error envelope, on every Phase 1.2
// route. When the messaging slice lands it replaces this test with real
// behavior assertions.
func TestMessagingStubsAnswer501(t *testing.T) {
	t.Parallel()

	for _, route := range messagingStubRoutes() {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()

			mods := []func(*http.Request){withSessionCookie("tok")}
			if route.method != http.MethodGet {
				mods = append(mods, withCSRF())
			}
			rec := do(t, authedStore(fixtureUser()), request(route.method, route.target, "", mods...))

			wantError(t, rec, http.StatusNotImplemented, "not_implemented")
		})
	}
}

// TestMessagingStubsAreGatedBeforeTheStub pins the ordering that makes 501
// safe to publish: route-level security runs first, so an anonymous caller
// never learns that an endpoint exists but is unimplemented.
func TestMessagingStubsAreGatedBeforeTheStub(t *testing.T) {
	t.Parallel()

	locked := fixtureUser()
	locked.MustChangePassword = true

	for _, route := range messagingStubRoutes() {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			t.Parallel()

			anon := do(t, &fakeStore{}, request(route.method, route.target, ""))
			wantError(t, anon, http.StatusUnauthorized, "not_authenticated")

			mods := []func(*http.Request){withSessionCookie("tok")}
			if route.method != http.MethodGet {
				mods = append(mods, withCSRF())
			}
			gated := do(t, authedStore(locked), request(route.method, route.target, "", mods...))
			wantError(t, gated, http.StatusForbidden, "password_change_required")
		})
	}
}
