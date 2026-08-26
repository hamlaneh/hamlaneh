package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/ratelimit"
)

// Per-endpoint rate-limit budgets, decided from the route a request matched
// rather than spent by hand inside each handler.
//
// # Where this runs in the chain
//
// rateLimitMiddleware sits between securityMiddleware and the handler:
// authentication, the must-change gate and the admin check answer first, and
// nothing of the handler has run yet when the budget is spent. Both halves of
// that position are load-bearing.
//
//   - AFTER authentication, because a budget spent before it cannot know the
//     account. That is not a convenience: it is what lets every budget below
//     be keyed on the caller rather than on their address, which is the
//     correct key for work an authenticated caller asks the server to do.
//   - BEFORE the handler, because a budget spent after the expensive work is
//     not a rate limit. The four two-step settings endpoints exist to bound
//     argon2id verifications; search exists to bound a scan that can fall back
//     to sequential; message send exists to bound writes. A 429 written after
//     any of those has already paid for what it refused.
//
// The order is expressed in server.go, where the generated router wraps the
// middlewares in slice order — so the LAST element is the outermost and runs
// first. TestAuthenticationAnswersBeforeTheBudget pins it from the outside so
// the list cannot be reordered by accident.
//
// # Why every budget here is keyed on the account
//
// It follows from the position rather than from taste. A budget this
// middleware can spend is one that ran after authentication, so it always has
// an account; a budget that must be keyed on an address is by definition one
// that decides before an account exists — login, the two-step code step,
// password reset — and those are exactly the ones that stayed in their
// handlers (see "What deliberately did not move" below).
//
// Keying on the account is also the answer these endpoints want. What a
// search costs follows how many messages the caller can reach; what a send
// costs is a write attributed to a person. An office behind one NAT address
// is one key by IP and many keys by account, and throttling a whole room
// because one colleague is chatty would be a bug wearing a security
// control's clothes.
//
// # What an endpoint with no declared budget does
//
// It is refused, with 500, exactly as an unclassified route is refused by
// routePolicies — the sibling table this one is shaped after. Two tables
// keyed on the same thing with opposite defaults would be a trap, and
// declaring an endpoint deliberately unbudgeted costs one word
// (budgetNone), so an omission is never a decision somebody made. The
// refusal is not a production failure mode either:
// TestEveryContractRouteHasABudgetDecision walks the routes the generated
// router registers and fails the build for a missing entry, so a new
// contract endpoint is caught in CI rather than at a user.
//
// # Which endpoints get a budget
//
// Exactly the ones docs/api/openapi.yaml reserves a "429 RateLimited"
// response on. An endpoint with no 429 in the contract does not get one
// invented here: adding it would be a contract change, which is reported,
// not made. Endpoints the contract does budget but this table does not
// carries budgetElsewhere and says where its budget actually lives.
//
// # What deliberately did not move
//
//   - Login (POST /api/v1/auth/login) spends TWO budgets against different
//     keys, the client address and the lowercased identifier, and the
//     identifier is in the request body — unreadable from the route, and not
//     readable at all without consuming a body the handler still has to
//     parse. It also records conditionally: a failed authentication and a
//     two-step challenge mint spend a unit, a completed sign-in spends
//     nothing. None of that is expressible as "this route costs one unit".
//   - The two-step code step (POST /api/v1/auth/login/totp) checks the
//     per-IP window before the challenge cookie is resolved and the
//     per-account window after the account behind that cookie is known but
//     before the presented code is evaluated, and then records only the
//     attempts that failed. Its two spends bracket work this middleware
//     cannot see between.
//   - Password reset keeps its budgets in internal/passwordreset, where the
//     rest of the enumeration defense lives. The request budget is keyed on
//     the presented address as well as the client IP, refuses with the longer
//     of the two waits, and counts requests that matched no account — all of
//     which exists so the 429 itself is not an existence oracle. Splitting
//     the IP half out to a route table would leave the address half to refuse
//     a request whose IP unit had already been recorded, which is a different
//     control from the one that is there.
//   - GET /api/v1/ws is budgeted by the gateway, not here. ws-protocol.md §8
//     keys the connect budget per session family AND per IP, and requires it
//     to be enforceable on an already-open socket (close code 4429), which
//     nothing request-scoped can do. Still open — ROADMAP 1.2a.
//
// Moving any of those into this table would have made it tidier and the
// control weaker, which is the wrong trade.

// budgetName identifies one rate-limit budget. Endpoints naming the same
// budget share one window, so a caller cannot walk along a row of
// interchangeable endpoints to buy a fresh one.
type budgetName string

const (
	// budgetUndeclared is what a lookup miss yields: an endpoint nobody
	// classified. It fails closed.
	budgetUndeclared budgetName = ""
	// budgetElsewhere marks an endpoint whose budget cannot be decided from
	// the route and is spent outside this middleware. Every use names where.
	budgetElsewhere budgetName = "elsewhere"
	// budgetNone marks an endpoint deliberately left unbudgeted, because the
	// contract reserves no 429 on it.
	budgetNone budgetName = "none"

	budgetTotpSettings      budgetName = "totp-settings"
	budgetSearch            budgetName = "search"
	budgetMessageSend       budgetName = "message-send"
	budgetConversationWrite budgetName = "conversation-write"
	budgetDirectory         budgetName = "directory"
	budgetUpload            budgetName = "upload"
)

// budgetSpec is one budget: how many requests fit its sliding window, and
// how long that window is. There is no key field because there is nothing to
// choose — see the file comment on why every budget here is per account.
type budgetSpec struct {
	limit  int
	window time.Duration
}

// budgetSpecs gives every named budget its numbers and its reason. The
// reasons are the point: a limit with no argument behind it is a number
// somebody will later "tune" in either direction with equal confidence.
var budgetSpecs = map[budgetName]budgetSpec{
	// Two-step settings: one window per account across totp/setup,
	// totp/verify, totp/disable and totp/recovery-codes.
	//
	// The budget is about server work, not guessing, which is why every call
	// spends a unit rather than only the failures. Two of the four run a full
	// argon2id password verification (64 MiB) before they decide anything,
	// and recovery-codes then hashes ten more; a hijacked session, or simply
	// a stuck client, can otherwise repeat that indefinitely.
	//
	// One window covers all four because an attacker who has a session picks
	// whichever endpoint is cheapest for them, and per-endpoint budgets would
	// just multiply the total. 10 in 5 minutes is far above any real use — a
	// user regenerates codes once — and far below what makes the argon2 cost
	// worth paying.
	//
	// totp/activate is deliberately absent: it verifies nothing and hashes
	// nothing, and the contract reserves no 429 on it.
	budgetTotpSettings: {limit: 10, window: 5 * time.Minute},

	// Search is budgeted because its cost is not uniform. The trigram index
	// behind it needs three characters to work with; a one- or two-character
	// needle cannot use it and falls back to a sequential scan over every
	// message the caller can reach — measured at 240ms across 60,000 rows,
	// and the contract allows a query that short on purpose, because one- and
	// two-character words are ordinary in Persian.
	//
	// So the endpoint is cheap to call and occasionally expensive to serve,
	// from an authenticated caller, in a loop. 30 in a minute is far above
	// someone typing into a search box and far below what a loop needs to
	// hurt.
	budgetSearch: {limit: 30, window: time.Minute},

	// Uploads move the most bytes per request of anything in the contract,
	// and each one costs disk that only the 24-hour orphan sweep reclaims if
	// no message ever references it. 20 a minute is a person dragging a
	// folder's worth of screenshots into a channel; a loop filling the disk
	// wants thousands. Declared with the contract stub so the pipeline
	// inherits a decided number instead of shipping with none.
	budgetUpload: {limit: 20, window: time.Minute},

	// Message send is the cheapest way to make this server write: one row,
	// plus a fan-out to every socket in the channel. Per account, because a
	// message is attributed to a person and because a shared address must not
	// let one colleague throttle a room.
	//
	// 60 a minute is a sustained message per second — past any human, and
	// comfortably past the burst a client flushes when it reconnects with a
	// queued outbox (sends are idempotent on client_msg_id, so that flush is
	// bounded by what the user actually typed).
	budgetMessageSend: {limit: 60, window: time.Minute},

	// Creating a channel, opening a direct message and adding a member are
	// one window per account, not three: they are the writes that create
	// conversation structure, and an attacker picks whichever of the three is
	// cheapest for them. Per-endpoint budgets would only multiply the total.
	//
	// 60 a minute is far above the real flows, all of which are one click at
	// a time: the people picker adds a single person per open, and a DM is
	// opened by clicking one name. It is far below what filling the channels
	// and channel_members tables would need.
	budgetConversationWrite: {limit: 60, window: time.Minute},

	// The user directory is a bounded, indexed page, so this budget bounds
	// repetition rather than any one call. It is deliberately looser than the
	// others: the picker debounces at 200ms, so a hunt-and-peck typist can
	// fire close to one request per character, and a budget that refused an
	// ordinary search for a colleague would be a broken screen rather than a
	// control.
	//
	// The endpoint is not a secrecy boundary — every signed-in user may read
	// the directory, and the rows carry no email, role or password state — so
	// what is being bounded here is server work, not disclosure.
	budgetDirectory: {limit: 120, window: time.Minute},
}

// endpointBudgets is the rate-limit table for every contract endpoint, keyed
// on the ServeMux pattern that matched the request (http.Request.Pattern) —
// "METHOD /path" with the contract's {template} segments left in place,
// exactly as the generated router registers them and exactly as
// routePolicies is keyed.
//
// Keying on the pattern rather than the concrete URL path is what lets
// path-parameterised routes be budgeted at all: one entry covers every
// channel id. It cannot be spoofed either, because the pattern is chosen by
// the router after matching, not taken from the request.
var endpointBudgets = map[string]budgetName{
	// Probes and the public instance document. Unauthenticated and free by
	// design: /healthz and /readyz are what an orchestrator polls, and
	// /api/v1/instance is read once by the sign-in screen before anyone has
	// an account to key a budget on.
	"GET /healthz":         budgetNone,
	"GET /readyz":          budgetNone,
	"GET /api/v1/instance": budgetNone,

	// Sign-in and account recovery. Every one of these is budgeted, and not
	// one of them can be budgeted from its route — see "What deliberately did
	// not move" at the top of this file.
	"POST /api/v1/auth/login":          budgetElsewhere, // auth_handlers.go: per IP and per identifier
	"POST /api/v1/auth/login/totp":     budgetElsewhere, // totp_handlers.go: per IP and per account
	"POST /api/v1/auth/reset-request":  budgetElsewhere, // internal/passwordreset: per address and per IP
	"POST /api/v1/auth/reset-complete": budgetElsewhere, // internal/passwordreset: per IP
	"GET /api/v1/ws":                   budgetElsewhere, // internal/wsgateway: per family and per IP — still to build

	// Session lifecycle. The contract reserves no 429 on any of these and
	// none of them is a lever: refresh and logout are bounded by holding a
	// live token, and users/me is a single indexed read.
	"POST /api/v1/auth/refresh":         budgetNone,
	"POST /api/v1/auth/logout":          budgetNone,
	"POST /api/v1/auth/change-password": budgetNone,
	"GET /api/v1/users/me":              budgetNone,

	// Two-step verification settings: four endpoints, one window.
	"POST /api/v1/users/me/totp/setup":          budgetTotpSettings,
	"POST /api/v1/users/me/totp/verify":         budgetTotpSettings,
	"POST /api/v1/users/me/totp/disable":        budgetTotpSettings,
	"POST /api/v1/users/me/totp/recovery-codes": budgetTotpSettings,
	"GET /api/v1/users/me/totp":                 budgetNone,
	"POST /api/v1/users/me/totp/activate":       budgetNone,

	// Self-service session management. Reads and revocations of the caller's
	// own families; the contract reserves no 429.
	"GET /api/v1/users/me/sessions":                budgetNone,
	"DELETE /api/v1/users/me/sessions/{familyId}":  budgetNone,
	"POST /api/v1/users/me/sessions/revoke-others": budgetNone,

	// Administration. Bounded by being an admin at all, and creating a user
	// is not a surface an attacker reaches without already holding the
	// instance; the contract reserves no 429.
	"GET /api/v1/admin/users":  budgetNone,
	"POST /api/v1/admin/users": budgetNone,

	// Conversations and messages.
	"GET /api/v1/users":                          budgetDirectory,
	"GET /api/v1/search":                         budgetSearch,
	"POST /api/v1/channels/{channelId}/messages": budgetMessageSend,
	"POST /api/v1/channels/{channelId}/files":    budgetUpload,
	"POST /api/v1/channels":                      budgetConversationWrite,
	"POST /api/v1/dms":                           budgetConversationWrite,
	"POST /api/v1/channels/{channelId}/members":  budgetConversationWrite,

	// Reads and edits the contract reserves no 429 on. Listing messages is
	// the read a client repeats most — a budget nobody declared must not
	// appear on it by accident, which is what the explicit budgetNone says.
	"GET /api/v1/channels":                                     budgetNone,
	"GET /api/v1/channels/{channelId}":                         budgetNone,
	"PATCH /api/v1/channels/{channelId}":                       budgetNone,
	"GET /api/v1/channels/{channelId}/members":                 budgetNone,
	"DELETE /api/v1/channels/{channelId}/members/{userId}":     budgetNone,
	"GET /api/v1/channels/{channelId}/messages":                budgetNone,
	"PATCH /api/v1/channels/{channelId}/messages/{messageId}":  budgetNone,
	"DELETE /api/v1/channels/{channelId}/messages/{messageId}": budgetNone,
	"PUT /api/v1/channels/{channelId}/read":                    budgetNone,
}

// newBudgetLimiters builds one limiter per named budget. Limiters live on the
// apiServer — never in package state — so every Handler carries its own
// windows and tests cannot bleed budget into each other.
func newBudgetLimiters() map[budgetName]*ratelimit.Limiter {
	limiters := make(map[budgetName]*ratelimit.Limiter, len(budgetSpecs))
	for name, spec := range budgetSpecs {
		limiters[name] = ratelimit.New(spec.limit, spec.window)
	}
	return limiters
}

// rateLimitMiddleware spends the matched route's budget against the
// authenticated caller, refusing with the contract's 429 when the window is
// full. See the file comment for where it sits in the chain and why.
//
// A route with no entry in endpointBudgets is refused with 500 rather than
// served: undeclared means nobody decided.
func (s *apiServer) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := endpointBudgets[r.Pattern]
		if !ok || name == budgetUndeclared {
			// A registered contract route with no rate-limit decision is a
			// programming error; fail closed rather than open.
			slog.Error("route has no rate-limit budget decision",
				"method", r.Method, "path", r.URL.Path, "pattern", r.Pattern)
			writeError(w, r, http.StatusInternalServerError, codeInternalError, msgInternalError)
			return
		}

		limiter, budgeted := s.budgets[name]
		if !budgeted {
			// budgetNone and budgetElsewhere: nothing to spend here.
			next.ServeHTTP(w, r)
			return
		}

		prin, ok := principalFrom(r.Context())
		if !ok {
			// Every budget in the table is per account, so a budgeted route
			// must be session-gated in routePolicies. A principal missing here
			// means the two tables disagree, and serving the request would
			// silently drop the budget.
			internalError(w, r, fmt.Errorf(
				"budget %q needs an account and route %q provides none", name, r.Pattern))
			return
		}

		key := prin.user.ID.String()
		if limiter.Limited(key) {
			writeRateLimited(w, r, limiter.RetryAfter(key))
			return
		}
		limiter.Record(key)

		next.ServeHTTP(w, r)
	})
}
