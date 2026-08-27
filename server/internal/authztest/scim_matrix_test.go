package authztest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/scim"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The SCIM authorization matrix: every operation the registry declares,
// asked with every credential the instance has, on a real server and a real
// database.
//
// It runs against the SAME handler the contract routes run against, because
// the property under test is about the two of them together: "two doors, two
// credentials, no ambient authority crossing between them" (docs/api/scim.md
// §3) is not a claim either surface can make on its own.

// scimWorld is the server plus the credentials the matrix presents to it.
type scimWorld struct {
	handler      http.Handler
	token        string // a live provisioning token
	revoked      string // one that was revoked
	memberCookie string // an ordinary session
	adminCookie  string // an administrator's session
	userID       uuid.UUID
}

func newSCIMWorld(t *testing.T) *scimWorld {
	t.Helper()

	store, _ := testdb.New(t)
	ctx := context.Background()

	admin := newFixtureUser(ctx, t, store, "scimadmin", true, false)
	member := newFixtureUser(ctx, t, store, "scimmember", false, false)
	adminAccess, _ := newFixtureSession(ctx, t, store, admin.ID)
	memberAccess, _ := newFixtureSession(ctx, t, store, member.ID)

	live, liveHash := session.NewToken()
	if _, err := store.CreateScimToken(ctx, admin.ID, liveHash, "matrix"); err != nil {
		t.Fatalf("mint provisioning token: %v", err)
	}
	dead, deadHash := session.NewToken()
	revoked, err := store.CreateScimToken(ctx, admin.ID, deadHash, "matrix revoked")
	if err != nil {
		t.Fatalf("mint token to revoke: %v", err)
	}
	if err := store.RevokeScimToken(ctx, revoked.ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	return &scimWorld{
		handler:      httpserver.Handler(store, httpserver.WithSCIM(scim.New(store))),
		token:        live,
		revoked:      dead,
		memberCookie: memberAccess,
		adminCookie:  adminAccess,
		userID:       member.ID,
	}
}

// request builds one SCIM request against a concrete target, with whatever
// credential the cell is presenting.
func (w *scimWorld) request(entry SCIMEntry, prepare func(*http.Request)) *http.Request {
	target := strings.ReplaceAll(entry.Path, "{id}", w.userID.String())

	body := ""
	if entry.Method == http.MethodPost || entry.Method == http.MethodPut {
		body = `{"userName":"matrix@example.com"}`
	}
	if entry.Method == http.MethodPatch {
		body = `{"Operations":[{"op":"replace","path":"displayName","value":"Matrix"}]}`
	}

	req := httptest.NewRequest(entry.Method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", scim.ContentType)
	// Each cell speaks from its own address so the surface's single per-IP
	// budget cannot turn the grid red for a reason that is not permission.
	req.RemoteAddr = fmt.Sprintf("[2001:db8:5c11::%x]:443", len(entry.Op)+len(entry.Method))
	prepare(req)
	return req
}

// TestSCIMBearerMatrix asserts the rule every registry entry declares: a live
// provisioning token, and nothing else.
//
// The four refusals are the point. Anonymous is obvious; the two cookie
// columns are not — an administrator's session reaching this surface would
// mean their browser could be made to provision accounts, which is exactly
// the ambient authority the two-door design exists to remove.
func TestSCIMBearerMatrix(t *testing.T) {
	t.Parallel()

	w := newSCIMWorld(t)

	refusals := map[string]func(*http.Request){
		"anonymous":                            func(*http.Request) {},
		"a guessed token":                      func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("z", 43)) },
		"a revoked token":                      func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+w.revoked) },
		"a member's cookie":                    func(r *http.Request) { addSession(r, w.memberCookie) },
		"an admin's cookie":                    func(r *http.Request) { addSession(r, w.adminCookie) },
		"an admin cookie plus an empty bearer": func(r *http.Request) { addSession(r, w.adminCookie); r.Header.Set("Authorization", "Bearer ") },
	}

	for _, entry := range SCIMRegistry() {
		if entry.Authz != SCIMBearer {
			t.Fatalf("%s declares authz %q; this matrix only knows how to assert %q",
				entry.Op, entry.Authz, SCIMBearer)
		}

		for name, prepare := range refusals {
			t.Run(fmt.Sprintf("%s as %s", entry.Op, name), func(t *testing.T) {
				t.Parallel()

				rec := httptest.NewRecorder()
				w.handler.ServeHTTP(rec, w.request(entry, prepare))

				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("got status %d, want 401 (body %s)", rec.Code, rec.Body.String())
				}
				// The refusal must be the SCIM envelope, not the
				// application's: a sync engine parses this one.
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode refusal: %v (body %s)", err, rec.Body.String())
				}
				schemas, _ := body["schemas"].([]any)
				if len(schemas) == 0 || schemas[0] != "urn:ietf:params:scim:api:messages:2.0:Error" {
					t.Errorf("refusal is not a SCIM error envelope: %s", rec.Body.String())
				}
				if _, isAppError := body["error"]; isAppError {
					t.Errorf("refusal used the application's Error shape: %s", rec.Body.String())
				}
			})
		}

		t.Run(fmt.Sprintf("%s with a live token", entry.Op), func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			w.handler.ServeHTTP(rec, w.request(entry, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+w.token)
			}))

			// What the operation answers is every other test's business;
			// this cell asserts only that the credential was accepted, so
			// the four refusals above are about the credential and not about
			// the surface being broken.
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("a live provisioning token was refused (body %s)", rec.Body.String())
			}
		})
	}
}

// TestAProvisioningTokenIsWorthlessUnderAPI is the other direction, and the
// half a SCIM-only test cannot cover: the contract routes must not accept
// the bearer token either. Two doors, two credentials.
func TestAProvisioningTokenIsWorthlessUnderAPI(t *testing.T) {
	t.Parallel()

	w := newSCIMWorld(t)

	// One read, one admin read, and the admin route that mints more of these
	// tokens — the one an attacker holding a token would most want.
	for _, target := range []string{
		"/api/v1/users/me",
		"/api/v1/admin/users",
		"/api/v1/admin/scim/tokens",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Authorization", "Bearer "+w.token)
			req.RemoteAddr = "[2001:db8:5c11::a1]:443"

			rec := httptest.NewRecorder()
			w.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got status %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			// And the refusal is the application's shape here, not SCIM's:
			// the surfaces do not leak their vocabularies into each other.
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode refusal: %v", err)
			}
			envelope, _ := body["error"].(map[string]any)
			if envelope["code"] != "not_authenticated" {
				t.Errorf("refusal = %s, want the contract's not_authenticated", rec.Body.String())
			}
		})
	}
}

// TestSCIMIsAbsentWithoutTheOption pins the zero-config shape: a server built
// without provisioning does not serve these paths at all.
func TestSCIMIsAbsentWithoutTheOption(t *testing.T) {
	t.Parallel()

	w := newSCIMWorld(t)
	store, _ := testdb.New(t)
	bare := httpserver.Handler(store)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer "+w.token)
	rec := httptest.NewRecorder()
	bare.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404 from a server with no provisioning surface", rec.Code)
	}
}

// addSession attaches a session the way a browser would, CSRF double-submit
// included — so a refusal here is about the credential being the wrong KIND
// rather than about a header the client forgot.
func addSession(r *http.Request, accessToken string) {
	r.AddCookie(&http.Cookie{Name: session.AccessCookie, Value: accessToken})
	r.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: "scim-matrix-csrf"})
	r.Header.Set(session.CSRFHeader, "scim-matrix-csrf")
}
