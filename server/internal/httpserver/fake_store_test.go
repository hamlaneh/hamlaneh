package httpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// errFakeUnwired reports a fakeStore method the test did not stub.
var errFakeUnwired = errors.New("fakeStore: method not wired in this test")

// fakeStore implements httpserver.Store with per-test function fields. Any
// unwired method fails with errFakeUnwired, so tests cannot silently rely
// on behavior they did not define.
type fakeStore struct {
	ready                   func(ctx context.Context) error
	userByIdentifier        func(ctx context.Context, identifier string) (storage.User, error)
	createUser              func(ctx context.Context, nu storage.NewUser) (storage.User, error)
	updatePassword          func(ctx context.Context, userID uuid.UUID, hash string, keepFamilyID uuid.UUID) error
	updatePasswordHash      func(ctx context.Context, userID uuid.UUID, hash string) error
	listUsers               func(ctx context.Context, params storage.ListUsersParams) ([]storage.User, error)
	createSession           func(ctx context.Context, ns storage.NewSession) (storage.Session, error)
	sessionUserByAccessHash func(ctx context.Context, accessHash []byte) (storage.Session, storage.User, error)
	rotateSession           func(ctx context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error)
	revokeFamily            func(ctx context.Context, familyID uuid.UUID) error
}

var _ httpserver.Store = (*fakeStore)(nil)

func (f *fakeStore) Ready(ctx context.Context) error {
	if f.ready == nil {
		return errFakeUnwired
	}
	return f.ready(ctx)
}

func (f *fakeStore) UserByIdentifier(ctx context.Context, identifier string) (storage.User, error) {
	if f.userByIdentifier == nil {
		return storage.User{}, errFakeUnwired
	}
	return f.userByIdentifier(ctx, identifier)
}

func (f *fakeStore) CreateUser(ctx context.Context, nu storage.NewUser) (storage.User, error) {
	if f.createUser == nil {
		return storage.User{}, errFakeUnwired
	}
	return f.createUser(ctx, nu)
}

func (f *fakeStore) UpdatePassword(ctx context.Context, userID uuid.UUID, hash string, keepFamilyID uuid.UUID) error {
	if f.updatePassword == nil {
		return errFakeUnwired
	}
	return f.updatePassword(ctx, userID, hash, keepFamilyID)
}

func (f *fakeStore) UpdatePasswordHash(ctx context.Context, userID uuid.UUID, hash string) error {
	if f.updatePasswordHash == nil {
		return errFakeUnwired
	}
	return f.updatePasswordHash(ctx, userID, hash)
}

func (f *fakeStore) ListUsers(ctx context.Context, params storage.ListUsersParams) ([]storage.User, error) {
	if f.listUsers == nil {
		return nil, errFakeUnwired
	}
	return f.listUsers(ctx, params)
}

func (f *fakeStore) CreateSession(ctx context.Context, ns storage.NewSession) (storage.Session, error) {
	if f.createSession == nil {
		return storage.Session{}, errFakeUnwired
	}
	return f.createSession(ctx, ns)
}

func (f *fakeStore) SessionUserByAccessHash(ctx context.Context, accessHash []byte) (storage.Session, storage.User, error) {
	if f.sessionUserByAccessHash == nil {
		return storage.Session{}, storage.User{}, errFakeUnwired
	}
	return f.sessionUserByAccessHash(ctx, accessHash)
}

func (f *fakeStore) RotateSession(ctx context.Context, refreshHash []byte, next storage.SessionTokens) (storage.Session, storage.RotateOutcome, error) {
	if f.rotateSession == nil {
		return storage.Session{}, storage.RotateOutcomeInvalid, errFakeUnwired
	}
	return f.rotateSession(ctx, refreshHash, next)
}

func (f *fakeStore) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	if f.revokeFamily == nil {
		return errFakeUnwired
	}
	return f.revokeFamily(ctx, familyID)
}

// fixturePassword is the known password of every fixture user; its argon2
// hash is computed once because hashing is deliberately slow.
const fixturePassword = "correct horse battery staple"

var fixtureHash = sync.OnceValue(func() string {
	return password.Hash(fixturePassword)
})

// fixtureUser is a member account with the fixture password.
func fixtureUser() storage.User {
	return storage.User{
		ID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Username:     "member",
		DisplayName:  "Member",
		PasswordHash: fixtureHash(),
		Locale:       "en",
	}
}

// fixtureSession is a live session for fixtureUser.
func fixtureSession() storage.Session {
	return storage.Session{
		ID:       uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		UserID:   uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		FamilyID: uuid.MustParse("33333333-3333-3333-3333-333333333333"),
	}
}

// authedStore wires a fakeStore that authenticates the given user for any
// access token (the middleware's hash lookup is the fake's concern, not the
// test's).
func authedStore(user storage.User) *fakeStore {
	return &fakeStore{
		sessionUserByAccessHash: func(_ context.Context, _ []byte) (storage.Session, storage.User, error) {
			return fixtureSession(), user, nil
		},
	}
}

// request builds a JSON API request. mods adjust cookies, headers, and the
// remote address.
func request(method, path, body string, mods ...func(*http.Request)) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, mod := range mods {
		mod(req)
	}
	return req
}

// withSessionCookie attaches an access cookie.
func withSessionCookie(value string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: session.AccessCookie, Value: value})
	}
}

// withCSRF attaches a matching CSRF cookie/header pair. The server never
// stores CSRF values (double-submit), so any non-empty pair is valid.
func withCSRF() func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: session.CSRFCookie, Value: "csrf-fixture-value"})
		r.Header.Set(session.CSRFHeader, "csrf-fixture-value")
	}
}

// do serves req against a handler built on store and returns the recorder.
func do(t *testing.T, store httpserver.Store, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	httpserver.Handler(store).ServeHTTP(rec, req)
	return rec
}

// wantError asserts the contract Error envelope: status, code, and a JSON
// body matching the generated schema.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	if rec.Code != status {
		t.Errorf("got status %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("got Content-Type %q, want application/json", ct)
	}
	var body api.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not the contract Error shape: %v", rec.Body.String(), err)
	}
	if body.Error.Code != code {
		t.Errorf("got error code %q, want %q (body %s)", body.Error.Code, code, rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Error("error message is empty")
	}
}

// responseCookies decodes the Set-Cookie headers of a response.
func responseCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	return rec.Result().Cookies()
}

// cookieByName finds a response cookie or fails the test.
func cookieByName(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("response has no cookie %q", name)
	return nil
}

// loginBody builds a login request body.
func loginBody(identifier, pw string) string {
	b, err := json.Marshal(map[string]string{"identifier": identifier, "password": pw})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// equalResponses reports whether two recorded responses are byte-identical
// in status, body, and content type.
func equalResponses(a, b *httptest.ResponseRecorder) bool {
	return a.Code == b.Code &&
		bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) &&
		a.Header().Get("Content-Type") == b.Header().Get("Content-Type")
}
