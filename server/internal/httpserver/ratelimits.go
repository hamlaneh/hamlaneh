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
// The budgets on public routes break that rule and say so in their own
// declarations: budgetInviteRedeem and budgetSSOFlow. Redeeming an
// invitation and walking the single sign-on flow are public by necessity —
// the session each produces is the reason the request exists — so there is
// no account to key on, and they are keyed on the client address instead
// (budgetSpec.perIP). The argument above still holds for everything else:
// per-IP is the key those routes are forced into, not the one they would
// choose.
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
	budgetAdminSecret       budgetName = "admin-secret"
	budgetInviteRedeem      budgetName = "invite-redeem"
	budgetSSOFlow           budgetName = "sso-flow"
	budgetSSOSettings       budgetName = "sso-settings"
	budgetCallToken         budgetName = "call-token"
	budgetConferenceCreate  budgetName = "conference-create"
	budgetConferenceGuest   budgetName = "conference-guest"
	budgetMlsDirectory      budgetName = "mls-directory"
	budgetMlsGroupWork      budgetName = "mls-group-work"
	budgetMlsMemberDevices  budgetName = "mls-member-devices"
)

// budgetSpec is one budget: how many requests fit its sliding window, how
// long that window is, and — for the one budget on a public route — what it
// is keyed on.
type budgetSpec struct {
	limit  int
	window time.Duration
	// perIP keys this budget on the client address instead of the account.
	// It exists for the public routes — invite redemption and the two SSO
	// flow halves — which are public by necessity, so there is no account
	// to key on, and a budget that demanded one would refuse the request
	// with a 500. Leave it false for anything behind a session — see the
	// file comment on why per account is the right key there.
	perIP bool
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

	// The two admin actions that mint a one-shot secret: a forced password
	// reset and an invitation link. One window per admin across both,
	// because an attacker holding an admin session picks whichever is
	// cheaper for them and per-endpoint budgets would only multiply the
	// total.
	//
	// What is bounded is server work, not guessing: the forced reset runs a
	// full argon2id hash (64 MiB) per call, and every invitation is a row
	// that stays live until it expires. 30 a minute is far past an admin
	// clicking through a row-action menu and far below what either loop
	// needs to hurt.
	budgetAdminSecret: {limit: 30, window: time.Minute},

	// The two public halves of the single sign-on flow share one per-IP
	// window: each start is meant to come back as one callback, so splitting
	// the budget would only multiply the total, and there is no account to
	// key on before the flow completes — the same forced choice as invite
	// redemption.
	//
	// What is bounded is outbound work. The start mints randomness and — on
	// a cold or previously-failed cache — retries provider discovery, so
	// this window is also the backoff on a down provider. The callback
	// triggers a token exchange: an HTTPS round trip to the provider per
	// request, which an unauthenticated caller must not be able to ask for
	// in a loop, both for this server's sake and because unbounded
	// server-to-provider traffic chosen by strangers is a lever.
	//
	// 30 a minute per address absorbs an office's worth of people behind
	// one NAT signing in together at nine in the morning (each sign-in is
	// two requests) — unlike login's budget, EVERY request here spends a
	// unit, so the window must clear a shared-address burst, not just one
	// person's retries. A loop wants far more.
	budgetSSOFlow: {limit: 30, window: time.Minute, perIP: true},

	// The Settings pair — linking and unlinking single sign-on — is one
	// window per account, the same shape as the two-step settings budget
	// beside it: an attacker holding a session picks whichever endpoint is
	// cheaper, and what is bounded is repetition of account-shape changes
	// (each link also mints a provider redirect). 10 in 5 minutes is far
	// above anyone's real reconfiguration and far below a useful loop.
	budgetSSOSettings: {limit: 10, window: 5 * time.Minute},

	// Minting a join ticket is the fourth endpoint in this server that hands
	// back a credential, and the only one a plain member may ask for. What is
	// bounded is repetition: a ticket is a two-minute bearer capability for
	// one room, and a caller who can mint them without limit can keep an
	// arbitrarily long queue of live tickets for a channel they may be
	// removed from at any moment. Signing itself is cheap, so this is about
	// the standing set of live tickets rather than about CPU.
	//
	// 30 a minute is far above the real flow — a ticket is one click, and a
	// media client re-mints only on a rejoin — and low enough that the live
	// set stays roughly one minute's worth.
	budgetCallToken: {limit: 30, window: time.Minute},

	// Making a conference is the fourth endpoint that answers with a one-shot
	// secret, and the first a plain member may ask for. It gets its own
	// window rather than joining budgetAdminSecret: that budget's argument is
	// about an administrator picking the cheapest of two admin-only
	// endpoints, and this is neither admin-only nor part of that choice.
	//
	// What is bounded is the standing set of live doors. A conference link
	// does not expire by default (ADR 005), so every call leaves a row that
	// admits strangers until somebody revokes it — a loop would fill an
	// administrator's revocation list with links they cannot tell apart. 30 a
	// minute per account is far above the real flow, which is one click per
	// meeting, and far below what such a loop needs.
	budgetConferenceCreate: {limit: 30, window: time.Minute},

	// The two public halves of a conference link share one per-IP window, for
	// the reason the SSO flow's two halves do: each preview is meant to come
	// back as one join, so splitting the budget would only multiply the
	// total, and there is no account to key on — the whole point of the
	// endpoint is that the caller may not have one.
	//
	// It is not a guessing defence: the link is 256 bits, and no budget makes
	// that more or less findable. What it bounds is work a stranger can ask
	// for — each preview and each join is a round trip to the media server,
	// and each join mints a ticket. 30 a minute per address absorbs an
	// office's worth of people behind one NAT joining a meeting together
	// (each arrival is two requests) and is far below a useful loop.
	budgetConferenceGuest: {limit: 30, window: time.Minute, perIP: true},

	// The E2EE key-package directory: registering a device and republishing
	// its pool. One window per account across both, because they are the
	// same startup handshake seen twice and an attacker would otherwise pick
	// whichever is cheaper.
	//
	// What is bounded is standing storage. A device row is permanent, and a
	// republish writes up to 50 packages of 8 KiB each — 400 KiB per call
	// that the previous pool's deletion only reclaims for THAT device, so a
	// loop registering fresh devices leaves both behind.
	//
	// 30 a minute is far above the real flow, which is one register and one
	// republish per client startup, and clears a reconnect storm: a client
	// republishes on every connect, and an exponential backoff on a flapping
	// network fits roughly a dozen connects into the first minute. A loop
	// filling the tables wants thousands.
	budgetMlsDirectory: {limit: 30, window: time.Minute},

	// Group work: creating a group, claiming key packages, and submitting
	// commits. One window per account across the three, for the reason the
	// conversation-write budget shares one across its three — they are the
	// steps of a single flow, and per-endpoint budgets would only multiply
	// the total an attacker gets.
	//
	// The number is set by the real burst rather than by a round figure.
	// Bootstrapping a 50-member channel is one create, 50 claims and up to
	// 50 commits — 101 calls in the seconds after the first member's client
	// finds an e2ee channel with no group. On top of that, every commit that
	// loses its epoch race retries, and during a bootstrap several clients
	// may be committing at once, so the budget has to clear 101 with room or
	// it would refuse the one flow it exists to permit. 180 a minute does,
	// and is still far below what filling the commit log needs.
	//
	// The commit body is the expensive half (up to 8 MiB, mls_handlers.go),
	// which is the other reason this is not simply folded into
	// conversation-write: those are one small row each.
	budgetMlsGroupWork: {limit: 180, window: time.Minute},

	// The member-device directory (ADR 007): the roster read a client
	// assembles its eviction allow-list from. It gets its own window rather
	// than joining budgetMlsGroupWork, which is a WRITE budget whose number
	// is argued from commit bodies of up to 8 MiB; this is one indexed range
	// scan over the channel_members primary key.
	//
	// The burst it must clear is set by the protocol rather than by the user,
	// which is what makes it the loosest MLS budget. Every accepted commit in
	// a channel nudges every member, and every nudged client reconciles — so
	// a group bootstrap of many members costs each member's client one read
	// per commit it hears about, not one per action its user took. A large
	// roster then multiplies that by its page count (200 members a page). The
	// idle client beside it spends 12 a minute on the 5-second retry poll.
	//
	// 240 a minute clears that storm with room and is still far below what
	// walking the roster in a loop needs. Making it tighter would fail the
	// one flow it exists to permit, and the failure would be silent in the
	// worst way: a client refused mid-walk assembles a PARTIAL allow-list,
	// and a sweep run against a partial roster evicts the members it never
	// read. So the direction of error matters here — this budget is
	// deliberately generous, and the endpoint is a bounded page over data
	// co-members already learn at claim time, so the disclosure it bounds is
	// nil and the work it bounds is small.
	budgetMlsMemberDevices: {limit: 240, window: time.Minute},

	// Redeeming an invitation is a public route keyed on the client
	// address (like the SSO flow above). It hashes the chosen
	// password with argon2id before the token is known to be good — the same
	// trade password reset makes, and what keeps the account creation and
	// the invite consumption in one transaction — so an unauthenticated
	// caller can otherwise buy 64 MiB of hashing per request.
	//
	// It is not a guessing defence: the token is 256 bits, and no budget
	// makes that more or less findable. 10 in 5 minutes is far above the
	// handful of tries a person needs to land on a username nobody has
	// taken, and far below a loop worth running.
	budgetInviteRedeem: {limit: 10, window: 5 * time.Minute, perIP: true},
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

	// Single sign-on. The two flow halves are the public per-IP window
	// declared above; the Settings pair shares one per-account window.
	// Unlinking is a single indexed delete, but the contract reserves a 429
	// on it and account-shape changes deserve the same posture as the
	// two-step settings.
	"GET /api/v1/auth/oidc/start":    budgetSSOFlow,
	"GET /api/v1/auth/oidc/callback": budgetSSOFlow,
	"POST /api/v1/users/me/oidc":     budgetSSOSettings,
	"DELETE /api/v1/users/me/oidc":   budgetSSOSettings,

	// Session lifecycle. The contract reserves no 429 on any of these and
	// none of them is a lever: refresh and logout are bounded by holding a
	// live token, and users/me is a single indexed read.
	"POST /api/v1/auth/refresh":         budgetNone,
	"POST /api/v1/auth/logout":          budgetNone,
	"POST /api/v1/auth/change-password": budgetNone,
	"GET /api/v1/users/me":              budgetNone,
	// The settings panel's own edit: one row, by the session that owns it,
	// and the contract reserves no 429 on it. A language switch is a click,
	// not a lever.
	"PATCH /api/v1/users/me": budgetNone,

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

	// Phase 1.4 administration. Exactly the three routes the contract
	// reserves a 429 on are budgeted; the rest are reads and idempotent
	// writes an admin already has the authority to make.
	"POST /api/v1/admin/users/{userId}/reset-password": budgetAdminSecret,
	"POST /api/v1/admin/invites":                       budgetAdminSecret,
	"POST /api/v1/invites/{token}":                     budgetInviteRedeem,
	"PATCH /api/v1/admin/users/{userId}":               budgetNone,
	"GET /api/v1/admin/invites":                        budgetNone,
	"DELETE /api/v1/admin/invites/{inviteId}":          budgetNone,
	"GET /api/v1/admin/org":                            budgetNone,
	"PATCH /api/v1/admin/org":                          budgetNone,
	// The preview is public and carries no 429 in the contract. It runs one
	// indexed lookup against a hash and answers the same 404 to everything
	// that is not live, so there is nothing here a budget would protect that
	// the 256-bit token does not already.
	"GET /api/v1/invites/{token}": budgetNone,
	// The audit log is one indexed page per call, read by an admin who
	// already holds the instance; the contract reserves no 429 on it.
	"GET /api/v1/admin/audit": budgetNone,

	// Phase 1.6 SCIM provisioning tokens. Minting one is the third admin
	// action that answers with a one-shot secret, so it joins the window the
	// other two share — an attacker holding an admin session would otherwise
	// have a fresh budget to walk to. The list and the revocation are a read
	// and an indexed update, and the contract reserves no 429 on either.
	//
	// The provisioning surface these credentials open has its own single
	// limiter at the top of its own mux; it cannot live in this table
	// because this table is keyed on contract route patterns and those are
	// not contract routes (scim.md §7).
	"POST /api/v1/admin/scim/tokens":             budgetAdminSecret,
	"GET /api/v1/admin/scim/tokens":              budgetNone,
	"DELETE /api/v1/admin/scim/tokens/{tokenId}": budgetNone,

	// Conversations and messages.
	"GET /api/v1/users":                          budgetDirectory,
	"GET /api/v1/search":                         budgetSearch,
	"POST /api/v1/channels/{channelId}/messages": budgetMessageSend,
	"POST /api/v1/channels/{channelId}/files":    budgetUpload,
	"POST /api/v1/channels":                      budgetConversationWrite,
	"POST /api/v1/dms":                           budgetConversationWrite,
	"POST /api/v1/channels/{channelId}/members":  budgetConversationWrite,

	// Phase 2 calls. The contract reserves a 429 on the ticket and none on
	// the state read, and this table follows it rather than inventing one:
	// the read is what a client does on channel open and after a reconnect,
	// and budgeting it would refuse a reconnect storm the reconnect backoff
	// already paces. It does cost one RoomService round trip per call, which
	// is worth knowing about — if that ever needs bounding, the 429 goes in
	// openapi.yaml first and the budget follows it here.
	"POST /api/v1/channels/{channelId}/call/token": budgetCallToken,
	"GET /api/v1/channels/{channelId}/call":        budgetNone,

	// Phase 2 conferences. Exactly the three routes the contract reserves a
	// 429 on are budgeted. The list is the caller's own view and the
	// revocation is one indexed update by somebody who already holds the
	// authority to make it; both do cost one round trip to the media server,
	// and if that ever needs bounding the 429 goes in openapi.yaml first and
	// the budget follows it here.
	"POST /api/v1/conferences":                  budgetConferenceCreate,
	"GET /api/v1/meet/{token}":                  budgetConferenceGuest,
	"POST /api/v1/meet/{token}/join":            budgetConferenceGuest,
	"GET /api/v1/conferences":                   budgetNone,
	"DELETE /api/v1/conferences/{conferenceId}": budgetNone,

	// Phase 3 slice 1: the E2EE transport (ADR 006). Exactly the five routes
	// the contract reserves a 429 on are budgeted, and the four it does not
	// are declared unbudgeted rather than left out — the reads are what a
	// client does on connect and on channel open, and acknowledging a
	// Welcome is one indexed delete of a row the caller already holds.
	"POST /api/v1/users/me/mls/device":                         budgetMlsDirectory,
	"PUT /api/v1/users/me/mls/devices/{deviceId}/key-packages": budgetMlsDirectory,
	"POST /api/v1/channels/{channelId}/mls/group":              budgetMlsGroupWork,
	"POST /api/v1/channels/{channelId}/mls/key-package-claims": budgetMlsGroupWork,
	"POST /api/v1/channels/{channelId}/mls/commits":            budgetMlsGroupWork,
	"GET /api/v1/users/me/mls/welcomes":                        budgetNone,
	"DELETE /api/v1/users/me/mls/welcomes/{welcomeId}":         budgetNone,
	"GET /api/v1/channels/{channelId}/mls/group":               budgetNone,
	"GET /api/v1/channels/{channelId}/mls/commits":             budgetNone,

	// Phase 3 slice 2: the member-device directory (ADR 007). Unlike the two
	// reads above it, the contract DOES reserve a 429 on this one, and this
	// table follows the contract rather than the family resemblance. The
	// difference is real: those two hand back state a client applies to its
	// own group, while this one is the answer an eviction decision is made
	// from, and a read that important is better with a declared ceiling than
	// with none.
	"GET /api/v1/channels/{channelId}/mls/member-devices": budgetMlsMemberDevices,

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

		key, ok := s.budgetKey(w, r, name)
		if !ok {
			return
		}
		if limiter.Limited(key) {
			writeRateLimited(w, r, limiter.RetryAfter(key))
			return
		}
		limiter.Record(key)

		next.ServeHTTP(w, r)
	})
}

// budgetKey resolves what this budget is spent against: the client address
// for a budget declared perIP, and the authenticated account otherwise.
//
// A per-account budget on a route that provides no principal is refused with
// 500 rather than served. It means routePolicies and budgetSpecs disagree
// about whether the route is authenticated, and serving the request would
// silently drop the budget instead.
func (s *apiServer) budgetKey(w http.ResponseWriter, r *http.Request, name budgetName) (string, bool) {
	if budgetSpecs[name].perIP {
		_, ipKey := clientIP(r)
		return ipKey, true
	}
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, fmt.Errorf(
			"budget %q needs an account and route %q provides none", name, r.Pattern))
		return "", false
	}
	return prin.user.ID.String(), true
}
