package scim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/scim"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The provisioning surface end to end, against a real database. Storage and
// authorization code are tested against real PostgreSQL and never mocks
// (CLAUDE.md testing policy), and here that is not a formality: the
// uniqueness answers, the username derivation loop and the last-administrator
// refusal are all facts the database decides.

// fixture is a running provisioning surface plus the store behind it.
type fixture struct {
	t     *testing.T
	store testdb.Store
	srv   *httptest.Server
	log   *recordingAudit
	token string
	admin storage.User
}

// recordingAudit keeps what the surface recorded, so §8's actions can be
// asserted rather than assumed.
type recordingAudit struct {
	mu      sync.Mutex
	records []audit.Record
}

func (a *recordingAudit) Record(_ context.Context, rec audit.Record) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, rec)
}

func (a *recordingAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.records))
	for _, rec := range a.records {
		out = append(out, rec.Action)
	}
	return out
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	store, _ := testdb.New(t)
	ctx := context.Background()

	admin, err := store.CreateUser(ctx, storage.NewUser{
		Username:     "instanceadmin",
		PasswordHash: password.Hash("an instance admin passphrase"),
		Locale:       "en",
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create instance admin: %v", err)
	}
	log := &recordingAudit{}
	srv := httptest.NewServer(scim.New(store, scim.WithAudit(log)))
	t.Cleanup(srv.Close)

	return &fixture{t: t, store: store, srv: srv, log: log, token: mintToken(t, store, admin.ID), admin: admin}
}

// mintToken creates one live provisioning credential and returns the value a
// provider would present.
func mintToken(t *testing.T, store testdb.Store, adminID uuid.UUID) string {
	t.Helper()

	raw, hash := session.NewToken()
	if _, err := store.CreateScimToken(context.Background(), adminID, hash, "integration"); err != nil {
		t.Fatalf("mint provisioning token: %v", err)
	}
	return raw
}

// do sends an authenticated request and returns the status and the decoded
// body. Every test goes through the real HTTP surface rather than calling a
// handler, because the authentication and the budget are part of what is
// being tested.
func (f *fixture) do(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	return f.doAs(method, path, body, "Bearer "+f.token)
}

func (f *fixture) doAs(method, path, body, authorization string) (int, map[string]any) {
	f.t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.srv.URL+path, reader)
	if err != nil {
		f.t.Fatalf("build request: %v", err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.Header.Set("Content-Type", scim.ContentType)

	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatalf("send request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.t.Errorf("close response: %v", closeErr)
		}
	}()

	decoded := map[string]any{}
	if resp.StatusCode != http.StatusNoContent {
		// Every response that carries a body carries the SCIM media type —
		// the errors included, because a sync engine parses those with the
		// same reader it parses a resource with. A 204 has no body and
		// therefore no type.
		if got := resp.Header.Get("Content-Type"); got != scim.ContentType {
			f.t.Errorf("%s %s answered Content-Type %q, want %q", method, path, got, scim.ContentType)
		}
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			f.t.Fatalf("decode %s %s body: %v", method, path, err)
		}
	}
	return resp.StatusCode, decoded
}

// createUser provisions one account and returns its id.
func (f *fixture) createUser(userName, externalID string) string {
	f.t.Helper()

	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],` +
		`"userName":"` + userName + `","externalId":"` + externalID + `",` +
		`"displayName":"Test Person",` +
		`"emails":[{"value":"` + userName + `","primary":true}],"active":true}`
	status, got := f.do(http.MethodPost, "/scim/v2/Users", body)
	if status != http.StatusCreated {
		f.t.Fatalf("create %s answered %d: %v", userName, status, got)
	}
	id, _ := got["id"].(string)
	if id == "" {
		f.t.Fatalf("create %s returned no id: %v", userName, got)
	}
	return id
}

// account reads one account back from storage.
func (f *fixture) account(id string) storage.User {
	f.t.Helper()

	user, err := f.store.UserByID(context.Background(), uuid.MustParse(id))
	if err != nil {
		f.t.Fatalf("read account %s: %v", id, err)
	}
	return user
}

func TestCreateProvisionsAnAccount(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	id := f.createUser("amir.dezyani@example.com", "00u1abc")

	user := f.account(id)
	// The provider's value is stored verbatim and the LOCAL username is
	// derived from it — the account rules do not accept an email address
	// (§4).
	if user.ScimUserName == nil || *user.ScimUserName != "amir.dezyani@example.com" {
		t.Errorf("scim_user_name = %v", user.ScimUserName)
	}
	if user.Username != "amir.dezyani" {
		t.Errorf("derived username = %q, want amir.dezyani", user.Username)
	}
	if user.ScimExternalID == nil || *user.ScimExternalID != "00u1abc" {
		t.Errorf("scim_external_id = %v", user.ScimExternalID)
	}
	if user.Email == nil || *user.Email != "amir.dezyani@example.com" {
		t.Errorf("email = %v", user.Email)
	}
	if !user.IsActive {
		t.Error("a provisioned account should start active")
	}
	// The account has no password credential, which is exactly what
	// migration 0014 made possible.
	if user.PasswordHash != "" {
		t.Error("a provisioned account was given a password hash")
	}
	if !containsAction(f.log.actions(), "scim.user.created") {
		t.Errorf("audit actions = %v, want scim.user.created", f.log.actions())
	}
}

// TestCreateSuffixesADerivedUsernameCollision pins the retry loop. Two
// different directory addresses can derive one local username, and the
// second must still get an account.
func TestCreateSuffixesADerivedUsernameCollision(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	first := f.account(f.createUser("amir@example.com", "ext-1"))
	second := f.account(f.createUser("amir@other.example", "ext-2"))

	if first.Username != "amir" {
		t.Errorf("first username = %q, want amir", first.Username)
	}
	if second.Username == first.Username {
		t.Fatalf("both accounts derived the username %q", second.Username)
	}
	if !strings.HasPrefix(second.Username, "amir-") {
		t.Errorf("second username = %q, want a suffixed amir", second.Username)
	}
}

// TestCreateRefusesADuplicate is the first half of adopting an account (§4):
// the create answers 409 and the provider's lookup then finds the existing
// row.
func TestCreateRefusesADuplicate(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.createUser("amir@example.com", "ext-1")

	body := `{"userName":"amir@example.com","externalId":"ext-9"}`
	status, got := f.do(http.MethodPost, "/scim/v2/Users", body)
	if status != http.StatusConflict {
		t.Fatalf("duplicate create answered %d, want 409: %v", status, got)
	}
	if got["scimType"] != "uniqueness" {
		t.Errorf("scimType = %v, want uniqueness", got["scimType"])
	}
}

// TestFilterFindsAnAccountByEmail is the second half of adoption: a
// userName filter matches scim_user_name OR email, which is what lets a
// provider take over an account somebody already made locally.
func TestFilterFindsAnAccountByEmail(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	local := "local@example.com"
	existing, err := f.store.CreateUser(ctx, storage.NewUser{
		Username:     "localperson",
		Email:        &local,
		PasswordHash: password.Hash("a local passphrase"),
		Locale:       "en",
	})
	if err != nil {
		t.Fatalf("create local account: %v", err)
	}

	status, got := f.do(http.MethodGet, `/scim/v2/Users?filter=userName+eq+%22local@example.com%22`, "")
	if status != http.StatusOK {
		t.Fatalf("filtered list answered %d: %v", status, got)
	}
	if total, _ := got["totalResults"].(float64); total != 1 {
		t.Fatalf("totalResults = %v, want 1: %v", got["totalResults"], got)
	}
	resources, _ := got["Resources"].([]any)
	first, _ := resources[0].(map[string]any)
	if first["id"] != existing.ID.String() {
		t.Errorf("found %v, want the local account %s", first["id"], existing.ID)
	}
	// Before adoption the account has no provider userName, so SCIM shows
	// the local one rather than an empty string.
	if first["userName"] != "localperson" {
		t.Errorf("userName = %v, want the local username", first["userName"])
	}

	// The adopting write: externalId lands, and the account is now
	// directory-managed.
	adopt := `{"userName":"local@example.com","externalId":"00uADOPT",` +
		`"emails":[{"value":"local@example.com","primary":true}]}`
	status, got = f.do(http.MethodPut, "/scim/v2/Users/"+existing.ID.String(), adopt)
	if status != http.StatusOK {
		t.Fatalf("adopting replace answered %d: %v", status, got)
	}
	adopted := f.account(existing.ID.String())
	if adopted.ScimExternalID == nil || *adopted.ScimExternalID != "00uADOPT" {
		t.Errorf("externalId after adoption = %v", adopted.ScimExternalID)
	}
	// The local username is untouched: adoption changes who manages the
	// account, not what it is called on every screen.
	if adopted.Username != "localperson" {
		t.Errorf("local username changed to %q on adoption", adopted.Username)
	}
}

func TestListFilterAndPaging(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	for _, name := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		f.createUser(name, "ext-"+name)
	}

	t.Run("externalId filter", func(t *testing.T) {
		status, got := f.do(http.MethodGet, `/scim/v2/Users?filter=externalId+eq+%22ext-b@example.com%22`, "")
		if status != http.StatusOK {
			t.Fatalf("answered %d: %v", status, got)
		}
		if total, _ := got["totalResults"].(float64); total != 1 {
			t.Errorf("totalResults = %v, want 1", got["totalResults"])
		}
	})

	t.Run("an unsupported filter is refused rather than ignored", func(t *testing.T) {
		status, got := f.do(http.MethodGet, `/scim/v2/Users?filter=displayName+co+%22Test%22`, "")
		if status != http.StatusBadRequest {
			t.Fatalf("answered %d, want 400: %v", status, got)
		}
		if got["scimType"] != "invalidFilter" {
			t.Errorf("scimType = %v, want invalidFilter", got["scimType"])
		}
	})

	t.Run("paging walks the whole directory", func(t *testing.T) {
		status, got := f.do(http.MethodGet, "/scim/v2/Users?startIndex=2&count=2", "")
		if status != http.StatusOK {
			t.Fatalf("answered %d: %v", status, got)
		}
		// Four accounts exist: the instance admin plus the three above.
		if total, _ := got["totalResults"].(float64); total != 4 {
			t.Errorf("totalResults = %v, want 4", got["totalResults"])
		}
		if index, _ := got["startIndex"].(float64); index != 2 {
			t.Errorf("startIndex = %v, want 2", got["startIndex"])
		}
		if resources, _ := got["Resources"].([]any); len(resources) != 2 {
			t.Errorf("returned %d resources, want 2", len(resources))
		}
	})

	t.Run("count zero asks only for the total", func(t *testing.T) {
		status, got := f.do(http.MethodGet, "/scim/v2/Users?count=0", "")
		if status != http.StatusOK {
			t.Fatalf("answered %d: %v", status, got)
		}
		if total, _ := got["totalResults"].(float64); total != 4 {
			t.Errorf("totalResults = %v, want 4", got["totalResults"])
		}
		if resources, _ := got["Resources"].([]any); len(resources) != 0 {
			t.Errorf("returned %d resources, want none", len(resources))
		}
	})
}

func TestGetAndUnknownIdsAnswerTheSame404(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	id := f.createUser("amir@example.com", "ext-1")

	status, got := f.do(http.MethodGet, "/scim/v2/Users/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get answered %d: %v", status, got)
	}
	if got["userName"] != "amir@example.com" {
		t.Errorf("userName = %v", got["userName"])
	}

	for _, path := range []string{
		"/scim/v2/Users/" + uuid.NewString(),
		"/scim/v2/Users/not-a-uuid",
	} {
		status, got := f.do(http.MethodGet, path, "")
		if status != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404: %v", path, status, got)
		}
	}
}

// TestScimCannotMakeAnAdministrator is the property that keeps a stolen sync
// token from being worth the whole instance: no shape of request — create,
// replace or patch — may set is_admin.
func TestScimCannotMakeAnAdministrator(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	t.Run("create ignores it", func(t *testing.T) {
		body := `{"userName":"climber@example.com","is_admin":true,"isAdmin":true,` +
			`"admin":true,"roles":[{"value":"admin","primary":true}],` +
			`"groups":[{"value":"administrators"}]}`
		status, got := f.do(http.MethodPost, "/scim/v2/Users", body)
		if status != http.StatusCreated {
			t.Fatalf("create answered %d: %v", status, got)
		}
		id, _ := got["id"].(string)
		if f.account(id).IsAdmin {
			t.Fatal("a SCIM create minted an administrator")
		}
		if _, emitted := got["is_admin"]; emitted {
			t.Error("the resource emitted is_admin")
		}
	})

	t.Run("replace ignores it", func(t *testing.T) {
		id := f.createUser("replacer@example.com", "ext-replace")
		body := `{"userName":"replacer@example.com","is_admin":true,"roles":[{"value":"admin"}]}`
		status, got := f.do(http.MethodPut, "/scim/v2/Users/"+id, body)
		if status != http.StatusOK {
			t.Fatalf("replace answered %d: %v", status, got)
		}
		if f.account(id).IsAdmin {
			t.Fatal("a SCIM replace promoted an account")
		}
	})

	t.Run("patch refuses it", func(t *testing.T) {
		id := f.createUser("patcher@example.com", "ext-patch")
		for _, path := range []string{"is_admin", "isAdmin", "roles", "groups"} {
			body := `{"Operations":[{"op":"replace","path":"` + path + `","value":true}]}`
			status, got := f.do(http.MethodPatch, "/scim/v2/Users/"+id, body)
			if status != http.StatusBadRequest {
				t.Errorf("patch %s answered %d, want 400: %v", path, status, got)
			}
			if got["scimType"] != "invalidPath" {
				t.Errorf("patch %s scimType = %v, want invalidPath", path, got["scimType"])
			}
		}
		if f.account(id).IsAdmin {
			t.Fatal("a SCIM patch promoted an account")
		}
	})

	t.Run("an existing administrator is not demoted either", func(t *testing.T) {
		// The role is not SCIM's to write in either direction: a directory
		// that does not know about it must not silently strip it.
		body := `{"userName":"instanceadmin","displayName":"Renamed"}`
		status, got := f.do(http.MethodPut, "/scim/v2/Users/"+f.admin.ID.String(), body)
		if status != http.StatusOK {
			t.Fatalf("replace answered %d: %v", status, got)
		}
		if !f.account(f.admin.ID.String()).IsAdmin {
			t.Fatal("a SCIM replace demoted the instance administrator")
		}
	})
}

// TestPushedPasswordsAreIgnored pins §2: changePassword is false, and a
// password attribute in a body is dropped rather than stored.
func TestPushedPasswordsAreIgnored(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	body := `{"userName":"pushed@example.com","password":"a directory-chosen password"}`
	status, got := f.do(http.MethodPost, "/scim/v2/Users", body)
	if status != http.StatusCreated {
		t.Fatalf("create answered %d: %v", status, got)
	}
	id, _ := got["id"].(string)
	if hash := f.account(id).PasswordHash; hash != "" {
		t.Errorf("a pushed password produced a credential (%d bytes of hash)", len(hash))
	}
}

func TestReplaceIsAFullReplacement(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	id := f.createUser("amir@example.com", "ext-1")

	// Nothing but userName: everything else is cleared, which is what
	// replace means.
	status, got := f.do(http.MethodPut, "/scim/v2/Users/"+id, `{"userName":"amir@example.com"}`)
	if status != http.StatusOK {
		t.Fatalf("replace answered %d: %v", status, got)
	}
	user := f.account(id)
	if user.Email != nil {
		t.Errorf("email survived a replace that omitted it: %v", *user.Email)
	}
	if user.ScimExternalID != nil {
		t.Errorf("externalId survived a replace that omitted it: %v", *user.ScimExternalID)
	}
	if user.DisplayName != "" {
		t.Errorf("displayName survived a replace that omitted it: %q", user.DisplayName)
	}
	// active is the one exception: an omitted flag must never offboard
	// somebody, so it is left alone rather than defaulting to false.
	if !user.IsActive {
		t.Error("a replace that omitted active deactivated the account")
	}
}

func TestDeactivationAndItsIdempotence(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	id := f.createUser("leaver@example.com", "ext-leaver")

	status, _ := f.do(http.MethodDelete, "/scim/v2/Users/"+id, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete answered %d, want 204", status)
	}
	if f.account(id).IsActive {
		t.Fatal("delete did not deactivate the account")
	}

	// Repeating it is 204: the resource still exists, deactivated, so the
	// operation the provider wanted is the state that holds (§5).
	status, _ = f.do(http.MethodDelete, "/scim/v2/Users/"+id, "")
	if status != http.StatusNoContent {
		t.Errorf("repeated delete answered %d, want 204", status)
	}

	// Reactivation through the Entra shape, quoted boolean included.
	body := `{"Operations":[{"op":"Replace","path":"active","value":"True"}]}`
	status, got := f.do(http.MethodPatch, "/scim/v2/Users/"+id, body)
	if status != http.StatusOK {
		t.Fatalf("reactivating patch answered %d: %v", status, got)
	}
	if !f.account(id).IsActive {
		t.Error("the account was not reactivated")
	}

	actions := f.log.actions()
	for _, want := range []string{"scim.user.deactivated", "scim.user.reactivated"} {
		if !containsAction(actions, want) {
			t.Errorf("audit actions = %v, want %s", actions, want)
		}
	}
}

// TestDeactivatingTheLastAdministratorIs409 pins §5's refusal. An instance
// nobody can administer is unrecoverable, and a provider will retry — so
// this has to be a conflict it can read, never a 500.
func TestDeactivatingTheLastAdministratorIs409(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	status, got := f.do(http.MethodDelete, "/scim/v2/Users/"+f.admin.ID.String(), "")
	if status != http.StatusConflict {
		t.Fatalf("deleting the last administrator answered %d, want 409: %v", status, got)
	}
	if !f.account(f.admin.ID.String()).IsActive {
		t.Fatal("the last administrator was deactivated anyway")
	}

	body := `{"Operations":[{"op":"replace","path":"active","value":false}]}`
	status, got = f.do(http.MethodPatch, "/scim/v2/Users/"+f.admin.ID.String(), body)
	if status != http.StatusConflict {
		t.Errorf("patching the last administrator inactive answered %d, want 409: %v", status, got)
	}
}

// TestDiscoveryDocumentsRefuseGroups pins the honest refusal: Groups are
// absent from ResourceTypes, and a provider that asks anyway gets a SCIM
// 404 rather than the application's.
func TestDiscoveryDocumentsRefuseGroups(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	status, got := f.do(http.MethodGet, "/scim/v2/ServiceProviderConfig", "")
	if status != http.StatusOK {
		t.Fatalf("ServiceProviderConfig answered %d", status)
	}
	patch, _ := got["patch"].(map[string]any)
	if patch["supported"] != true {
		t.Error("patch is not declared supported")
	}
	changePassword, _ := got["changePassword"].(map[string]any)
	if changePassword["supported"] != false {
		t.Error("changePassword is not declared unsupported")
	}
	filter, _ := got["filter"].(map[string]any)
	if maxResults, _ := filter["maxResults"].(float64); maxResults != 200 {
		t.Errorf("filter.maxResults = %v, want 200", filter["maxResults"])
	}

	status, got = f.do(http.MethodGet, "/scim/v2/ResourceTypes", "")
	if status != http.StatusOK {
		t.Fatalf("ResourceTypes answered %d", status)
	}
	resources, _ := got["Resources"].([]any)
	for _, res := range resources {
		entry, _ := res.(map[string]any)
		if entry["id"] == "Group" {
			t.Fatal("ResourceTypes lists Group, which this server does not have")
		}
	}

	status, _ = f.do(http.MethodGet, "/scim/v2/Schemas", "")
	if status != http.StatusOK {
		t.Errorf("Schemas answered %d", status)
	}

	// The refusal a provider actually meets.
	status, got = f.do(http.MethodGet, "/scim/v2/Groups", "")
	if status != http.StatusNotFound {
		t.Errorf("Groups answered %d, want 404: %v", status, got)
	}
	if schemas, _ := got["schemas"].([]any); len(schemas) == 0 ||
		schemas[0] != "urn:ietf:params:scim:api:messages:2.0:Error" {
		t.Errorf("the Groups refusal is not a SCIM error envelope: %v", got)
	}
}

// TestEveryUnusableCredentialAnswersOne401 pins §3 from the inside: missing,
// malformed, unknown and revoked tokens are one answer, and none of them
// says which it was.
func TestEveryUnusableCredentialAnswersOne401(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	revokedRaw, revokedHash := session.NewToken()
	ctx := context.Background()
	revoked, err := f.store.CreateScimToken(ctx, f.admin.ID, revokedHash, "to be revoked")
	if err != nil {
		t.Fatalf("mint token to revoke: %v", err)
	}
	if err := f.store.RevokeScimToken(ctx, revoked.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	credentials := map[string]string{
		"no header at all":       "",
		"an empty bearer":        "Bearer ",
		"a guessed token":        "Bearer " + strings.Repeat("z", 43),
		"a revoked token":        "Bearer " + revokedRaw,
		"the wrong scheme":       "Basic " + f.token,
		"the token with no verb": f.token,
	}
	for name, authorization := range credentials {
		status, got := f.doAs(http.MethodGet, "/scim/v2/Users", "", authorization)
		if status != http.StatusUnauthorized {
			t.Errorf("%s answered %d, want 401: %v", name, status, got)
		}
		if got["status"] != "401" || got["detail"] != "a provisioning token is required" {
			t.Errorf("%s answered a different envelope: %v", name, got)
		}
	}
}

// TestUsingATokenRecordsIt pins the one thing that tells a configured
// credential apart from one that was minted and forgotten.
func TestUsingATokenRecordsIt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if status, _ := f.do(http.MethodGet, "/scim/v2/Users", ""); status != http.StatusOK {
		t.Fatalf("list answered %d", status)
	}

	tokens, err := f.store.ListScimTokens(context.Background())
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	if tokens[0].LastUsedAt == nil {
		t.Error("last_used_at is still null after the token authenticated a request")
	}
}

// TestOversizedAttributesAre400 keeps a column constraint from surfacing as
// a 500 a provider cannot act on.
func TestOversizedAttributesAre400(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	bodies := map[string]string{
		"userName":    `{"userName":"` + strings.Repeat("a", 400) + `@example.com"}`,
		"externalId":  `{"userName":"ok@example.com","externalId":"` + strings.Repeat("x", 300) + `"}`,
		"displayName": `{"userName":"ok@example.com","displayName":"` + strings.Repeat("n", 200) + `"}`,
		"missing":     `{"displayName":"nobody"}`,
	}
	for name, body := range bodies {
		status, got := f.do(http.MethodPost, "/scim/v2/Users", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400: %v", name, status, got)
		}
		if got["scimType"] != "invalidValue" {
			t.Errorf("%s scimType = %v, want invalidValue", name, got["scimType"])
		}
	}
}

func containsAction(actions []string, want string) bool {
	for _, action := range actions {
		if action == want {
			return true
		}
	}
	return false
}

// TestExhaustedDerivationsAreAConflictNotAnInternalError pins the second
// half of the non-Latin fallback fix. Every derivation of one userName can
// genuinely be taken — twenty different directory addresses whose local
// parts are all "amir" will do it — and when that happens the provider must
// get a conflict it can read rather than an opaque 500.
func TestExhaustedDerivationsAreAConflictNotAnInternalError(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Each of these derives the base "amir", so they walk the whole suffix
	// range: amir, amir-1 ... amir-19.
	for i := range 20 {
		f.createUser(fmt.Sprintf("amir@host%d.example", i), fmt.Sprintf("ext-%d", i))
	}

	status, got := f.do(http.MethodPost, "/scim/v2/Users",
		`{"userName":"amir@host99.example","externalId":"ext-99"}`)
	if status != http.StatusConflict {
		t.Fatalf("the twenty-first collision answered %d, want 409: %v", status, got)
	}
	if got["scimType"] != "uniqueness" {
		t.Errorf("scimType = %v, want uniqueness", got["scimType"])
	}
	if detail, _ := got["detail"].(string); !strings.Contains(detail, "local username") {
		t.Errorf("detail = %q, want something an operator can act on", detail)
	}
}

// TestNonLatinUserNamesGetDistinctAccounts is the reported bug from the
// outside, and it has to be this size to catch it.
//
// A shared fallback literal still provisions the first twenty people: they
// all derive one base, and the caller's suffix loop rescues them one after
// another. It is the twenty-FIRST that cannot be provisioned at all, because
// the loop has run out of suffixes — so a three-account test passes against
// the broken code and proves nothing. A Persian organisation onboarding its
// twenty-first person is exactly who found this.
func TestNonLatinUserNamesGetDistinctAccounts(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Distinct Persian given names, none of which leaves any usable ASCII.
	names := []string{
		"امیر", "سارا", "زهرا", "رضا", "مریم", "علی", "نگار", "حسین",
		"لیلا", "کاوه", "بهار", "آرش", "شیرین", "پویا", "نرگس", "بابک",
		"فرزانه", "کیان", "مینا", "سپهر", "یاسمن",
	}

	usernames := map[string]string{}
	for i, name := range names {
		userName := name + "@example.com"
		id := f.createUser(userName, fmt.Sprintf("ext-fa-%d", i))
		username := f.account(id).Username
		if first, clash := usernames[username]; clash {
			t.Fatalf("%q and %q both derived %q", first, userName, username)
		}
		usernames[username] = userName
	}
	if len(usernames) != len(names) {
		t.Errorf("%d directory names produced %d local usernames", len(names), len(usernames))
	}
}

// TestAnImplausibleTokenCostsNoQuery pins the shape check in front of the
// lookup. The budget is spent AFTER authentication, so anything that reaches
// storage reaches it unmetered — which is the whole reason a guess that
// cannot be a token must be thrown away before the query.
func TestAnImplausibleTokenCostsNoQuery(t *testing.T) {
	t.Parallel()

	var queries int
	service := scim.New(countingStore{onLookup: func() { queries++ }})
	srv := httptest.NewServer(service)
	t.Cleanup(srv.Close)

	implausible := []string{
		"", "x", strings.Repeat("z", 42), strings.Repeat("z", 44),
		strings.Repeat("z", 42) + "=", strings.Repeat("z", 42) + "+",
		"'; DROP TABLE scim_tokens; --",
	}
	for _, raw := range implausible {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, srv.URL+"/scim/v2/Users", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("send request: %v", err)
		}
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close response: %v", closeErr)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%q answered %d, want 401", raw, resp.StatusCode)
		}
	}
	if queries != 0 {
		t.Errorf("%d implausible tokens reached storage; want none", queries)
	}

	// A well-shaped token still does reach it, so the check is a filter and
	// not an outage.
	wellShaped, _ := session.NewToken()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, srv.URL+"/scim/v2/Users", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+wellShaped)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Errorf("close response: %v", closeErr)
	}
	if queries != 1 {
		t.Errorf("a well-shaped token produced %d queries, want 1", queries)
	}
}

// countingStore answers nothing and counts the token lookups it was asked
// for. Only the door is exercised here.
type countingStore struct{ onLookup func() }

func (c countingStore) ScimTokenByHash(context.Context, []byte) (uuid.UUID, error) {
	c.onLookup()
	return uuid.Nil, storage.ErrNotFound
}

func (countingStore) CreateScimUser(context.Context, storage.NewScimUser) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (countingStore) ReplaceScimUser(context.Context, uuid.UUID, storage.ScimUserAttributes) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (countingStore) ListScimUsers(context.Context, storage.ScimUserFilter, int, int) ([]storage.User, int, error) {
	return nil, 0, nil
}

func (countingStore) UserByID(context.Context, uuid.UUID) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (countingStore) UpdateUserAdmin(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (countingStore) OrgSettings(context.Context) (storage.OrgSettings, error) {
	return storage.OrgSettings{DefaultLocale: "en"}, nil
}
