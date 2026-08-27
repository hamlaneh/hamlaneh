package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/scim"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The dashboard half of provisioning. Who may call these is the authz
// matrix's business (internal/authztest); what they do with a credential is
// this file's.

var scimTokenID = uuid.MustParse("22222222-2222-2222-2222-222222222222")

func storedScimToken(note string) storage.ScimToken {
	return storage.ScimToken{
		ID:        scimTokenID,
		Note:      note,
		CreatedBy: storage.ScimTokenCreator{ID: adminID, Username: "admin", DisplayName: "Admin"},
		CreatedAt: time.Now().Add(-time.Hour),
	}
}

// TestCreateScimTokenShowsTheTokenOnce is the whole security shape of
// minting: the value is in the response and nowhere else, and only its
// digest reaches storage.
func TestCreateScimTokenShowsTheTokenOnce(t *testing.T) {
	t.Parallel()

	var storedHash []byte
	store := adminStore()
	store.createScimToken = func(_ context.Context, createdBy uuid.UUID, tokenHash []byte, note string) (storage.ScimToken, error) {
		storedHash = tokenHash
		if createdBy != adminID {
			t.Errorf("created_by = %s, want the acting admin", createdBy)
		}
		if note != "okta production" {
			t.Errorf("note = %q", note)
		}
		return storedScimToken(note), nil
	}
	log := &recordingAudit{}

	rec := newRecorder()
	httpserver.Handler(store, httpserver.WithAudit(log)).ServeHTTP(rec,
		adminAPI(http.MethodPost, "/api/v1/admin/scim/tokens", `{"note":"okta production"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var body api.CreatedScimToken
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the contract shape: %v", err)
	}
	if body.Token == "" {
		t.Fatal("the response carried no token")
	}
	if body.Scim.Id != scimTokenID {
		t.Errorf("token id = %s, want %s", body.Scim.Id, scimTokenID)
	}

	// Only the digest is stored: a stolen database yields nothing that can
	// be presented at /scim/v2.
	if string(storedHash) == body.Token {
		t.Fatal("the raw token was handed to storage")
	}
	if want := session.HashToken(body.Token); string(storedHash) != string(want) {
		t.Error("what was stored is not the digest of what was shown")
	}
	if !containsString(log.actions(), "scim.token.created") {
		t.Errorf("audit actions = %v, want scim.token.created", log.actions())
	}
}

// TestCreatedScimTokenAuthenticatesTheProvisioningSurface closes the loop
// the two halves of this slice exist for: the value the dashboard shows once
// is the value a sync engine presents, and nothing else about the two
// surfaces has to agree for that to work.
func TestCreatedScimTokenAuthenticatesTheProvisioningSurface(t *testing.T) {
	t.Parallel()

	var storedHash []byte
	store := adminStore()
	store.createScimToken = func(_ context.Context, _ uuid.UUID, tokenHash []byte, note string) (storage.ScimToken, error) {
		storedHash = tokenHash
		return storedScimToken(note), nil
	}
	// The provisioning surface resolves whatever digest was stored, and
	// nothing else.
	provisioning := scim.New(fakeScimStore{hash: func() []byte { return storedHash }})
	handler := httpserver.Handler(store, httpserver.WithSCIM(provisioning))

	rec := newRecorder()
	handler.ServeHTTP(rec, adminAPI(http.MethodPost, "/api/v1/admin/scim/tokens", `{}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint answered %d (body %s)", rec.Code, rec.Body.String())
	}
	var minted api.CreatedScimToken
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatalf("body is not the contract shape: %v", err)
	}

	presented := request(http.MethodGet, "/scim/v2/ServiceProviderConfig", "")
	presented.Header.Set("Authorization", "Bearer "+minted.Token)
	rec = newRecorder()
	handler.ServeHTTP(rec, presented)
	if rec.Code != http.StatusOK {
		t.Fatalf("the minted token was refused at /scim/v2: %d (body %s)", rec.Code, rec.Body.String())
	}

	// And the same request without it is refused, so the 200 above was the
	// credential rather than an open door.
	rec = newRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "/scim/v2/ServiceProviderConfig", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the provisioning surface answered %d with no credential, want 401", rec.Code)
	}
}

func TestCreateScimTokenRejectsAnOversizedNote(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.createScimToken = func(context.Context, uuid.UUID, []byte, string) (storage.ScimToken, error) {
		t.Error("the store was asked to mint a token for an invalid request")
		return storage.ScimToken{}, nil
	}

	body := `{"note":"` + strings.Repeat("n", 201) + `"}`
	rec := do(t, store, adminAPI(http.MethodPost, "/api/v1/admin/scim/tokens", body))
	wantError(t, rec, http.StatusBadRequest, "invalid_request")
}

// TestListScimTokensNeverCarriesATokenValue mirrors the invite list: the
// table says which credentials are live, never what they are.
func TestListScimTokensNeverCarriesATokenValue(t *testing.T) {
	t.Parallel()

	used := time.Now().Add(-time.Minute)
	store := adminStore()
	store.listScimTokens = func(context.Context) ([]storage.ScimToken, error) {
		token := storedScimToken("okta production")
		token.LastUsedAt = &used
		return []storage.ScimToken{token}, nil
	}

	rec := do(t, store, adminAPI(http.MethodGet, "/api/v1/admin/scim/tokens", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d (body %s)", rec.Code, rec.Body.String())
	}
	var page api.ScimTokenPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not the contract shape: %v", err)
	}
	if len(page.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(page.Tokens))
	}
	if page.Tokens[0].LastUsedAt == nil {
		t.Error("last_used_at was dropped; it is how a configured token is told from a forgotten one")
	}
	// Nothing in the serialized row may be presentable as a credential.
	for _, field := range []string{"token", "token_hash"} {
		if strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("the token list carried a %q field", field)
		}
	}
}

// TestRevokeScimTokenIsNotIdempotent pins the deliberate difference from
// invitation revocation: an id naming no live credential is a 404, because
// an administrator cutting one off needs to know they named the wrong one.
func TestRevokeScimTokenIsNotIdempotent(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.revokeScimToken = func(context.Context, uuid.UUID) error { return storage.ErrNotFound }

	rec := do(t, store, adminAPI(http.MethodDelete, "/api/v1/admin/scim/tokens/"+scimTokenID.String(), ""))
	wantError(t, rec, http.StatusNotFound, "scim_token_not_found")
}

func TestRevokeScimTokenSucceedsAndIsAudited(t *testing.T) {
	t.Parallel()

	var revoked uuid.UUID
	store := adminStore()
	store.revokeScimToken = func(_ context.Context, id uuid.UUID) error {
		revoked = id
		return nil
	}
	log := &recordingAudit{}

	rec := newRecorder()
	httpserver.Handler(store, httpserver.WithAudit(log)).ServeHTTP(rec,
		adminAPI(http.MethodDelete, "/api/v1/admin/scim/tokens/"+scimTokenID.String(), ""))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if revoked != scimTokenID {
		t.Errorf("revoked %s, want %s", revoked, scimTokenID)
	}
	if !containsString(log.actions(), "scim.token.revoked") {
		t.Errorf("audit actions = %v, want scim.token.revoked", log.actions())
	}
}

// TestSCIMPrefixMatchesPackage pins the one string this package spells out
// for itself. The mount point and internal/scim's own BasePath have to be
// the same path, and nothing else would notice if they drifted.
func TestSCIMPrefixMatchesPackage(t *testing.T) {
	t.Parallel()

	store := adminStore()
	handler := httpserver.Handler(store, httpserver.WithSCIM(
		scim.New(fakeScimStore{hash: func() []byte { return nil }})))

	// A path the SCIM mux owns must reach it — proved by the SCIM error
	// envelope coming back rather than the application's 404.
	rec := newRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, scim.BasePath+"/Users", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%s/Users answered %d, want the provisioning surface's 401", scim.BasePath, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "urn:ietf:params:scim:api:messages:2.0:Error") {
		t.Errorf("%s/Users did not reach the provisioning surface: %s", scim.BasePath, rec.Body.String())
	}
}

// fakeScimStore is the narrowest possible scim.Store: it answers the token
// lookup and nothing else. These tests only ever reach the door.
type fakeScimStore struct {
	hash func() []byte
}

func (f fakeScimStore) ScimTokenByHash(_ context.Context, tokenHash []byte) (uuid.UUID, error) {
	want := f.hash()
	if want == nil || string(tokenHash) != string(want) {
		return uuid.Nil, storage.ErrNotFound
	}
	return scimTokenID, nil
}

func (fakeScimStore) CreateScimUser(context.Context, storage.NewScimUser) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (fakeScimStore) ReplaceScimUser(context.Context, uuid.UUID, storage.ScimUserAttributes) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (fakeScimStore) ListScimUsers(context.Context, storage.ScimUserFilter, int, int) ([]storage.User, int, error) {
	return nil, 0, nil
}

func (fakeScimStore) UserByID(context.Context, uuid.UUID) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (fakeScimStore) UpdateUserAdmin(context.Context, uuid.UUID, storage.AdminUserUpdate) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

func (fakeScimStore) OrgSettings(context.Context) (storage.OrgSettings, error) {
	return storage.OrgSettings{DefaultLocale: "en"}, nil
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
