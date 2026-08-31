package httpserver

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/oidc"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/totp"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Single sign-on (ADR 004 slice 2): the browser-facing half of the OIDC
// flow. The protocol half — discovery, the authorization URL, the code
// exchange and ID-token verification — lives in internal/oidc; this file
// owns the transaction cookie, the linking state machine, the resolution
// ladder that decides which account a verified identity is (ADR 004 slice 4
// completed it — see resolveUnlinkedIdentity), and the fact that an SSO
// sign-in ends in the SAME session mint as a password sign-in, so the org
// two-step policy and session lifetime bind to it unchanged.

// The transaction cookie: the browser-side state of one in-flight
// authorization round trip. It holds the state the callback compares to the
// query parameter, the nonce the ID token must echo, the PKCE verifier the
// exchange needs, and — for a link — a secret the pending-link row is gated
// on. Every field is server-minted.
//
// The line this whole slice is a lesson in: a value may live in this cookie
// if and only if it is COMPARED against server-side state, never ACTED UPON.
//
//   - Compared (safe unsigned): state against the query parameter, nonce
//     against the verified token, verifier against the provider's PKCE check,
//     link secret against link_secret_hash in the pending row. A forged value
//     just fails its comparison and matches nothing — the cookie is trivially
//     forgeable (a hand-built Cookie header), so its safety cannot rest on
//     integrity, only on the fact that a wrong value grants nothing.
//   - Acted upon (must NEVER live here): anything read to DECIDE, not to
//     match. The link TARGET is the example — which account receives the
//     identity. It was carried here once and was the account-takeover this
//     slice removed: it now lives server-side (oidc_link_requests), created
//     only by an authenticated session, and the cookie's secret merely
//     unlocks that row. The next person to add a field must put it in the
//     compared column or not in this cookie at all.
//
// The link secret earns its place because the state alone does not: state is
// a CSRF nonce and appears in the victim's address bar, history, proxy logs
// and at the provider, so an attacker who OBSERVES an outstanding state could
// forge a cookie carrying it. The secret only ever existed in the browser
// that started the flow, and an attacker who can read that cookie jar already
// holds the session cookie.
//
// It is SameSite=Lax, the ONE deliberate departure from this codebase's
// Strict rule: the callback arrives as a top-level cross-site navigation
// from the provider, and a Strict cookie would not be sent with it, which
// would fail every flow. HttpOnly, Secure and Path constrain what a
// *browser* does with it; they are not the security boundary here (the
// compared-not-acted-upon design above is). It is single-use: the callback
// clears it on every outcome.
const (
	oidcTxnCookieName = "hamlaneh_oidc"
	oidcTxnCookiePath = oidc.CallbackPath
	// oidcTxnTTL bounds one round trip to the provider, including a person
	// typing credentials and touching a second factor there. It is also the
	// lifetime of the pending-link row, whose expiry the consuming DELETE
	// enforces in the database — not the browser dropping the cookie.
	oidcTxnTTL = 10 * time.Minute
)

// Where the callback's redirects land. Every outcome is a redirect (it is
// a browser navigation), and every one lands on the application root — the
// contract's rule, so the server's enumerated client-route list never
// grows and a stale callback URL cannot resolve to a screen implying a
// state its visitor is not in. What differs is one query parameter: none
// when signed in (a completed Settings link also lands bare — the client
// reads sso_linked off users/me), sso=totp when the second factor is
// still owed (the value names the METHOD, so the parameter survives
// WebAuthn arriving beside totp), and sso_error carrying one fixed code on
// failure — never text a provider supplied.
const (
	ssoLandingTarget   = "/"
	ssoChallengeTarget = "/?sso=totp"
	ssoErrorParam      = "sso_error"
)

// oidcTxn is the transaction cookie's payload. Every field is a
// server-minted secret the callback COMPARES against server-side state;
// nothing here is acted upon. LinkSecret is present only for a link flow and
// is checked against link_secret_hash in the pending row — the link TARGET
// itself lives server-side (oidc_link_requests), never here. See the block
// comment above for why that compared-not-acted-upon boundary is load-bearing.
type oidcTxn struct {
	State      string `json:"state"`
	Nonce      string `json:"nonce"`
	Verifier   string `json:"verifier"`
	LinkSecret string `json:"link_secret,omitempty"`
}

// WithSSO installs the OIDC relying party. Omitting it (or passing nil)
// leaves single sign-on unconfigured — the zero-config install — and the
// SSO endpoints answer 503 sso_unavailable.
func WithSSO(svc *oidc.Service) Option {
	return func(s *apiServer) { s.sso = svc }
}

// StartOidcSignIn begins the sign-in flow: 302 to the provider with fresh
// state, nonce and PKCE challenge, and the transaction cookie holding what
// the callback must compare against. No pending-link row is created, so the
// callback treats its return as a sign-in.
func (s *apiServer) StartOidcSignIn(w http.ResponseWriter, r *http.Request) {
	// No link secret: a sign-in creates no pending row, so the cookie carries
	// nothing to unlock one.
	authReq, ok := s.newOidcRedirect(w, r, "")
	if !ok {
		return
	}
	// A redirect, not a JSON body: the sign-in button is a plain top-level
	// navigation to this endpoint.
	http.Redirect(w, r, authReq.URL, http.StatusFound)
}

// LinkOidcIdentity begins the Settings link flow for the signed-in caller:
// the same transaction as a sign-in, plus a server-side pending-link row
// naming the caller's own account. Answered as JSON because the client
// calls it with fetch and follows the URL itself.
//
// The account id goes in the database, keyed by the state's hash — NOT in
// the transaction cookie. Only this endpoint, behind the session gate,
// creates such a row, and only for its own authenticated caller, so the
// callback's link target is something an attacker composing a request by
// hand cannot manufacture. A fresh secret, minted here and carried in the
// cookie, gates that row: the state can be observed, so it alone must not be
// enough to complete the link (see ConsumeOidcLinkRequest).
func (s *apiServer) LinkOidcIdentity(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("sso link reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}
	if prin.user.SsoLinked {
		writeError(w, r, http.StatusConflict, codeSSOAlreadyLinked,
			"this account already has a single sign-on identity linked")
		return
	}

	// session.NewToken gives the raw secret (for the cookie) and its SHA-256
	// (for the row) in one call — the same one-shot-credential shape the
	// session, reset, invite and challenge tokens already use here.
	secretRaw, secretHash := session.NewToken()
	authReq, ok := s.newOidcRedirect(w, r, secretRaw)
	if !ok {
		// A stray pending row is never created before this point, so a 503
		// here leaves nothing behind.
		return
	}
	if err := store.CreateOidcLinkRequest(r.Context(),
		session.HashToken(authReq.State), secretHash, prin.user.ID, oidcTxnTTL); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSONValue(w, r, http.StatusOK, api.OidcRedirect{RedirectUrl: authReq.URL})
}

// newOidcRedirect is the shared start of both flows: it mints the
// authorization request and sets the transaction cookie, whose LinkSecret is
// linkSecret ("" for a sign-in, which creates no pending row for it to
// unlock). A nil service or a failing discovery answers 503 sso_unavailable
// itself — password sign-in is untouched either way — and reports false.
func (s *apiServer) newOidcRedirect(w http.ResponseWriter, r *http.Request, linkSecret string) (oidc.AuthRequest, bool) {
	if s.sso == nil {
		writeSSOUnavailable(w, r)
		return oidc.AuthRequest{}, false
	}
	authReq, err := s.sso.NewAuthRequest(r.Context())
	if err != nil {
		// The provider being down must not read as a server fault: 503 with
		// the same code as "not configured", details in the log only.
		slog.Warn("sso provider discovery failed", "error", err)
		writeSSOUnavailable(w, r)
		return oidc.AuthRequest{}, false
	}

	value, err := encodeOidcTxn(oidcTxn{
		State:      authReq.State,
		Nonce:      authReq.Nonce,
		Verifier:   authReq.Verifier,
		LinkSecret: linkSecret,
	})
	if err != nil {
		internalError(w, r, err)
		return oidc.AuthRequest{}, false
	}
	http.SetCookie(w, oidcTxnCookie(value, int(oidcTxnTTL.Seconds())))
	return authReq, true
}

// CompleteOidcSignIn is the callback the provider redirects back to. Every
// outcome is a redirect; every failure carries one fixed code; the
// transaction cookie is single-use and cleared on every path.
//
// The order is the security design: the state is compared against the
// cookie before ANYTHING else is looked at — before the provider's error
// parameter, before the code — because a callback that fails the state
// comparison is not from the browser that started a flow, and nothing it
// carries deserves an answer.
func (s *apiServer) CompleteOidcSignIn(w http.ResponseWriter, r *http.Request, params api.CompleteOidcSignInParams) {
	if s.sso == nil {
		writeSSOUnavailable(w, r)
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	// Single-use: whatever happens below, the transaction is over.
	txnCookie, cookieErr := r.Cookie(oidcTxnCookieName)
	http.SetCookie(w, oidcTxnCookie("", -1))

	if cookieErr != nil || txnCookie.Value == "" {
		ssoFail(w, r, codeSSOFailed)
		return
	}
	txn, err := decodeOidcTxn(txnCookie.Value)
	if err != nil {
		ssoFail(w, r, codeSSOFailed)
		return
	}

	// State: against the cookie, constant-time, before anything else. Not
	// merely "present" — present-but-different is an attack, not a typo.
	if params.State == nil ||
		subtle.ConstantTimeCompare([]byte(txn.State), []byte(*params.State)) != 1 {
		ssoFail(w, r, codeSSOFailed)
		return
	}

	if params.Error != nil && *params.Error != "" {
		// Provider-supplied text goes to the server log and nowhere else:
		// not into the redirect URL, not into a page.
		slog.Warn("sso provider returned an error", "provider_error", *params.Error)
		ssoFail(w, r, codeSSOFailed)
		return
	}
	if params.Code == nil || *params.Code == "" {
		ssoFail(w, r, codeSSOFailed)
		return
	}

	ident, err := s.sso.Exchange(r.Context(), *params.Code, txn.Verifier, txn.Nonce)
	if err != nil {
		// Covers the exchange, every ID-token verification failure (issuer,
		// audience, signature, algorithm, expiry) and the nonce comparison.
		// One log line, one fixed code: which check failed is for the
		// operator, never for the caller.
		slog.Warn("sso callback rejected", "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}

	// Link vs sign-in is decided by SERVER state, never the cookie: a
	// pending-link row keyed by this state's hash AND gated on the cookie's
	// link secret means an authenticated caller asked to link it, to their
	// own account, from the browser that started the flow. Both the state
	// hash and the secret hash must match — the state can be observed, so the
	// secret is what an attacker forging a cookie cannot supply. The DELETE is
	// atomic (single-use and expiry enforced by the one statement), so a
	// replayed, stale, or secret-less callback finds nothing and falls through
	// to sign-in. A sign-in flow carries no secret; its empty hash matches no
	// live row, which is the same fall-through.
	linkUserID, err := store.ConsumeOidcLinkRequest(r.Context(),
		session.HashToken(txn.State), session.HashToken(txn.LinkSecret))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		slog.Error("sso link request lookup", "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}
	if err == nil {
		s.completeOidcLink(w, r, store, linkUserID, ident)
		return
	}
	s.completeOidcLogin(w, r, store, ident)
}

// completeOidcLogin finishes a sign-in intent. Rung 1 of the contract's
// resolution ladder is here — the identity is already linked, so email is
// never consulted — and rungs 2 to 5 are resolveUnlinkedIdentity's. Whichever
// rung answers, the account then goes through the SAME session mint as a
// password sign-in: advisory lock, org session lifetime, the two-step
// enrolment flag decided at mint, and the audit record. That is what makes a
// just-in-time account indistinguishable from an invited one the moment it
// exists; a second mint path for created accounts would be a policy hole.
func (s *apiServer) completeOidcLogin(w http.ResponseWriter, r *http.Request, store Store, ident oidc.Identity) {
	user, err := store.UserByOidcIdentity(r.Context(), ident.Issuer, ident.Subject)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		// Rungs 2 to 5. Every refusal is answered in there; false means the
		// browser has already been redirected.
		var resolved bool
		user, resolved = s.resolveUnlinkedIdentity(w, r, store, ident)
		if !resolved {
			return
		}
	case err != nil:
		slog.Error("sso identity lookup", "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}
	if !user.IsActive {
		// Same opacity rule as password login: a deactivated account is not
		// a state an unauthenticated caller gets to learn, so the answer is
		// the generic failure, with the reason in the log.
		slog.Warn("sso sign-in refused for deactivated account", "user_id", user.ID)
		ssoFail(w, r, codeSSOFailed)
		return
	}

	// An account with an activated second factor gets the existing two-step
	// challenge — same storage row, same cookie — and lands on the
	// challenge screen instead of holding a session. A policy screen that
	// silently exempted a whole sign-in method would be a lie (ADR 004).
	raw, challenged, err := startTotpChallenge(r.Context(), store, user.ID)
	if err != nil {
		slog.Error("sso two-step challenge", "user_id", user.ID, "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}
	if challenged {
		http.SetCookie(w, challengeCookie(raw, int(totp.ChallengeTTL.Seconds())))
		http.Redirect(w, r, ssoChallengeTarget, http.StatusFound)
		return
	}

	addr, _ := s.clientIP(r)
	tokens, cookies := mintSessionTokens()
	tokens.UserAgent = sanitizedUserAgent(r)
	tokens.IP = ipParam(addr)
	if _, err := store.CreateSession(r.Context(), storage.NewSession{
		UserID:        user.ID,
		SessionTokens: tokens,
	}); err != nil {
		slog.Error("sso session mint", "user_id", user.ID, "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}

	s.recordSignIn(r, user, "sso")
	session.SetCookies(w, cookies)
	http.Redirect(w, r, ssoLandingTarget, http.StatusFound)
}

// resolveUnlinkedIdentity answers an identity linked to nobody: rungs 2 to 5
// of the contract's resolution ladder, in that order, because the ORDER is
// the security property and not an implementation detail.
//
//  2. the provider VERIFIED the email and it names a directory-managed
//     account — link it and sign in;
//  3. the email names any other account, or was not verified — refuse
//     sso_account_exists, whatever sso_jit_provisioning says;
//  4. nothing matched and JIT is on — create an account and sign in;
//  5. nothing matched and JIT is off — refuse sso_account_unknown.
//
// Rung 3 sitting above rung 4 is what stops JIT from being a way around a
// local password account: auto-linking would fuse the weakest email
// assertion on either side into a session, and creating a SECOND account for
// an address that already has one is worse than refusing (ADR 004 §D3). The
// structure is what enforces it — rung 4 is only reachable when the email
// matched nothing at all — so a future edit that reorders the branches
// changes the shape of this function rather than slipping past a check.
//
// It reports false when it has answered the browser itself; true carries the
// account the caller signs in.
func (s *apiServer) resolveUnlinkedIdentity(w http.ResponseWriter, r *http.Request, store Store, ident oidc.Identity) (storage.User, bool) {
	// Rungs 2 and 3 both consult the email claim, so with nothing to consult
	// neither of them can hold and an absent claim goes straight to rung 4.
	// The emptiness is tested HERE rather than left to a lookup of "": "the
	// token carried no email" and "the email matches nobody" are different
	// facts, and letting the first arrive at the second's branch is how a
	// blank claim would end up compared against something.
	if ident.Email != "" {
		existing, err := store.UserByEmail(r.Context(), ident.Email)
		switch {
		case err == nil:
			return s.adoptOrRefuse(w, r, store, existing, ident)
		case !errors.Is(err, storage.ErrNotFound):
			slog.Error("sso identity email lookup", "error", err)
			ssoFail(w, r, codeSSOFailed)
			return storage.User{}, false
		}
	}
	return s.provisionIdentity(w, r, store, ident)
}

// adoptOrRefuse decides rung 2 against rung 3 for an identity whose email
// names the account existing.
//
// Rung 2 is the ONE case ADR 004 permits an email to attach an identity, and
// it needs TWO conditions that come from two different statements:
//
//   - scim_external_id set — an administrator minted the token that let a
//     directory adopt this account, so the account side of the match rests
//     on authority they granted; and
//   - the provider verified the incoming email — which is the only thing
//     that binds "this subject owns this address". Being directory-managed
//     says nothing about the assertion arriving now; a provider that lets a
//     person self-assert an unverified address would otherwise let anyone
//     who can register a subject there put a colleague's address in their
//     profile and be signed in as them.
//
// Missing either one is rung 3's refusal, which is safe: the account exists,
// its owner reaches it the way they already can and connects single sign-on
// from Settings, where a live session — not an email — is the authority for
// the link. Note that rung 3 itself is NOT gated on verification. Refusing
// an unverified-but-matching email is strictly safer than not, and gating
// the refusal would drop those identities into rung 4, where they would
// create a second account for an address that already has one.
//
// Every other account is rung 3, including one with no password at all:
// whether it can answer a password prompt is not what makes a match
// trustworthy.
func (s *apiServer) adoptOrRefuse(w http.ResponseWriter, r *http.Request, store Store, existing storage.User, ident oidc.Identity) (storage.User, bool) {
	if existing.ScimExternalID == nil || !ident.EmailVerified {
		if existing.ScimExternalID != nil {
			// Directory-managed, so the unverified email is the only thing
			// that stopped the link. Worth a line: from the outside this is
			// indistinguishable from an ordinary rung-3 refusal, and an
			// operator wondering why adoption never happens has nothing
			// else to look at.
			slog.Warn("sso auto-link refused: the provider did not verify the asserted email",
				"user_id", existing.ID)
		}
		ssoFail(w, r, codeSSOAccountExists)
		return storage.User{}, false
	}
	if !existing.IsActive {
		// The same opacity a deactivated account gets everywhere else, and
		// no identity is attached to an account that cannot sign in:
		// deactivation is how a directory offboards somebody, and
		// reactivation must not silently arrive with a door opened while
		// they were gone.
		slog.Warn("sso auto-link refused for deactivated account", "user_id", existing.ID)
		ssoFail(w, r, codeSSOFailed)
		return storage.User{}, false
	}

	if err := store.LinkOidcIdentity(r.Context(), existing.ID,
		ident.Issuer, ident.Subject, &ident.Email); err != nil {
		// A second tab linking first, or an identity that already belongs to
		// another account. Neither distinction is the caller's to learn from
		// a redirect; the log carries it.
		slog.Warn("sso auto-link refused", "user_id", existing.ID, "error", err)
		ssoFail(w, r, codeSSOFailed)
		return storage.User{}, false
	}

	// An account gaining a second door is a fact an operator has to be able
	// to read, and this one nobody clicked: the actor is the system, as it
	// is for every SCIM-driven change (docs/api/scim.md §8).
	s.record(r, AuditEvent{
		Action:      "sso.linked",
		TargetID:    existing.ID,
		TargetLabel: existing.Username,
		Detail:      map[string]any{"issuer": ident.Issuer, "matched_by": "scim_external_id"},
	})
	return existing, true
}

// maxJitUsernameAttempts bounds the username derivation retry, exactly as
// SCIM provisioning bounds its own. Each attempt is one INSERT, and the
// bound exists so that a pathological run of collisions ends in a refusal
// somebody can read rather than in a loop.
const maxJitUsernameAttempts = 20

// provisionIdentity decides rung 4 against rung 5 for an identity that
// matched nothing at all.
func (s *apiServer) provisionIdentity(w http.ResponseWriter, r *http.Request, store Store, ident oidc.Identity) (storage.User, bool) {
	settings, err := store.OrgSettings(r.Context())
	if err != nil {
		slog.Error("sso provisioning settings read", "error", err)
		ssoFail(w, r, codeSSOFailed)
		return storage.User{}, false
	}
	if !settings.SsoJitProvisioning {
		// Rung 5: nothing below runs. "Single sign-on cannot walk around
		// registration being closed" is true because the creating branch is
		// not entered, not because something afterwards checks it.
		ssoFail(w, r, codeSSOAccountUnknown)
		return storage.User{}, false
	}

	// Rung 4. The derivation source is the email when the token carried one
	// and the subject otherwise — both are per-identity, and DeriveUsername
	// turns either into a name uservalidate.Username accepts.
	source := ident.Email
	if source == "" {
		source = ident.Subject
	}
	newUser := storage.NewOidcUser{
		Locale:  settings.DefaultLocale,
		Issuer:  ident.Issuer,
		Subject: ident.Subject,
	}
	if ident.Email != "" {
		newUser.Email = &ident.Email
	}

	// Storage owns the uniqueness answer, so a taken name is a retry with
	// the next derivation rather than a pre-flight check a concurrent create
	// could invalidate between the look and the insert.
	var created storage.User
	for attempt := range maxJitUsernameAttempts {
		newUser.Username = uservalidate.DeriveUsername(source, attempt)
		created, err = store.CreateOidcUser(r.Context(), newUser)
		if !errors.Is(err, storage.ErrUsernameTaken) {
			break
		}
	}
	switch {
	case errors.Is(err, storage.ErrOidcIdentityTaken), errors.Is(err, storage.ErrEmailTaken):
		// Two tabs, one identity, both here at once. The create is one
		// transaction, so exactly one of them made the account; this is the
		// loser, and it signs in to what the winner made instead of making a
		// second account or answering with an internal error.
		raced, lookupErr := store.UserByOidcIdentity(r.Context(), ident.Issuer, ident.Subject)
		if lookupErr != nil {
			// The collision was somebody else's account, not the other tab's
			// — an email now held by an account this identity is not linked
			// to. Signing in is exactly what must not happen.
			slog.Warn("sso just-in-time provisioning collided", "error", err)
			ssoFail(w, r, codeSSOFailed)
			return storage.User{}, false
		}
		return raced, true
	case errors.Is(err, storage.ErrUsernameTaken):
		// Every derivation of this identity is already held by somebody. The
		// redirect's code set is closed, so the operator's answer is in the
		// log — but it is a named refusal, not a 500 with nothing in it.
		slog.Warn("sso just-in-time provisioning could not derive a free username",
			"last_candidate", newUser.Username)
		ssoFail(w, r, codeSSOFailed)
		return storage.User{}, false
	case err != nil:
		slog.Error("sso just-in-time provisioning", "error", err)
		ssoFail(w, r, codeSSOFailed)
		return storage.User{}, false
	}

	// The creation is its own entry, distinct from the sign-in that follows
	// it: an operator reading the log has to be able to see that an account
	// came into existence from a provider assertion, which "somebody signed
	// in" does not say. The actor is the system — nobody here was signed in.
	s.record(r, AuditEvent{
		Action:      "sso.user.created",
		TargetID:    created.ID,
		TargetLabel: created.Username,
		Detail:      map[string]any{"issuer": ident.Issuer},
	})
	return created, true
}

// completeOidcLink finishes a link intent: the identity is recorded against
// the account whose id the link endpoint bound into the transaction cookie.
// No session is minted and none is read — the account id in the
// server-minted cookie is the whole authority, because the Strict session
// cookie does not accompany this cross-site navigation.
func (s *apiServer) completeOidcLink(w http.ResponseWriter, r *http.Request, store Store, userID uuid.UUID, ident oidc.Identity) {
	// Read the account first: the audit entry needs its name, and a link
	// must not attach to an account that was deactivated or deleted while
	// the browser was away at the provider.
	user, err := store.UserByID(r.Context(), userID)
	if err != nil || !user.IsActive {
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			slog.Error("sso link account read", "error", err)
		}
		ssoFail(w, r, codeSSOFailed)
		return
	}

	var emailAtLink *string
	if ident.Email != "" {
		emailAtLink = &ident.Email
	}
	err = store.LinkOidcIdentity(r.Context(), userID, ident.Issuer, ident.Subject, emailAtLink)
	switch {
	case errors.Is(err, storage.ErrOidcAccountLinked), errors.Is(err, storage.ErrOidcIdentityTaken):
		// A second tab finishing first, or an identity that already belongs
		// to another account. Neither distinction is the caller's to learn
		// from a redirect; the log carries it.
		slog.Warn("sso link refused", "user_id", userID, "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	case err != nil:
		slog.Error("sso link", "user_id", userID, "error", err)
		ssoFail(w, r, codeSSOFailed)
		return
	}

	// ActorID explicitly: this route has no principal, and the person who
	// acted is the account whose authenticated session created the
	// pending-link row this callback just consumed.
	s.record(r, AuditEvent{
		Action:      "sso.linked",
		ActorID:     userID,
		TargetID:    userID,
		TargetLabel: user.Username,
		Detail:      map[string]any{"issuer": ident.Issuer},
	})
	http.Redirect(w, r, ssoLandingTarget, http.StatusFound)
}

// UnlinkOidcIdentity disconnects single sign-on from the caller's account.
// Refused when the account has no password: unlinking would leave it with
// no way in at all, and the recovery path for such an account is an
// administrator issuing a temporary password (the contract).
func (s *apiServer) UnlinkOidcIdentity(w http.ResponseWriter, r *http.Request) {
	prin, ok := principalFrom(r.Context())
	if !ok {
		internalError(w, r, errors.New("sso unlink reached without principal"))
		return
	}
	store, ok := s.requireStore(w, r)
	if !ok {
		return
	}

	if !prin.user.SsoLinked {
		writeSSONotLinked(w, r)
		return
	}
	// PasswordHash is empty only for an account that never had one: the
	// password-less accounts a directory provisions and just-in-time
	// provisioning creates, for which single sign-on is the only door.
	// Checked after the linked check: an account with neither a password nor
	// a link gets the honest "nothing to unlink".
	if prin.user.PasswordHash == "" {
		writeError(w, r, http.StatusConflict, codeSSOUnlinkNoPassword,
			"single sign-on is this account's only way in; set a password first")
		return
	}

	err := store.UnlinkOidcIdentity(r.Context(), prin.user.ID)
	if errors.Is(err, storage.ErrNotFound) {
		// Raced away between the check and the delete; same honest answer.
		writeSSONotLinked(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}

	s.record(r, AuditEvent{
		Action:      "sso.unlinked",
		TargetID:    prin.user.ID,
		TargetLabel: prin.user.Username,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ssoFail sends the browser back to the sign-in screen with one fixed
// machine-readable code in the query. The code set is closed and
// server-chosen; provider-supplied text never reaches this URL.
func ssoFail(w http.ResponseWriter, r *http.Request, code errorCode) {
	http.Redirect(w, r, ssoLandingTarget+"?"+ssoErrorParam+"="+url.QueryEscape(string(code)),
		http.StatusFound)
}

// writeSSOUnavailable is the single source of the 503: not configured and
// provider-unreachable answer identically, and password sign-in is
// unaffected by either.
func writeSSOUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, codeSSOUnavailable,
		"single sign-on is not available")
}

func writeSSONotLinked(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, codeSSONotLinked,
		"no single sign-on identity is linked to this account")
}

// encodeOidcTxn and decodeOidcTxn serialize the transaction cookie:
// base64url over JSON, no signature. That is safe here for one reason and
// only that reason: every field is COMPARED against a value from elsewhere
// — state against the query parameter, nonce against the verified token,
// verifier against the provider's PKCE check — so a forged value fails the
// comparison and grants nothing. It holds NOTHING that is acted upon. The
// link target used to live here and was the account-takeover this design
// removed: it is now server-side state (oidc_link_requests) an
// authenticated session creates. Nothing acted upon may ever be added back
// to this cookie; decodeOidcTxn ignoring unknown JSON fields is deliberate,
// so a stray one from an older or hostile client changes nothing.
func encodeOidcTxn(txn oidcTxn) (string, error) {
	raw, err := json.Marshal(txn)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeOidcTxn(value string) (oidcTxn, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return oidcTxn{}, err
	}
	var txn oidcTxn
	if err := json.Unmarshal(raw, &txn); err != nil {
		return oidcTxn{}, err
	}
	return txn, nil
}

// oidcTxnCookie builds the transaction cookie; see the block comment at the
// top of this file for why it is the one SameSite=Lax cookie this server
// sets. Attributes must match between setting and clearing it.
func oidcTxnCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     oidcTxnCookieName,
		Value:    value,
		Path:     oidcTxnCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}
