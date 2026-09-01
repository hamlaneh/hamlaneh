package httpserver_test

// Just-in-time provisioning (ADR 004 slice 4): the callback's resolution
// ladder, driven end to end against the fake provider in
// oidc_handlers_test.go and a real migrated database.
//
// Every rung gets a test, and the ORDER gets one of its own: with
// provisioning ON, an identity whose email names a local password account is
// still refused and no account is created. That is the test that catches a
// refactor which reorders the branches, which is why it asserts the count of
// accounts rather than only the redirect.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// enableJit turns just-in-time provisioning on, the way the org settings
// screen does.
func enableJit(ctx context.Context, t *testing.T, store testdb.Store) {
	t.Helper()
	on := true
	if _, err := store.UpdateOrgSettings(ctx, storage.OrgSettingsPatch{SsoJitProvisioning: &on}); err != nil {
		t.Fatalf("enable sso_jit_provisioning: %v", err)
	}
}

// newScimUser creates a directory-managed account: scim_external_id set is
// what "the directory manages this" means, and it is the one condition that
// lets an email attach an identity.
func (f *ssoFixture) newScimUser(ctx context.Context, t *testing.T, username, email string) storage.User {
	t.Helper()
	externalID := "ext-" + username
	user, err := f.store.CreateScimUser(ctx, storage.NewScimUser{
		Username:     username,
		ScimUserName: email,
		ExternalID:   &externalID,
		Email:        &email,
		Locale:       "en",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("create fixture scim user %s: %v", username, err)
	}
	return user
}

// countUsers is what the ordering tests assert on: a refusal that creates an
// account is the failure they exist to catch, and only a count sees it.
func countUsers(ctx context.Context, t *testing.T, store testdb.Store) int64 {
	t.Helper()
	n, err := store.CountUsers(ctx)
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	return n
}

// auditEventsFor filters the recorded log down to one action.
func auditEventsFor(rec *recordingAudit, action string) []httpserver.AuditEvent {
	out := []httpserver.AuditEvent{}
	for _, ev := range rec.snapshot() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

// TestOidcJitProvisionsAnAccount is rung 4: nothing matches, the setting is
// on, so an account comes into existence from the assertion and signs in.
func TestOidcJitProvisionsAnAccount(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	enableJit(ctx, t, f.store)
	before := countUsers(ctx, t, f.store)

	rec := signInSSO(t, f, "sub-newcomer", "newcomer@corp.example")
	wantSSORedirect(t, rec, "/")

	me := currentUser(t, f.handler, ssoSession(t, rec))
	if me.Username != "newcomer" {
		t.Errorf("username %q, want newcomer derived from the email", me.Username)
	}
	if !me.SsoLinked {
		t.Error("the created account does not report sso_linked")
	}
	if after := countUsers(ctx, t, f.store); after != before+1 {
		t.Errorf("account count %d, want %d — exactly one account", after, before+1)
	}

	created, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-newcomer")
	if err != nil {
		t.Fatalf("the identity does not resolve to the account it created: %v", err)
	}
	if created.ID != me.Id {
		t.Errorf("identity resolves to %s but the session is %s", created.ID, me.Id)
	}
	if !created.IsActive {
		t.Error("the created account is not active")
	}
	if created.PasswordHash != "" {
		t.Error("the created account has a password hash; a provisioned account has no password credential")
	}
	if created.Email == nil || *created.Email != "newcomer@corp.example" {
		t.Errorf("created email = %v, want the claim", created.Email)
	}
	if created.ScimExternalID != nil {
		t.Error("the created account is marked directory-managed; no directory claimed it")
	}

	// The creation is its own entry, and it precedes the sign-in: an
	// operator has to be able to see that the account came into existence
	// here, which "somebody signed in" does not say.
	creations := auditEventsFor(f.audit, "sso.user.created")
	if len(creations) != 1 {
		t.Fatalf("recorded %d sso.user.created events, want 1", len(creations))
	}
	if creations[0].TargetID != created.ID || creations[0].TargetLabel != "newcomer" {
		t.Errorf("sso.user.created target = (%s, %q), want (%s, newcomer)",
			creations[0].TargetID, creations[0].TargetLabel, created.ID)
	}
	if creations[0].Detail["issuer"] != f.idp.issuer() {
		t.Errorf("sso.user.created issuer = %v, want %q", creations[0].Detail["issuer"], f.idp.issuer())
	}
	signIns := signInEvents(f.audit)
	if len(signIns) != 1 || signIns[0].ActorID != created.ID || signIns[0].Detail["method"] != "sso" {
		t.Errorf("sign-ins = %+v, want exactly one sso sign-in by %s", signIns, created.ID)
	}
	if index(f.audit.actions(), "sso.user.created") > index(f.audit.actions(), "user.signed_in") {
		t.Error("the sign-in was recorded before the creation; the log reads backwards")
	}
}

// TestOidcJitOffCreatesNothing is rung 5, and the property the setting
// exists for: with it off the creating branch does not run at all, so single
// sign-on cannot walk around registration being closed.
func TestOidcJitOffCreatesNothing(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	// No enableJit: off is the default a fresh instance boots with.
	before := countUsers(ctx, t, f.store)

	wantSSOFailure(t, signInSSO(t, f, "sub-stranger", "stranger@corp.example"), "sso_account_unknown")
	wantSSOFailure(t, signInSSO(t, f, "sub-anonymous", ""), "sso_account_unknown")

	if after := countUsers(ctx, t, f.store); after != before {
		t.Errorf("account count %d, want %d — a refused identity created an account", after, before)
	}
	if got := len(auditEventsFor(f.audit, "sso.user.created")); got != 0 {
		t.Errorf("recorded %d creations with provisioning off, want 0", got)
	}
	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("refused callbacks recorded %d sign-ins, want 0", got)
	}
}

// TestOidcJitDoesNotReorderTheLadder is the ordering test: rung 3 beats
// rung 4, so a local password account's email is refused with
// sso_account_exists even with provisioning ON, and nothing is created.
//
// Both halves matter. The redirect alone would still pass if the branches
// were swapped and the refusal happened after a create; the count is what
// makes that impossible to miss.
func TestOidcJitDoesNotReorderTheLadder(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	local := f.newPasswordUser(ctx, t, "localacct", "local@corp.example")
	enableJit(ctx, t, f.store)
	before := countUsers(ctx, t, f.store)

	wantSSOFailure(t, signInSSO(t, f, "sub-collider", "local@corp.example"), "sso_account_exists")

	if after := countUsers(ctx, t, f.store); after != before {
		t.Errorf("account count %d, want %d — provisioning made a SECOND account for an address that already has one",
			after, before)
	}
	if got := len(auditEventsFor(f.audit, "sso.user.created")); got != 0 {
		t.Errorf("recorded %d creations, want 0", got)
	}
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-collider"); err == nil {
		t.Error("the identity was linked anyway; an email must never attach itself to a local account")
	}
	after, err := f.store.UserByID(ctx, local.ID)
	if err != nil || after.SsoLinked {
		t.Errorf("local account sso_linked=%v err=%v, want unlinked", after.SsoLinked, err)
	}
	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("a refused callback recorded %d sign-ins, want 0", got)
	}
}

// TestOidcJitDoesNotAdoptAPasswordlessLocalAccount pins the other half of
// rung 3: "local" is about who vouches for the account, not about whether it
// can answer a password prompt. An account with no password and no directory
// behind it is still refused.
func TestOidcJitDoesNotAdoptAPasswordlessLocalAccount(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	enableJit(ctx, t, f.store)

	// The first identity provisions the account; a DIFFERENT identity then
	// arrives claiming the same address.
	wantSSORedirect(t, signInSSO(t, f, "sub-first", "shared@corp.example"), "/")
	before := countUsers(ctx, t, f.store)

	wantSSOFailure(t, signInSSO(t, f, "sub-second", "shared@corp.example"), "sso_account_exists")
	if after := countUsers(ctx, t, f.store); after != before {
		t.Errorf("account count %d, want %d", after, before)
	}
	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-second"); err == nil {
		t.Error("the second identity attached itself to the first one's account")
	}
}

// TestOidcScimManagedAccountAutoLinks is rung 2, the gap slice 2 left
// deliberately: an email match attaches an identity for exactly one kind of
// account — one an administrator already let a directory adopt. It needs no
// provisioning setting, so this runs with that setting OFF.
func TestOidcScimManagedAccountAutoLinks(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	managed := f.newScimUser(ctx, t, "directoryuser", "directory@corp.example")
	before := countUsers(ctx, t, f.store)

	rec := signInSSO(t, f, "sub-directory", "directory@corp.example")
	wantSSORedirect(t, rec, "/")
	if me := currentUser(t, f.handler, ssoSession(t, rec)); me.Id != managed.ID {
		t.Errorf("signed in as %s, want the directory-managed account %s", me.Id, managed.ID)
	}
	if after := countUsers(ctx, t, f.store); after != before {
		t.Errorf("account count %d, want %d — the identity was adopted, not provisioned", after, before)
	}

	linked, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-directory")
	if err != nil || linked.ID != managed.ID {
		t.Fatalf("identity resolves to (%v, %v), want %s", linked.ID, err, managed.ID)
	}

	// An account gaining a door nobody clicked is exactly what the log is
	// for, and the entry says what matched.
	links := auditEventsFor(f.audit, "sso.linked")
	if len(links) != 1 {
		t.Fatalf("recorded %d sso.linked events, want 1", len(links))
	}
	if links[0].TargetID != managed.ID || links[0].Detail["matched_by"] != "scim_external_id" {
		t.Errorf("sso.linked = %+v, want the managed account matched by scim_external_id", links[0])
	}

	// From here it is rung 1: the link exists, so email is never consulted
	// again — the provider may assert a different one and the account is
	// still the same.
	again := signInSSO(t, f, "sub-directory", "renamed@corp.example")
	wantSSORedirect(t, again, "/")
	if me := currentUser(t, f.handler, ssoSession(t, again)); me.Id != managed.ID {
		t.Errorf("second sign-in landed on %s, want %s", me.Id, managed.ID)
	}
}

// TestOidcScimManagedAccountNeedsAVerifiedEmail is the second condition rung
// 2 needs, and the one ADR 004's original wording assumed away.
// scim_external_id proves the ACCOUNT is directory-managed; it says nothing
// about whether the assertion arriving now belongs to the subject making it.
// Only email_verified binds those, so without it a provider that lets a
// person self-assert an address would let anyone who can register a subject
// there put a colleague's address in their profile and be signed in as them.
//
// Both refusals must also leave no account behind: falling through to
// provisioning would make a second account for an address that already has
// one, which is what the ladder exists to prevent. Provisioning is ON here
// for exactly that reason.
func TestOidcScimManagedAccountNeedsAVerifiedEmail(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	managed := f.newScimUser(ctx, t, "verifyuser", "verify@corp.example")
	enableJit(ctx, t, f.store)
	before := countUsers(ctx, t, f.store)

	unverified := []struct {
		name    string
		subject string
		claims  map[string]any
	}{
		{"the provider says the email is not verified", "sub-unverified",
			map[string]any{"email_verified": false}},
		// Absent is not true: a token that never mentions verification has
		// not asserted it, and must not be read as if it had.
		{"the provider does not mention verification", "sub-noclaim",
			map[string]any{"email_verified": nil}},
		// Nor is the string "true" — a real shape from some providers, and
		// the reason the claim is decoded as any rather than bool.
		{"the claim is a string rather than a boolean", "sub-stringly",
			map[string]any{"email_verified": "true"}},
	}
	for _, tt := range unverified {
		t.Run(tt.name, func(t *testing.T) {
			f.idp.overrideNextClaims(tt.claims)
			rec := signInSSO(t, f, tt.subject, "verify@corp.example")
			wantSSOFailure(t, rec, "sso_account_exists")

			if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), tt.subject); err == nil {
				t.Error("an unverified email attached an identity to a directory-managed account")
			}
			if after := countUsers(ctx, t, f.store); after != before {
				t.Errorf("account count %d, want %d — the refusal fell through to provisioning", after, before)
			}
		})
	}

	if got := len(signInEvents(f.audit)); got != 0 {
		t.Errorf("unverified callbacks recorded %d sign-ins, want 0", got)
	}
	after, err := f.store.UserByID(ctx, managed.ID)
	if err != nil || after.SsoLinked {
		t.Errorf("managed account sso_linked=%v err=%v, want unlinked", after.SsoLinked, err)
	}

	// And with the provider actually asserting it, the same account adopts
	// the identity — the guard is a condition, not a wall.
	wantSSORedirect(t, signInSSO(t, f, "sub-verified", "verify@corp.example"), "/")
	linked, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-verified")
	if err != nil || linked.ID != managed.ID {
		t.Fatalf("verified identity resolves to (%v, %v), want %s", linked.ID, err, managed.ID)
	}
}

// TestOidcScimManagedDeactivatedAccountIsNotAdopted: deactivation is how a
// directory offboards somebody, so it must not be possible to attach a door
// to that account while they are gone — and the refusal says nothing about
// the account's state.
func TestOidcScimManagedDeactivatedAccountIsNotAdopted(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	managed := f.newScimUser(ctx, t, "goneuser", "gone@corp.example")
	enableJit(ctx, t, f.store)

	// The last-admin rule needs an admin who can still sign in to exist.
	if _, err := f.store.CreateUser(ctx, storage.NewUser{
		Username: "jitadmin", PasswordHash: password.Hash(totpFixturePassword), Locale: "en", IsAdmin: true,
	}); err != nil {
		t.Fatalf("create fixture admin: %v", err)
	}
	inactive := false
	if _, err := f.store.UpdateUserAdmin(ctx, managed.ID, storage.AdminUserUpdate{IsActive: &inactive}); err != nil {
		t.Fatalf("deactivate fixture user: %v", err)
	}
	before := countUsers(ctx, t, f.store)

	wantSSOFailure(t, signInSSO(t, f, "sub-gone", "gone@corp.example"), "sso_failed")

	if _, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-gone"); err == nil {
		t.Error("an identity was attached to a deactivated account")
	}
	if after := countUsers(ctx, t, f.store); after != before {
		t.Errorf("account count %d, want %d — a second account was made for a deactivated one's address", after, before)
	}
}

// TestOidcJitWithoutEmailClaim: rungs 2 and 3 both consult the email, so a
// token that carries none can only reach rung 4 — and "there is no email"
// must never be evaluated as "this email matches nothing", which would let a
// blank claim be compared against an account.
func TestOidcJitWithoutEmailClaim(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	// An account that also has no email address at all. Nothing about the
	// identity below may resolve to it.
	bystander := f.newPasswordUser(ctx, t, "bystander", "")
	enableJit(ctx, t, f.store)

	first := signInSSO(t, f, "sub-quiet-one", "")
	wantSSORedirect(t, first, "/")
	one := currentUser(t, f.handler, ssoSession(t, first))
	if one.Id == bystander.ID {
		t.Fatal("a token with no email signed in as an account with no email; the blank was matched against something")
	}

	created, err := f.store.UserByID(ctx, one.Id)
	if err != nil {
		t.Fatalf("read the created account: %v", err)
	}
	if created.Email != nil {
		t.Errorf("created email = %v, want none — there was no claim to store", *created.Email)
	}
	if !strings.Contains(created.Username, "sub-quiet-one") {
		t.Errorf("username %q is not derived from the subject, which is all the identity carried", created.Username)
	}

	// A second silent identity is a second person, not the same one.
	second := signInSSO(t, f, "sub-quiet-two", "")
	wantSSORedirect(t, second, "/")
	two := currentUser(t, f.handler, ssoSession(t, second))
	if two.Id == one.Id {
		t.Error("two subjects with no email collapsed into one account")
	}
}

// TestOidcJitDerivesAFreeUsername: the derived name is taken, so the loop
// retries with the next derivation rather than failing. Storage owns the
// uniqueness answer, which is why this is a retry and not a pre-flight look.
func TestOidcJitDerivesAFreeUsername(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	// Same username, different address: this is not the email collision of
	// rung 3, it is a plain name clash on the way to creating an account.
	f.newPasswordUser(ctx, t, "clash", "someone.else@corp.example")
	enableJit(ctx, t, f.store)

	rec := signInSSO(t, f, "sub-clash", "clash@corp.example")
	wantSSORedirect(t, rec, "/")
	if me := currentUser(t, f.handler, ssoSession(t, rec)); me.Username != "clash-1" {
		t.Errorf("username %q, want clash-1 — the next derivation after the taken one", me.Username)
	}
}

// TestOidcJitCreatedAccountObeysOrgPolicy is the "indistinguishable from an
// invited account" property: the session goes through the SAME mint, so the
// organisation's two-step requirement binds at the very first sign-in.
func TestOidcJitCreatedAccountObeysOrgPolicy(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	enableJit(ctx, t, f.store)
	requireTotp(ctx, t, f.store)

	rec := signInSSO(t, f, "sub-policy", "policy@corp.example")
	wantSSORedirect(t, rec, "/")
	sc := ssoSession(t, rec)

	if me := currentUser(t, f.handler, sc); !me.TotpEnrollmentRequired {
		t.Error("a provisioned account's first session is not flagged; provisioning must not bypass org 2FA")
	}
	gated := channelsStatus(t, f.handler, sc)
	wantError(t, gated, http.StatusForbidden, "totp_enrollment_required")
}

// TestOidcJitConcurrentCallbacksMakeOneAccount: two tabs, one identity, both
// arriving at rung 4 at once. The account and its link are one transaction,
// so one of them wins and the other resolves to the winner's account —
// never a second account, and never an internal error.
//
// The identity carries NO email, which is what makes both tabs land on rung
// 4 for certain: with one, the slower tab's email lookup may already see the
// account the faster tab just made and be answered by rung 3 instead. That
// is a correct outcome too — still one account, still a refusal rather than
// an error — but it is not the collision this test is about.
func TestOidcJitConcurrentCallbacksMakeOneAccount(t *testing.T) {
	t.Parallel()

	f := newSSOFixture(t)
	ctx := context.Background()
	enableJit(ctx, t, f.store)
	before := countUsers(ctx, t, f.store)

	// Both flows are started first, so the two callbacks overlap rather
	// than queue behind each other's authorization round trip.
	const tabs = 2
	callbacks := make([]string, tabs)
	txns := make([]*http.Cookie, tabs)
	for i := range tabs {
		authorizeURL, txn := startSSO(t, f.handler)
		callbacks[i], txns[i] = f.idp.grant(t, authorizeURL, "sub-racer", ""), txn
	}

	var wg sync.WaitGroup
	landed := make([]string, tabs)
	sessions := make([]bool, tabs)
	for i := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := completeSSO(t, f.handler, callbacks[i], txns[i])
			landed[i] = rec.Header().Get("Location")
			for _, c := range responseCookies(rec) {
				if c.Name == session.AccessCookie {
					sessions[i] = true
				}
			}
		}()
	}
	wg.Wait()

	if after := countUsers(ctx, t, f.store); after != before+1 {
		t.Errorf("account count %d, want %d — the race made more than one account", after, before+1)
	}
	created, err := f.store.UserByOidcIdentity(ctx, f.idp.issuer(), "sub-racer")
	if err != nil {
		t.Fatalf("the raced identity resolves to nobody: %v", err)
	}
	for i := range tabs {
		if landed[i] != "/" || !sessions[i] {
			t.Errorf("tab %d landed on %q with a session=%v; the loser of the race must sign in to the winner's account (%s)",
				i, landed[i], sessions[i], created.ID)
		}
	}
}

// index reports where action first appears in the recorded log, or -1.
func index(actions []string, action string) int {
	for i, a := range actions {
		if a == action {
			return i
		}
	}
	return -1
}
