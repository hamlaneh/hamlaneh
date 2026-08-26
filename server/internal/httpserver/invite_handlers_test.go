package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// recordingAudit is the AuditRecorder tests assert against. It is
// concurrency-safe because the recorder's contract permits the real one to be
// called from anywhere.
type recordingAudit struct {
	mu     sync.Mutex
	events []httpserver.AuditEvent
}

func (r *recordingAudit) Record(_ context.Context, ev httpserver.AuditEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingAudit) actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.Action)
	}
	return out
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// liveInvite is the store's answer for a token that can still be redeemed.
func liveInvite() storage.Invite {
	return storage.Invite{
		ID:        uuid.MustParse("55555555-5555-5555-5555-555555555555"),
		CreatedBy: storage.InviteCreator{ID: adminID, Username: "member", DisplayName: "Member"},
		Note:      "for the new designer",
		CreatedAt: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// matrixToken is a well-formed token of the right length that names nothing.
const matrixToken = "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

func TestCreateInviteShowsTheLinkOnce(t *testing.T) {
	t.Parallel()

	var storedHash []byte
	var storedTTL time.Duration
	store := adminStore()
	store.createInvite = func(_ context.Context, createdBy uuid.UUID, tokenHash []byte, note string, ttl time.Duration) (storage.Invite, error) {
		storedHash, storedTTL = tokenHash, ttl
		if createdBy != adminID {
			t.Errorf("created_by = %s, want the acting admin", createdBy)
		}
		if note != "for the new designer" {
			t.Errorf("note = %q", note)
		}
		return liveInvite(), nil
	}

	rec := do(t, store, adminAPI(http.MethodPost, "/api/v1/admin/invites",
		`{"note":"  for the new designer  ","expires_in_hours":48}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if storedTTL != 48*time.Hour {
		t.Errorf("ttl = %s, want 48h", storedTTL)
	}

	var got api.CreatedInvite
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract CreatedInvite shape: %v", err)
	}

	// The link carries the token in the fragment, and what was stored is its
	// hash — so nothing can redisplay the link later.
	_, fragment, found := strings.Cut(got.Url, "#token=")
	if !found {
		t.Fatalf("url %q does not carry the token in a fragment", got.Url)
	}
	if !strings.HasPrefix(got.Url, "/invite#") {
		t.Errorf("url %q is not the redemption path", got.Url)
	}
	if string(storedHash) == fragment {
		t.Fatal("the raw token was handed to the store")
	}
	if string(session.HashToken(fragment)) != string(storedHash) {
		t.Error("the stored hash is not the hash of the token in the link")
	}
}

func TestCreateInviteDefaultsAndBounds(t *testing.T) {
	t.Parallel()

	t.Run("default expiry", func(t *testing.T) {
		t.Parallel()
		var ttl time.Duration
		store := adminStore()
		store.createInvite = func(_ context.Context, _ uuid.UUID, _ []byte, _ string, got time.Duration) (storage.Invite, error) {
			ttl = got
			return liveInvite(), nil
		}
		rec := do(t, store, adminAPI(http.MethodPost, "/api/v1/admin/invites", `{}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
		}
		if ttl != 168*time.Hour {
			t.Errorf("default ttl = %s, want 168h", ttl)
		}
	})

	for _, tt := range []struct{ name, body string }{
		{"expiry zero", `{"expires_in_hours":0}`},
		{"expiry over max", `{"expires_in_hours":721}`},
		{"note too long", `{"note":"` + strings.Repeat("n", 201) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := adminStore()
			store.createInvite = func(context.Context, uuid.UUID, []byte, string, time.Duration) (storage.Invite, error) {
				t.Error("an out-of-bounds request reached the store")
				return storage.Invite{}, nil
			}
			rec := do(t, store, adminAPI(http.MethodPost, "/api/v1/admin/invites", tt.body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestListInvitesNeverCarriesTheLink(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.listOpenInvites = func(context.Context, storage.ListInvitesParams) ([]storage.Invite, error) {
		return []storage.Invite{liveInvite()}, nil
	}

	rec := do(t, store, request(http.MethodGet, "/api/v1/admin/invites", "", withSessionCookie("tok")))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var page api.InvitePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not the contract InvitePage shape: %v", err)
	}
	if len(page.Invites) != 1 {
		t.Fatalf("got %d invites, want 1", len(page.Invites))
	}
	if page.Invites[0].CreatedBy.Username != "member" {
		t.Errorf("created_by = %+v", page.Invites[0].CreatedBy)
	}
	for _, forbidden := range []string{"url", "token", "#token="} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("the list carries %q; the link exists in the creation response and nowhere else", forbidden)
		}
	}
}

func TestRevokeInviteIsIdempotent(t *testing.T) {
	t.Parallel()

	calls := 0
	store := adminStore()
	store.revokeInvite = func(context.Context, uuid.UUID) error {
		calls++
		return nil
	}

	for range 2 {
		rec := do(t, store, adminAPI(http.MethodDelete,
			"/api/v1/admin/invites/55555555-5555-5555-5555-555555555555", ""))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
		}
	}
	if calls != 2 {
		t.Errorf("store called %d times, want 2", calls)
	}
}

// TestUnusableInviteTokensAnswerIdentically is the anti-enumeration property
// the contract states twice: an unknown, expired, revoked or already-used
// token answers the same 404 on preview AND on redemption. The four are one
// answer at the storage boundary, so this pins that nothing above it
// reintroduces the difference — and that a signed-in caller learns no more
// than an anonymous one.
func TestUnusableInviteTokensAnswerIdentically(t *testing.T) {
	t.Parallel()

	// The store collapses all four failures into ErrNotFound; what is being
	// checked here is that every route and every principal answers the same
	// bytes for it.
	unusable := func() *fakeStore {
		f := authedStore(fixtureUser())
		f.openInviteByTokenHash = func(context.Context, []byte) (storage.Invite, error) {
			return storage.Invite{}, storage.ErrNotFound
		}
		f.redeemInvite = func(context.Context, []byte, storage.NewUser) (storage.User, error) {
			t.Error("redemption reached the store for a token that is not live")
			return storage.User{}, nil
		}
		return f
	}

	body := `{"username":"newcomer","password":"a redemption passphrase"}`
	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"preview anonymous", func() *http.Request {
			return request(http.MethodGet, "/api/v1/invites/"+matrixToken, "")
		}},
		{"preview signed in", func() *http.Request {
			return request(http.MethodGet, "/api/v1/invites/"+matrixToken, "", withSessionCookie("tok"))
		}},
		{"preview short token", func() *http.Request {
			return request(http.MethodGet, "/api/v1/invites/tooshort", "")
		}},
		{"redeem anonymous", func() *http.Request {
			return request(http.MethodPost, "/api/v1/invites/"+matrixToken, body)
		}},
		{"redeem signed in", func() *http.Request {
			return request(http.MethodPost, "/api/v1/invites/"+matrixToken, body,
				withSessionCookie("tok"), withCSRF())
		}},
		{"redeem short token", func() *http.Request {
			return request(http.MethodPost, "/api/v1/invites/tooshort", body)
		}},
	}

	var first *httptest.ResponseRecorder
	for _, tc := range cases {
		rec := do(t, unusable(), tc.req())
		wantError(t, rec, http.StatusNotFound, "invite_not_found")
		if first == nil {
			first = rec
			continue
		}
		assertIdentical(t, tc.name+" vs preview anonymous", first, rec)
	}
}

func TestPreviewInviteAnswersOnlyTheOrgName(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		openInviteByTokenHash: func(context.Context, []byte) (storage.Invite, error) {
			return liveInvite(), nil
		},
		orgSettings: func(context.Context) (storage.OrgSettings, error) {
			return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "fa"}, nil
		},
	}

	rec := do(t, store, request(http.MethodGet, "/api/v1/invites/"+matrixToken, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got api.InvitePreview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract InvitePreview shape: %v", err)
	}
	if got.OrgName != "Nest" {
		t.Errorf("org_name = %q", got.OrgName)
	}
	// It says nothing about who issued the invitation or for whom.
	for _, leaked := range []string{"member", "Member", "designer", liveInvite().ID.String()} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("preview leaked %q: %s", leaked, rec.Body.String())
		}
	}
}

func TestRedeemInviteCreatesTheAccount(t *testing.T) {
	t.Parallel()

	var got storage.NewUser
	store := &fakeStore{
		openInviteByTokenHash: func(context.Context, []byte) (storage.Invite, error) {
			return liveInvite(), nil
		},
		orgSettings: func(context.Context) (storage.OrgSettings, error) {
			// The instance default is what a new account starts in.
			return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "fa", RegistrationMode: "invite"}, nil
		},
		redeemInvite: func(_ context.Context, _ []byte, nu storage.NewUser) (storage.User, error) {
			got = nu
			return storage.User{
				ID:       uuid.MustParse("66666666-6666-6666-6666-666666666666"),
				Username: nu.Username, DisplayName: nu.DisplayName,
				PasswordHash: nu.PasswordHash, Locale: nu.Locale, IsActive: true,
			}, nil
		},
	}

	rec := do(t, store, request(http.MethodPost, "/api/v1/invites/"+matrixToken,
		`{"username":"newcomer","password":"a redemption passphrase","display_name":"New Comer"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	if got.Locale != "fa" {
		t.Errorf("locale = %q, want the instance default fa", got.Locale)
	}
	if got.MustChangePassword {
		t.Error("a redeemed account owes a password change; the person chose this password themselves")
	}
	if got.IsAdmin {
		t.Error("a redeemed account is an admin")
	}
	if got.PasswordHash == "a redemption passphrase" || got.PasswordHash == "" {
		t.Errorf("password hash = %q, want an argon2id hash", got.PasswordHash)
	}

	var summary api.UserSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("body is not the contract UserSummary shape: %v", err)
	}
	if summary.Username != "newcomer" {
		t.Errorf("username = %q", summary.Username)
	}
	if strings.Contains(rec.Body.String(), "argon2id") || strings.Contains(rec.Body.String(), "fa") {
		t.Errorf("the summary carries more than the public face of a user: %s", rec.Body.String())
	}
}

func TestRedeemInviteRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		storeErr   error
		wantStatus int
		wantCode   string
	}{
		{"username too short", `{"username":"ab","password":"a redemption passphrase"}`,
			nil, http.StatusBadRequest, "invalid_request"},
		{"password too short", `{"username":"newcomer","password":"short"}`,
			nil, http.StatusBadRequest, "invalid_request"},
		{"password over the contract bound",
			`{"username":"newcomer","password":"` + strings.Repeat("p", 201) + `"}`,
			nil, http.StatusBadRequest, "invalid_request"},
		{"display name too long",
			`{"username":"newcomer","password":"a redemption passphrase","display_name":"` + strings.Repeat("d", 65) + `"}`,
			nil, http.StatusBadRequest, "invalid_request"},
		{"username taken", `{"username":"newcomer","password":"a redemption passphrase"}`,
			storage.ErrUsernameTaken, http.StatusConflict, "username_taken"},
		{"lost the race", `{"username":"newcomer","password":"a redemption passphrase"}`,
			storage.ErrNotFound, http.StatusNotFound, "invite_not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{
				openInviteByTokenHash: func(context.Context, []byte) (storage.Invite, error) {
					return liveInvite(), nil
				},
				orgSettings: func(context.Context) (storage.OrgSettings, error) {
					return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "en"}, nil
				},
				redeemInvite: func(context.Context, []byte, storage.NewUser) (storage.User, error) {
					return storage.User{}, tt.storeErr
				},
			}
			rec := do(t, store, request(http.MethodPost, "/api/v1/invites/"+matrixToken, tt.body))
			wantError(t, rec, tt.wantStatus, tt.wantCode)
		})
	}
}

// TestRedeemInviteIgnoresRegistrationMode pins the contract's split: an
// invitation is a capability somebody handed out, and closing registration
// must not retroactively void links already sent.
func TestRedeemInviteIgnoresRegistrationMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"invite", "open"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{
				openInviteByTokenHash: func(context.Context, []byte) (storage.Invite, error) {
					return liveInvite(), nil
				},
				orgSettings: func(context.Context) (storage.OrgSettings, error) {
					return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "en", RegistrationMode: mode}, nil
				},
				redeemInvite: func(_ context.Context, _ []byte, nu storage.NewUser) (storage.User, error) {
					return storage.User{ID: uuid.New(), Username: nu.Username}, nil
				},
			}
			rec := do(t, store, request(http.MethodPost, "/api/v1/invites/"+matrixToken,
				`{"username":"newcomer","password":"a redemption passphrase"}`))
			if rec.Code != http.StatusCreated {
				t.Errorf("registration_mode %q changed redemption: status %d (body %s)", mode, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestInviteActionsAreRecorded names the invite half of this slice's audit
// vocabulary, and pins the one entry with no admin behind it.
func TestInviteActionsAreRecorded(t *testing.T) {
	t.Parallel()

	t.Run("creation and revocation carry the admin", func(t *testing.T) {
		t.Parallel()
		store := adminStore()
		store.createInvite = func(context.Context, uuid.UUID, []byte, string, time.Duration) (storage.Invite, error) {
			return liveInvite(), nil
		}
		store.revokeInvite = func(context.Context, uuid.UUID) error { return nil }

		rec := &recordingAudit{}
		handler := httpserver.Handler(store, httpserver.WithAudit(rec))
		handler.ServeHTTP(newRecorder(), adminAPI(http.MethodPost, "/api/v1/admin/invites", `{}`))
		handler.ServeHTTP(newRecorder(), adminAPI(http.MethodDelete,
			"/api/v1/admin/invites/55555555-5555-5555-5555-555555555555", ""))

		if got := rec.actions(); !equalStrings(got, []string{"invite.created", "invite.revoked"}) {
			t.Fatalf("recorded %v", got)
		}
		for _, ev := range rec.events {
			if ev.ActorID != adminID {
				t.Errorf("%s recorded actor %s, want the acting admin", ev.Action, ev.ActorID)
			}
		}
	})

	t.Run("redemption has no actor", func(t *testing.T) {
		t.Parallel()
		created := uuid.MustParse("66666666-6666-6666-6666-666666666666")
		store := &fakeStore{
			openInviteByTokenHash: func(context.Context, []byte) (storage.Invite, error) {
				return liveInvite(), nil
			},
			orgSettings: func(context.Context) (storage.OrgSettings, error) {
				return storage.OrgSettings{OrgName: "Nest", DefaultLocale: "en"}, nil
			},
			redeemInvite: func(context.Context, []byte, storage.NewUser) (storage.User, error) {
				return storage.User{ID: created, Username: "newcomer"}, nil
			},
		}

		rec := &recordingAudit{}
		httpserver.Handler(store, httpserver.WithAudit(rec)).ServeHTTP(newRecorder(),
			request(http.MethodPost, "/api/v1/invites/"+matrixToken,
				`{"username":"newcomer","password":"a redemption passphrase"}`))

		if got := rec.actions(); !equalStrings(got, []string{"invite.redeemed"}) {
			t.Fatalf("recorded %v", got)
		}
		ev := rec.events[0]
		if ev.ActorID != uuid.Nil {
			t.Errorf("actor = %s, want none: the account that redeemed the link did not exist when the request started", ev.ActorID)
		}
		if ev.TargetID != created || ev.TargetLabel != "newcomer" {
			t.Errorf("target = %s/%q, want the created account", ev.TargetID, ev.TargetLabel)
		}
	})
}

// TestInviteURLUsesTheConfiguredOrigin pins the link an admin actually
// pastes into a message: absolute when the instance knows its own origin,
// and site-relative when it does not.
func TestInviteURLUsesTheConfiguredOrigin(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.createInvite = func(context.Context, uuid.UUID, []byte, string, time.Duration) (storage.Invite, error) {
		return liveInvite(), nil
	}

	rec := newRecorder()
	httpserver.Handler(store, httpserver.WithPublicURL("https://chat.example.invalid/")).
		ServeHTTP(rec, adminAPI(http.MethodPost, "/api/v1/admin/invites", `{}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got api.CreatedInvite
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the contract CreatedInvite shape: %v", err)
	}
	if !strings.HasPrefix(got.Url, "https://chat.example.invalid/invite#token=") {
		t.Errorf("url = %q, want the configured origin with the token in the fragment", got.Url)
	}
}
