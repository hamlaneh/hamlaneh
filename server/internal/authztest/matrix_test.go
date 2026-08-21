package authztest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// fixturePassword is every matrix user's password; hashing is slow, so the
// hash is computed once and shared.
const fixturePassword = "matrix fixture password"

var fixtureHash = sync.OnceValue(func() string {
	return password.Hash(fixturePassword)
})

// cellSeq makes every cell's fixture unique.
var cellSeq atomic.Int64

// cell is one provisioned matrix cell: a fresh user with (for
// authenticated principals) a fresh live session.
type cell struct {
	fx           Fixture
	accessToken  string
	refreshToken string
}

// provisionCell creates the acting user and session for one cell directly
// through storage — no login round-trips, so the login rate limiter only
// sees the login row's own cells.
func provisionCell(ctx context.Context, t *testing.T, store *storage.Store, principal Principal) cell {
	t.Helper()

	seq := cellSeq.Add(1)
	fx := Fixture{
		Username: fmt.Sprintf("mx%d", seq),
		Password: fixturePassword,
		Unique:   fmt.Sprintf("%d", seq),
	}

	user, err := store.CreateUser(ctx, storage.NewUser{
		Username:           fx.Username,
		PasswordHash:       fixtureHash(),
		Locale:             "en",
		IsAdmin:            principal == Admin,
		MustChangePassword: principal == MemberMustChange,
	})
	if err != nil {
		t.Fatalf("create %s fixture user: %v", principal, err)
	}

	c := cell{fx: fx}
	if principal == Anonymous {
		return c
	}

	accessRaw, accessHash := session.NewToken()
	refreshRaw, refreshHash := session.NewToken()
	if _, err := store.CreateSession(ctx, storage.NewSession{
		UserID: user.ID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash:  accessHash,
			RefreshTokenHash: refreshHash,
			AccessTTL:        session.AccessTTL,
			RefreshTTL:       session.RefreshTTL,
		},
	}); err != nil {
		t.Fatalf("create %s fixture session: %v", principal, err)
	}
	c.accessToken = accessRaw
	c.refreshToken = refreshRaw
	return c
}

// buildRequest assembles the cell's request: body from the entry's builder,
// cookies for authenticated principals, and the CSRF pair on mutations.
func buildRequest(entry Entry, principal Principal, c cell) *http.Request {
	body := ""
	if entry.Body != nil {
		body = entry.Body(c.fx)
	}
	req := httptest.NewRequest(entry.Method, entry.Target(), strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if principal == Anonymous {
		return req
	}

	req.AddCookie(&http.Cookie{Name: session.AccessCookie, Value: c.accessToken})
	req.AddCookie(&http.Cookie{Name: session.RefreshCookie, Value: c.refreshToken})
	if entry.Method != http.MethodGet && entry.Method != http.MethodHead {
		// Double-submit: the server stores no CSRF state, so the harness
		// picks the value.
		req.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: "matrix-csrf-value"})
		req.Header.Set(session.CSRFHeader, "matrix-csrf-value")
	}
	return req
}

// TestAuthzMatrix runs the full principal-times-endpoint grid against a
// real server on a real database and asserts every cell. Every cell gets
// its own user and session, so mutating cells (logout, change-password)
// cannot poison others.
func TestAuthzMatrix(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	handler := httpserver.Handler(store)
	ctx := context.Background()

	entries := Registry()
	cells := 0
	for _, entry := range entries {
		for _, principal := range Principals() {
			want, ok := entry.Want[principal]
			if !ok {
				t.Fatalf("%s %s has no expectation for %s", entry.Method, entry.Path, principal)
			}
			cells++

			t.Run(fmt.Sprintf("%s %s as %s", entry.Method, entry.Path, principal), func(t *testing.T) {
				c := provisionCell(ctx, t, store, principal)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, buildRequest(entry, principal, c))

				if rec.Code != want {
					t.Errorf("got status %d, want %d (body %s)", rec.Code, want, rec.Body.String())
				}
				if wantCode, ok := entry.WantCode[principal]; ok {
					var body struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatalf("body %q is not the contract Error shape: %v", rec.Body.String(), err)
					}
					if body.Error.Code != wantCode {
						t.Errorf("got error code %q, want %q", body.Error.Code, wantCode)
					}
				}
			})
		}
	}

	if wantCells := len(entries) * len(Principals()); cells != wantCells {
		t.Errorf("matrix ran %d cells, want %d", cells, wantCells)
	}
}
