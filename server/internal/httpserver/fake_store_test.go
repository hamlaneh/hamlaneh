package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

	listSessionFamilies func(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error)
	revokeUserFamily    func(ctx context.Context, userID, familyID uuid.UUID) error
	revokeOtherFamilies func(ctx context.Context, userID, keepFamilyID uuid.UUID) error

	totpByUser                   func(ctx context.Context, userID uuid.UUID) (storage.Totp, error)
	recoveryCodeCounts           func(ctx context.Context, userID uuid.UUID) (int, int, error)
	startTotpSetup               func(ctx context.Context, userID uuid.UUID, secret []byte, ttl time.Duration) error
	verifyTotpSetup              func(ctx context.Context, v storage.TotpSetupVerification) (storage.TotpVerifyOutcome, error)
	activateTotp                 func(ctx context.Context, userID uuid.UUID) (time.Time, error)
	disableTotp                  func(ctx context.Context, userID uuid.UUID) error
	replaceRecoveryCodes         func(ctx context.Context, userID uuid.UUID, hashes func() []string) error
	createTotpChallenge          func(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error
	totpChallengeUserByTokenHash func(ctx context.Context, tokenHash []byte) (uuid.UUID, error)
	completeTotpChallenge        func(ctx context.Context, att storage.TotpChallengeAttempt) (storage.User, storage.Session, storage.TotpChallengeOutcome, error)

	createChannel       func(ctx context.Context, nc storage.NewChannel) (storage.Channel, error)
	channelForUser      func(ctx context.Context, channelID, userID uuid.UUID) (storage.Channel, error)
	updateChannelTopic  func(ctx context.Context, id uuid.UUID, topic string) (storage.Channel, error)
	listChannelsForUser func(ctx context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error)
	openDirectMessage   func(ctx context.Context, callerID, peerID uuid.UUID) (storage.Channel, bool, error)
	addChannelMember    func(ctx context.Context, channelID, userID, addedBy uuid.UUID) error
	removeChannelMember func(ctx context.Context, channelID, userID uuid.UUID) error
	listChannelMembers  func(ctx context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error)
	isChannelMember     func(ctx context.Context, channelID, userID uuid.UUID) (bool, error)
	createMessage       func(ctx context.Context, nm storage.NewMessage) (storage.Message, bool, error)
	listMessages        func(ctx context.Context, params storage.ListMessagesParams) (storage.MessagePage, error)
	setReadPosition     func(ctx context.Context, channelID, userID, messageID uuid.UUID) error
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

func (f *fakeStore) ListSessionFamilies(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error) {
	if f.listSessionFamilies == nil {
		return nil, errFakeUnwired
	}
	return f.listSessionFamilies(ctx, userID, currentFamilyID)
}

func (f *fakeStore) RevokeUserFamily(ctx context.Context, userID, familyID uuid.UUID) error {
	if f.revokeUserFamily == nil {
		return errFakeUnwired
	}
	return f.revokeUserFamily(ctx, userID, familyID)
}

func (f *fakeStore) RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	if f.revokeOtherFamilies == nil {
		return errFakeUnwired
	}
	return f.revokeOtherFamilies(ctx, userID, keepFamilyID)
}

// TotpByUser is the one method whose unwired default is a real answer rather
// than errFakeUnwired: "this account has no second factor".
//
// Login asks it before minting anything and fails closed on any other error,
// so errFakeUnwired here would turn every login test in the package — tests
// about rate limiting, enumeration, cookies, CSRF — into a 500. The fake's
// tests are overwhelmingly about something else, and a fixture account
// without two-step verification is what they all mean. A test that cares
// wires the field.
func (f *fakeStore) TotpByUser(ctx context.Context, userID uuid.UUID) (storage.Totp, error) {
	if f.totpByUser == nil {
		return storage.Totp{}, storage.ErrNotFound
	}
	return f.totpByUser(ctx, userID)
}

func (f *fakeStore) RecoveryCodeCounts(ctx context.Context, userID uuid.UUID) (int, int, error) {
	if f.recoveryCodeCounts == nil {
		return 0, 0, errFakeUnwired
	}
	return f.recoveryCodeCounts(ctx, userID)
}

func (f *fakeStore) StartTotpSetup(ctx context.Context, userID uuid.UUID, secret []byte, ttl time.Duration) error {
	if f.startTotpSetup == nil {
		return errFakeUnwired
	}
	return f.startTotpSetup(ctx, userID, secret, ttl)
}

func (f *fakeStore) VerifyTotpSetup(ctx context.Context, v storage.TotpSetupVerification) (storage.TotpVerifyOutcome, error) {
	if f.verifyTotpSetup == nil {
		return storage.TotpVerifyNoSetup, errFakeUnwired
	}
	return f.verifyTotpSetup(ctx, v)
}

func (f *fakeStore) ActivateTotp(ctx context.Context, userID uuid.UUID) (time.Time, error) {
	if f.activateTotp == nil {
		return time.Time{}, errFakeUnwired
	}
	return f.activateTotp(ctx, userID)
}

func (f *fakeStore) DisableTotp(ctx context.Context, userID uuid.UUID) error {
	if f.disableTotp == nil {
		return errFakeUnwired
	}
	return f.disableTotp(ctx, userID)
}

func (f *fakeStore) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes func() []string) error {
	if f.replaceRecoveryCodes == nil {
		return errFakeUnwired
	}
	return f.replaceRecoveryCodes(ctx, userID, hashes)
}

func (f *fakeStore) CreateTotpChallenge(ctx context.Context, userID uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	if f.createTotpChallenge == nil {
		return errFakeUnwired
	}
	return f.createTotpChallenge(ctx, userID, tokenHash, ttl)
}

// TotpChallengeUserByTokenHash follows TotpByUser's convention: the unwired
// default is the real answer "no challenge matches this token", so tests
// about other things do not need to wire the rate limiter's account lookup.
func (f *fakeStore) TotpChallengeUserByTokenHash(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	if f.totpChallengeUserByTokenHash == nil {
		return uuid.Nil, storage.ErrNotFound
	}
	return f.totpChallengeUserByTokenHash(ctx, tokenHash)
}

func (f *fakeStore) CompleteTotpChallenge(ctx context.Context, att storage.TotpChallengeAttempt) (storage.User, storage.Session, storage.TotpChallengeOutcome, error) {
	if f.completeTotpChallenge == nil {
		return storage.User{}, storage.Session{}, storage.TotpChallengeNone, errFakeUnwired
	}
	return f.completeTotpChallenge(ctx, att)
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

// assertIdentical fails when two responses differ in any way a client can
// observe: status, body, and every header, not just Content-Type. Both
// anti-enumeration properties in this package — login's unknown-user versus
// wrong-password, and reset's known versus unknown address — are exactly
// this assertion, and a leak hiding in some other header would be just as
// real a leak.
func assertIdentical(t *testing.T, what string, a, b *httptest.ResponseRecorder) {
	t.Helper()

	if a.Code != b.Code {
		t.Errorf("%s: statuses differ: %d vs %d", what, a.Code, b.Code)
	}
	if a.Body.String() != b.Body.String() {
		t.Errorf("%s: bodies differ: %q vs %q", what, a.Body.String(), b.Body.String())
	}
	if len(a.Header()) != len(b.Header()) {
		t.Errorf("%s: header sets differ: %v vs %v", what, a.Header(), b.Header())
		return
	}
	for name, values := range a.Header() {
		other := b.Header().Values(name)
		if len(values) != len(other) {
			t.Errorf("%s: header %s differs: %v vs %v", what, name, values, other)
			continue
		}
		for i := range values {
			if values[i] != other[i] {
				t.Errorf("%s: header %s differs: %v vs %v", what, name, values, other)
				break
			}
		}
	}
}

func (f *fakeStore) CreateChannel(ctx context.Context, nc storage.NewChannel) (storage.Channel, error) {
	if f.createChannel == nil {
		return storage.Channel{}, errFakeUnwired
	}
	return f.createChannel(ctx, nc)
}

func (f *fakeStore) ChannelForUser(ctx context.Context, channelID, userID uuid.UUID) (storage.Channel, error) {
	if f.channelForUser == nil {
		return storage.Channel{}, errFakeUnwired
	}
	return f.channelForUser(ctx, channelID, userID)
}

func (f *fakeStore) UpdateChannelTopic(ctx context.Context, id uuid.UUID, topic string) (storage.Channel, error) {
	if f.updateChannelTopic == nil {
		return storage.Channel{}, errFakeUnwired
	}
	return f.updateChannelTopic(ctx, id, topic)
}

func (f *fakeStore) ListChannelsForUser(ctx context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error) {
	if f.listChannelsForUser == nil {
		return nil, errFakeUnwired
	}
	return f.listChannelsForUser(ctx, userID, params)
}

func (f *fakeStore) OpenDirectMessage(ctx context.Context, callerID, peerID uuid.UUID) (storage.Channel, bool, error) {
	if f.openDirectMessage == nil {
		return storage.Channel{}, false, errFakeUnwired
	}
	return f.openDirectMessage(ctx, callerID, peerID)
}

func (f *fakeStore) AddChannelMember(ctx context.Context, channelID, userID, addedBy uuid.UUID) error {
	if f.addChannelMember == nil {
		return errFakeUnwired
	}
	return f.addChannelMember(ctx, channelID, userID, addedBy)
}

func (f *fakeStore) RemoveChannelMember(ctx context.Context, channelID, userID uuid.UUID) error {
	if f.removeChannelMember == nil {
		return errFakeUnwired
	}
	return f.removeChannelMember(ctx, channelID, userID)
}

func (f *fakeStore) ListChannelMembers(ctx context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error) {
	if f.listChannelMembers == nil {
		return nil, errFakeUnwired
	}
	return f.listChannelMembers(ctx, channelID, params)
}

func (f *fakeStore) IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	if f.isChannelMember == nil {
		return false, errFakeUnwired
	}
	return f.isChannelMember(ctx, channelID, userID)
}

func (f *fakeStore) CreateMessage(ctx context.Context, nm storage.NewMessage) (storage.Message, bool, error) {
	if f.createMessage == nil {
		return storage.Message{}, false, errFakeUnwired
	}
	return f.createMessage(ctx, nm)
}

func (f *fakeStore) ListMessages(ctx context.Context, params storage.ListMessagesParams) (storage.MessagePage, error) {
	if f.listMessages == nil {
		return storage.MessagePage{}, errFakeUnwired
	}
	return f.listMessages(ctx, params)
}

func (f *fakeStore) SetReadPosition(ctx context.Context, channelID, userID, messageID uuid.UUID) error {
	if f.setReadPosition == nil {
		return errFakeUnwired
	}
	return f.setReadPosition(ctx, channelID, userID, messageID)
}
