package wsgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/scim"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The offboarding test, and the one that matters most about SCIM: when a
// directory says active: false, the access is gone — not marked as ended,
// gone.
//
// It runs against a real database and real sockets, deliberately end to end:
// the SCIM handler calls the same UpdateUserAdmin the admin dashboard calls,
// which revokes every session family inside its transaction, and the
// gateway's existing sweep then closes every socket of those families
// because it keys on revoked families rather than on who revoked them
// (docs/api/scim.md §5). Nothing in the middle is stubbed, because the whole
// claim is that no new kill path exists — that the old one already does
// this.
//
// It lives in this package to reuse the harness that proves revocation
// closes a socket within its budget, so it is the same endpoint, the same
// gates and the same sweep every other test here goes through.

// deprovisionSweep is fast because the budget is not what this test is
// about: TestRevokedSessionClosesSocketWithinBudget already pins the ten
// seconds on the production interval. What this one proves is that a SCIM
// deactivation lands in the state that sweep reads.
const deprovisionSweep = 20 * time.Millisecond

func TestScimDeactivationClosesEverySocketOfTheAccount(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	// An administrator to hold the last-admin rule open, and the person a
	// directory is about to offboard, signed in on two devices.
	newTestUser(ctx, t, store, "scimadmin", true)
	target := newTestUser(ctx, t, store, "leaver", false)
	laptop := newTestFamily(ctx, t, store, target.ID)
	phone := newTestFamily(ctx, t, store, target.ID)

	h := newHarnessOn(t, store, func(id uuid.UUID) storage.User {
		user, err := store.UserByID(ctx, id)
		if err != nil {
			t.Errorf("resolve socket principal: %v", err)
		}
		return user
	}, WithSweepInterval(deprovisionSweep))

	first := h.dial(target, laptop)
	first.hello()
	second := h.dial(target, phone)
	second.hello()

	provisioning, token := newProvisioningServer(t, store)
	body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
		`"Operations":[{"op":"replace","path":"active","value":false}]}`
	resp := provisioning.do(t, http.MethodPatch, "/scim/v2/Users/"+target.ID.String(), token, body)
	if resp != http.StatusOK {
		t.Fatalf("SCIM deactivation answered %d, want 200", resp)
	}

	// Every socket of the account, not merely the family that happened to be
	// named: deactivation revokes them all in one transaction.
	for name, c := range map[string]*wsClient{"laptop": first, "phone": second} {
		if code := c.waitClose(); code != closeUnauthorized {
			t.Errorf("%s socket close code = %d, want %d", name, code, closeUnauthorized)
		}
	}

	families, err := store.ListSessionFamilies(ctx, target.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("list session families: %v", err)
	}
	if len(families) != 0 {
		t.Errorf("account still holds %d live session families after deactivation", len(families))
	}

	user, err := store.UserByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("read account back: %v", err)
	}
	if user.IsActive {
		t.Error("account is still active after a SCIM deactivation")
	}
}

// provisioningServer is the SCIM surface on a real store, with one live
// token to present to it.
type provisioningServer struct {
	server *httptest.Server
}

func newProvisioningServer(t *testing.T, store *storage.Store) (*provisioningServer, string) {
	t.Helper()

	ctx := context.Background()
	admin, err := store.UserByIdentifier(ctx, "scimadmin")
	if err != nil {
		t.Fatalf("find token minter: %v", err)
	}
	raw, hash := session.NewToken()
	if _, err := store.CreateScimToken(ctx, admin.ID, hash, "deprovision test"); err != nil {
		t.Fatalf("mint provisioning token: %v", err)
	}

	server := httptest.NewServer(scim.New(store))
	t.Cleanup(server.Close)
	return &provisioningServer{server: server}, raw
}

// do sends one authenticated SCIM request and returns its status.
func (p *provisioningServer) do(t *testing.T, method, path, token, body string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, p.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build scim request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", scim.ContentType)

	resp, err := p.server.Client().Do(req)
	if err != nil {
		t.Fatalf("send scim request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close scim response: %v", closeErr)
		}
	}()
	return resp.StatusCode
}

// newTestUser creates an account with a real password hash, which is what
// makes it an ordinary account rather than a directory-provisioned one.
func newTestUser(ctx context.Context, t *testing.T, store *storage.Store, username string, admin bool) storage.User {
	t.Helper()

	user, err := store.CreateUser(ctx, storage.NewUser{
		Username:     username,
		PasswordHash: password.Hash("a deprovision test passphrase"),
		Locale:       "en",
		IsAdmin:      admin,
	})
	if err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return user
}

// newTestFamily mints one live session and returns its family id — one
// signed-in device.
func newTestFamily(ctx context.Context, t *testing.T, store *storage.Store, userID uuid.UUID) uuid.UUID {
	t.Helper()

	_, accessHash := session.NewToken()
	_, refreshHash := session.NewToken()
	sess, err := store.CreateSession(ctx, storage.NewSession{
		UserID: userID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash:  accessHash,
			RefreshTokenHash: refreshHash,
			AccessTTL:        session.AccessTTL,
			RefreshTTL:       session.RefreshTTL,
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess.FamilyID
}
