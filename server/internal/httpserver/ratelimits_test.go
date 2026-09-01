package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The generic per-endpoint rate-limit middleware (ratelimits.go).
//
// Every test here drives the real stack through httpserver.Handler, because
// the budget is a property of the chain and not of any handler: it is spent
// after authentication (so it has an account to key on) and before the
// handler runs (so a refusal actually refuses the work).
//
// The budgets a handler still spends for itself — login, two-step sign-in,
// password reset — keep their own tests where they always were. This file
// covers what the table decides.

// fixtureChannelID is a syntactically valid channel id for the routes that
// carry one. The generated router binds path parameters before any
// middleware, so a malformed id would answer 400 without ever reaching the
// budget being measured.
const fixtureChannelID = "44444444-4444-4444-4444-444444444444"

// accountsStore authenticates each of the given users by an access token
// equal to its username, so one handler — and therefore one set of limiters
// — can serve two distinct callers.
func accountsStore(users ...storage.User) *fakeStore {
	byHash := make(map[string]storage.User, len(users))
	for _, u := range users {
		byHash[string(session.HashToken(u.Username))] = u
	}
	return &fakeStore{
		sessionUserByAccessHash: func(_ context.Context, hash []byte) (storage.Session, storage.User, error) {
			u, ok := byHash[string(hash)]
			if !ok {
				return storage.Session{}, storage.User{}, storage.ErrNotFound
			}
			sess := fixtureSession()
			sess.UserID = u.ID
			return sess, u, nil
		},
	}
}

// secondUser is an account that is not fixtureUser, for the tests that prove
// two callers do not share one window.
func secondUser() storage.User {
	u := fixtureUser()
	u.ID = uuid.MustParse("99999999-9999-9999-9999-999999999999")
	u.Username = "other"
	return u
}

// budgetCase is one endpoint standing in for its budget: how many requests
// the caller may make, and the request to repeat.
type budgetCase struct {
	name   string
	limit  int
	method string
	target string
	body   string
}

// budgetCases covers every budget the middleware owns, one representative
// endpoint each.
func budgetCases() []budgetCase {
	return []budgetCase{
		{
			name:   "directory",
			limit:  httpserver.DirectoryRateLimit,
			method: http.MethodGet,
			target: "/api/v1/users",
		},
		{
			name:   "search",
			limit:  httpserver.SearchRateLimit,
			method: http.MethodGet,
			target: "/api/v1/search?q=ab",
		},
		{
			name:   "message send",
			limit:  httpserver.MessageSendRateLimit,
			method: http.MethodPost,
			target: "/api/v1/channels/" + fixtureChannelID + "/messages",
			body:   `{"client_msg_id":"55555555-5555-5555-5555-555555555555","content":"hi"}`,
		},
		{
			name:   "conversation write",
			limit:  httpserver.ConversationWriteRateLimit,
			method: http.MethodPost,
			target: "/api/v1/channels",
			body:   `{"kind":"public","slug":"budget"}`,
		},
		{
			name:   "two-step settings",
			limit:  httpserver.TotpSettingsRateLimit,
			method: http.MethodPost,
			target: "/api/v1/users/me/totp/setup",
		},
	}
}

// callAs builds one request of the case, signed in as the named account.
func (c budgetCase) callAs(username string) *http.Request {
	mods := []func(*http.Request){withSessionCookie(username)}
	if c.method != http.MethodGet {
		mods = append(mods, withCSRF())
	}
	return request(c.method, c.target, c.body, mods...)
}

// TestBudgetRefusesAtItsLimitAndNotBefore is the core property of every
// budget in the table: the limit-th request still reaches its handler, and
// only the one after it is refused.
//
// The assertion is "not 429" rather than a particular success status on
// purpose. Whether a given handler then answers 200, 400 or 500 against an
// unwired fake is that handler's business; what this test pins is where the
// middleware draws its line. TestSearchIsBudgetedPerAccount already proves
// the green path stays green underneath a budget.
func TestBudgetRefusesAtItsLimitAndNotBefore(t *testing.T) {
	t.Parallel()

	for _, tc := range budgetCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// One handler across every attempt: the budgets live on the
			// server, so a fresh handler per request would spend nothing.
			handler := httpserver.Handler(accountsStore(fixtureUser()))

			for attempt := range tc.limit {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, tc.callAs(fixtureUser().Username))
				if rec.Code == http.StatusTooManyRequests {
					t.Fatalf("attempt %d of %d was refused while the budget still stood (body %s)",
						attempt+1, tc.limit, rec.Body.String())
				}
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, tc.callAs(fixtureUser().Username))
			wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
			// Every 429 this server sends names its own wait; a client left to
			// guess retries straight back into the same refusal.
			if rec.Header().Get("Retry-After") == "" {
				t.Error("the refusal carries no Retry-After")
			}
		})
	}
}

// TestBudgetsAreKeyedPerAccount proves the key actually separates callers:
// an exhausted account must not spend anybody else's budget. Both callers
// speak to the SAME handler, so they share the limiter and differ only in
// the key it is asked about.
//
// Keying these on the account rather than the address is deliberate — see
// the table in ratelimits.go. An office behind one NAT address is one key by
// IP and many keys by account, and throttling a room because one colleague
// is chatty would be a bug wearing a security control's clothes.
func TestBudgetsAreKeyedPerAccount(t *testing.T) {
	t.Parallel()

	for _, tc := range budgetCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := httpserver.Handler(accountsStore(fixtureUser(), secondUser()))

			for range tc.limit + 1 {
				handler.ServeHTTP(httptest.NewRecorder(), tc.callAs(fixtureUser().Username))
			}

			// The first account is now refused...
			spent := httptest.NewRecorder()
			handler.ServeHTTP(spent, tc.callAs(fixtureUser().Username))
			if spent.Code != http.StatusTooManyRequests {
				t.Fatalf("the exhausted account got status %d, want 429 (body %s)",
					spent.Code, spent.Body.String())
			}

			// ...and the second one still has its own window.
			fresh := httptest.NewRecorder()
			handler.ServeHTTP(fresh, tc.callAs(secondUser().Username))
			if fresh.Code == http.StatusTooManyRequests {
				t.Errorf("an unrelated account was refused (body %s)", fresh.Body.String())
			}
		})
	}
}

// TestConversationWritesShareOneWindow pins the shared budget: creating a
// channel, opening a direct message and adding a member are one window per
// account, not three.
//
// Per-endpoint budgets would only multiply the total an attacker gets,
// because they pick whichever of the three is cheapest for them. It is the
// same reasoning the two-step settings budget already uses, and the same
// reasoning that must survive somebody later "tidying" the table into one
// row per endpoint.
func TestConversationWritesShareOneWindow(t *testing.T) {
	t.Parallel()

	handler := httpserver.Handler(accountsStore(fixtureUser()))
	peer := `{"user_id":"` + secondUser().ID.String() + `"}`

	// Exhaust the window through channel creation alone.
	for range httpserver.ConversationWriteRateLimit {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodPost, "/api/v1/channels",
			`{"kind":"public","slug":"budget"}`,
			withSessionCookie(fixtureUser().Username), withCSRF()))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatal("the shared window was exhausted early")
		}
	}

	for _, tt := range []struct {
		name   string
		target string
		body   string
	}{
		{"create channel", "/api/v1/channels", `{"kind":"public","slug":"another"}`},
		{"open direct message", "/api/v1/dms", peer},
		{"add channel member", "/api/v1/channels/" + fixtureChannelID + "/members", peer},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, request(http.MethodPost, tt.target, tt.body,
				withSessionCookie(fixtureUser().Username), withCSRF()))
			wantError(t, rec, http.StatusTooManyRequests, "rate_limited")
		})
	}
}

// TestUnbudgetedEndpointIsNeverRefused is the asserted half of the decision
// the table records for endpoints the contract reserves no 429 on: they are
// served, not refused. Listing messages is the one that matters — it is the
// read a client repeats most, and a budget nobody declared must not appear
// on it by accident.
func TestUnbudgetedEndpointIsNeverRefused(t *testing.T) {
	t.Parallel()

	handler := httpserver.Handler(accountsStore(fixtureUser()))
	for attempt := range httpserver.MessageSendRateLimit * 2 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(http.MethodGet,
			"/api/v1/channels/"+fixtureChannelID+"/messages", "",
			withSessionCookie(fixtureUser().Username)))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("an endpoint the table leaves unbudgeted was refused on attempt %d", attempt+1)
		}
	}
}

// TestAuthenticationAnswersBeforeTheBudget pins where the middleware sits in
// the chain. The budget runs AFTER securityMiddleware, which is what gives it
// an account to key on — so a request that fails a route-level gate is
// answered by that gate and never spends anything.
//
// The CSRF case is the sharp one: the account's window is already exhausted,
// so a 429 here would prove the budget had run first.
func TestAuthenticationAnswersBeforeTheBudget(t *testing.T) {
	t.Parallel()

	handler := httpserver.Handler(accountsStore(fixtureUser()))
	setup := func(mods ...func(*http.Request)) *http.Request {
		return request(http.MethodPost, "/api/v1/users/me/totp/setup", "", mods...)
	}

	for range httpserver.TotpSettingsRateLimit + 1 {
		handler.ServeHTTP(httptest.NewRecorder(), setup(withSessionCookie(fixtureUser().Username), withCSRF()))
	}

	// Sanity: the window really is exhausted for this account.
	spent := httptest.NewRecorder()
	handler.ServeHTTP(spent, setup(withSessionCookie(fixtureUser().Username), withCSRF()))
	if spent.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429 before the ordering assertions mean anything", spent.Code)
	}

	tests := []struct {
		name string
		req  *http.Request
		want int
		code string
	}{
		{
			name: "no session at all",
			req:  setup(withCSRF()),
			want: http.StatusUnauthorized,
			code: "not_authenticated",
		},
		{
			name: "session without the CSRF pair",
			req:  setup(withSessionCookie(fixtureUser().Username)),
			want: http.StatusForbidden,
			code: "csrf_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, tt.req)
			wantError(t, rec, tt.want, tt.code)
		})
	}
}
