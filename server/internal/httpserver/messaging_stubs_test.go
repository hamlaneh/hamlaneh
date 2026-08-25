package httpserver_test

import (
	"net/http"
	"testing"
)

// Fixture ids for the path-parameterised messaging routes. Nothing looks
// them up: the stubs answer before touching storage, and the gating test
// never reaches a handler at all.
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

// messagingRoutes is every Phase 1.2 operation the contract added, whether
// or not it is implemented yet. The route-level gate runs before any of
// them, so this is the list the gating test walks.
func messagingRoutes() []stubRoute {
	base := "/api/v1/channels/" + stubChannelID
	return append(messagingStubRoutes(),
		stubRoute{http.MethodGet, "/api/v1/users"},
		stubRoute{http.MethodGet, "/api/v1/channels"},
		stubRoute{http.MethodPost, "/api/v1/channels"},
		stubRoute{http.MethodGet, base},
		stubRoute{http.MethodPatch, base},
		stubRoute{http.MethodGet, base + "/members"},
		stubRoute{http.MethodPost, base + "/members"},
		stubRoute{http.MethodDelete, base + "/members/" + stubUserID},
		stubRoute{http.MethodGet, base + "/messages"},
		stubRoute{http.MethodPost, base + "/messages"},
		stubRoute{http.MethodPut, base + "/read"},
		stubRoute{http.MethodPost, "/api/v1/dms"},
		stubRoute{http.MethodGet, "/api/v1/search?q=hello"},
	)
}

// messagingStubRoutes is what still answers 501: message edit and soft
// delete, both slice 1.2b. The WebSocket upgrade left this list when the
// gateway landed, and search when its handler did.
func messagingStubRoutes() []stubRoute {
	base := "/api/v1/channels/" + stubChannelID
	return []stubRoute{
		{http.MethodPatch, base + "/messages/" + stubMessageID},
		{http.MethodDelete, base + "/messages/" + stubMessageID},
	}
}

// TestMessagingStubsAnswer501 pins the placeholder contract: an
// authenticated member who has passed every route-level gate gets 501
// not_implemented in the contract's Error envelope, on every Phase 1.2
// route that has no behavior yet. Each route leaves this list as its slice
// lands.
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

// TestMessagingRoutesAreGatedBeforeTheHandler pins the ordering every
// messaging route depends on: route-level security runs first, so an
// anonymous caller never reaches a handler and a user who still owes a
// password change never gets past the gate — implemented or not.
func TestMessagingRoutesAreGatedBeforeTheHandler(t *testing.T) {
	t.Parallel()

	locked := fixtureUser()
	locked.MustChangePassword = true

	for _, route := range messagingRoutes() {
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
