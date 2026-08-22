package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const sessionsPath = "/api/v1/users/me/sessions"

// authedSessionStore wires a fake that authenticates fixtureUser for any
// access token; each test wires the session-family methods it exercises.
func authedSessionStore() *fakeStore {
	return authedStore(fixtureUser())
}

// sessionRequest builds an authenticated request to a sessions endpoint.
func sessionRequest(method, path string) *http.Request {
	return request(method, path, "", withSessionCookie("any-access-token"), withCSRF())
}

// TestListMySessionsRendersTheContractShape pins the JSON one settings row
// draws, and pins that the query is scoped by the authenticating session
// rather than by anything the request carries.
func TestListMySessionsRendersTheContractShape(t *testing.T) {
	t.Parallel()

	ip := netip.MustParseAddr("203.0.113.7")
	lastActive := time.Date(2026, 8, 21, 9, 14, 0, 0, time.UTC)

	var gotUserID, gotFamilyID uuid.UUID
	store := authedSessionStore()
	store.listSessionFamilies = func(_ context.Context, userID, currentFamilyID uuid.UUID) ([]storage.SessionFamily, error) {
		gotUserID, gotFamilyID = userID, currentFamilyID
		return []storage.SessionFamily{
			{
				FamilyID:     fixtureSession().FamilyID,
				UserAgent:    "this device",
				IP:           &ip,
				LastActiveAt: lastActive,
				Current:      true,
			},
			{
				FamilyID:     uuid.MustParse("44444444-4444-4444-4444-444444444444"),
				UserAgent:    "some phone",
				LastActiveAt: lastActive.Add(-time.Hour),
			},
		}, nil
	}

	rec := do(t, store, sessionRequest(http.MethodGet, sessionsPath))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	if gotUserID != fixtureUser().ID {
		t.Errorf("listed sessions of %s, want the authenticated user %s", gotUserID, fixtureUser().ID)
	}
	if gotFamilyID != fixtureSession().FamilyID {
		t.Errorf("current family %s, want the authenticating session's %s", gotFamilyID, fixtureSession().FamilyID)
	}

	var list api.SessionFamilyList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("body %q is not the contract SessionFamilyList shape: %v", rec.Body.String(), err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("got %d rows, want 2", len(list.Sessions))
	}
	if !list.Sessions[0].Current || list.Sessions[1].Current {
		t.Error("current is not set on exactly the first row")
	}
	if list.Sessions[0].Ip == nil || *list.Sessions[0].Ip != "203.0.113.7" {
		t.Errorf("ip = %v, want 203.0.113.7", list.Sessions[0].Ip)
	}
	if list.Sessions[1].Ip != nil {
		t.Errorf("ip = %v for a generation that recorded none, want null", *list.Sessions[1].Ip)
	}
	if !list.Sessions[0].LastActiveAt.Equal(lastActive) {
		t.Errorf("last_active_at = %s, want %s", list.Sessions[0].LastActiveAt, lastActive)
	}

	// location is in the contract but has no source yet: it must stay null
	// rather than be invented.
	for i, row := range rawSessionRows(t, rec.Body.Bytes()) {
		if raw, present := row["location"]; present && string(raw) != "null" {
			t.Errorf("row %d invented a location: %s", i, raw)
		}
	}
}

// rawSessionRows decodes the sessions array without applying the generated
// types, so field presence can be inspected.
func rawSessionRows(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()

	var envelope struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode sessions array: %v", err)
	}
	return envelope.Sessions
}

// TestListMySessionsEmptyIsAnArray pins that a caller with nothing to show
// gets [], never null — the contract makes sessions required.
func TestListMySessionsEmptyIsAnArray(t *testing.T) {
	t.Parallel()

	store := authedSessionStore()
	store.listSessionFamilies = func(context.Context, uuid.UUID, uuid.UUID) ([]storage.SessionFamily, error) {
		return nil, nil
	}

	rec := do(t, store, sessionRequest(http.MethodGet, sessionsPath))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"sessions":[]}` {
		t.Errorf("body = %s, want {\"sessions\":[]}", got)
	}
}

// TestRevokeSessionFamilyRefusesTheCurrentFamily pins that the refusal
// happens before storage is asked to do anything: signing this device out is
// logout, and the design gives the current row no sign-out control.
func TestRevokeSessionFamilyRefusesTheCurrentFamily(t *testing.T) {
	t.Parallel()

	revoked := false
	store := authedSessionStore()
	store.revokeUserFamily = func(context.Context, uuid.UUID, uuid.UUID) error {
		revoked = true
		return nil
	}

	rec := do(t, store, sessionRequest(http.MethodDelete, sessionsPath+"/"+fixtureSession().FamilyID.String()))
	wantError(t, rec, http.StatusBadRequest, "cannot_revoke_current_session")
	if revoked {
		t.Error("storage was asked to revoke the current family")
	}
}

// TestRevokeSessionFamilyPassesTheCallersScope pins that the user id handed
// to storage is the authenticated one, so the path parameter can only ever
// name a family inside the caller's own account.
func TestRevokeSessionFamilyPassesTheCallersScope(t *testing.T) {
	t.Parallel()

	target := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	var gotUserID, gotFamilyID uuid.UUID
	store := authedSessionStore()
	store.revokeUserFamily = func(_ context.Context, userID, familyID uuid.UUID) error {
		gotUserID, gotFamilyID = userID, familyID
		return nil
	}

	rec := do(t, store, sessionRequest(http.MethodDelete, sessionsPath+"/"+target.String()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if gotUserID != fixtureUser().ID {
		t.Errorf("revoked within account %s, want the authenticated user %s", gotUserID, fixtureUser().ID)
	}
	if gotFamilyID != target {
		t.Errorf("revoked family %s, want %s", gotFamilyID, target)
	}
}

// TestRevokeSessionFamilyNotFound pins the answer for a family the caller
// does not own: 404, the same as one that does not exist.
func TestRevokeSessionFamilyNotFound(t *testing.T) {
	t.Parallel()

	store := authedSessionStore()
	store.revokeUserFamily = func(context.Context, uuid.UUID, uuid.UUID) error {
		return storage.ErrNotFound
	}

	rec := do(t, store, sessionRequest(http.MethodDelete,
		sessionsPath+"/55555555-5555-5555-5555-555555555555"))
	wantError(t, rec, http.StatusNotFound, "session_not_found")
}

// TestRevokeOtherSessionsPassesTheCallersFamily pins that the family kept
// alive is the one that authenticated the request.
func TestRevokeOtherSessionsPassesTheCallersFamily(t *testing.T) {
	t.Parallel()

	var gotUserID, gotKeep uuid.UUID
	store := authedSessionStore()
	store.revokeOtherFamilies = func(_ context.Context, userID, keepFamilyID uuid.UUID) error {
		gotUserID, gotKeep = userID, keepFamilyID
		return nil
	}

	rec := do(t, store, sessionRequest(http.MethodPost, sessionsPath+"/revoke-others"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if gotUserID != fixtureUser().ID {
		t.Errorf("swept account %s, want the authenticated user %s", gotUserID, fixtureUser().ID)
	}
	if gotKeep != fixtureSession().FamilyID {
		t.Errorf("kept family %s, want the authenticating session's %s", gotKeep, fixtureSession().FamilyID)
	}
}

// TestSessionEndpointsSurfaceStorageFailures pins that a broken store is a
// generic 500, never a partial success or a leaked cause.
func TestSessionEndpointsSurfaceStorageFailures(t *testing.T) {
	t.Parallel()

	boom := errors.New("database is on fire")
	tests := []struct {
		name   string
		wire   func(*fakeStore)
		method string
		path   string
	}{
		{
			name: "list",
			wire: func(f *fakeStore) {
				f.listSessionFamilies = func(context.Context, uuid.UUID, uuid.UUID) ([]storage.SessionFamily, error) {
					return nil, boom
				}
			},
			method: http.MethodGet,
			path:   sessionsPath,
		},
		{
			name: "revoke one",
			wire: func(f *fakeStore) {
				f.revokeUserFamily = func(context.Context, uuid.UUID, uuid.UUID) error { return boom }
			},
			method: http.MethodDelete,
			path:   sessionsPath + "/55555555-5555-5555-5555-555555555555",
		},
		{
			name: "revoke others",
			wire: func(f *fakeStore) {
				f.revokeOtherFamilies = func(context.Context, uuid.UUID, uuid.UUID) error { return boom }
			},
			method: http.MethodPost,
			path:   sessionsPath + "/revoke-others",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := authedSessionStore()
			tc.wire(store)

			rec := do(t, store, sessionRequest(tc.method, tc.path))
			wantError(t, rec, http.StatusInternalServerError, "internal_error")
			if body := rec.Body.String(); strings.Contains(body, "on fire") {
				t.Errorf("the internal cause leaked to the client: %s", body)
			}
		})
	}
}

// listSessions calls the sessions endpoint with sc and decodes the list.
func listSessions(t *testing.T, handler http.Handler, sc sessionCookies) api.SessionFamilyList {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodGet, sessionsPath, "", withSession(sc)))
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	var list api.SessionFamilyList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode session list: %v", err)
	}
	return list
}

// withUserAgent stamps a device name on a request, so a session family can be
// recognised in the list by the User-Agent its generation recorded.
func withUserAgent(ua string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("User-Agent", ua) }
}

// signIn logs a fixture user in from a named device at a named address.
func signIn(t *testing.T, handler http.Handler, identifier, pw, device, addr string) sessionCookies {
	t.Helper()

	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/login", loginBody(identifier, pw),
		withUserAgent(device), withRemoteAddr(addr)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login from %s: got status %d (body %s)", device, rec.Code, rec.Body.String())
	}
	cookies := responseCookies(rec)
	return sessionCookies{
		access:  cookieByName(t, cookies, session.AccessCookie).Value,
		refresh: cookieByName(t, cookies, session.RefreshCookie).Value,
		csrf:    cookieByName(t, cookies, session.CSRFCookie).Value,
	}
}

// deviceNames lists the User-Agent of every row, in order.
func deviceNames(list api.SessionFamilyList) []string {
	names := make([]string, 0, len(list.Sessions))
	for _, row := range list.Sessions {
		names = append(names, row.UserAgent)
	}
	return names
}

// familyOf returns the family id of the row with the given device name.
func familyOf(t *testing.T, list api.SessionFamilyList, device string) uuid.UUID {
	t.Helper()

	for _, row := range list.Sessions {
		if row.UserAgent == device {
			return row.FamilyId
		}
	}
	t.Fatalf("no session row for device %q (rows %v)", device, deviceNames(list))
	return uuid.Nil
}

// mustSignUp creates a fixture account with a known password.
func mustSignUp(ctx context.Context, t *testing.T, store *storage.Store, username, pw string) {
	t.Helper()

	if _, err := store.CreateUser(ctx, storage.NewUser{
		Username: username, PasswordHash: password.Hash(pw), Locale: "en",
	}); err != nil {
		t.Fatalf("create fixture user %s: %v", username, err)
	}
}

// TestSessionsListIntegration walks the settings Sessions list through the
// real stack: three devices, the caller's own first, and nothing from any
// other account.
func TestSessionsListIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	const pw = "the listing passphrase"
	mustSignUp(ctx, t, store, "listcaller", pw)
	mustSignUp(ctx, t, store, "listbystander", pw)

	// The caller is the OLDEST device, so current-first ordering has to beat
	// recency for this to pass.
	caller := signIn(t, handler, "listcaller", pw, "device-desktop", "203.0.113.10:5000")
	phone := signIn(t, handler, "listcaller", pw, "device-phone", "203.0.113.11:5000")
	tablet := signIn(t, handler, "listcaller", pw, "device-tablet", "203.0.113.12:5000")
	bystander := signIn(t, handler, "listbystander", pw, "device-stranger", "203.0.113.99:5000")

	list := listSessions(t, handler, caller)
	want := []string{"device-desktop", "device-tablet", "device-phone"}
	if got := deviceNames(list); !slices.Equal(got, want) {
		t.Fatalf("session rows:\n got %v\nwant %v", got, want)
	}
	if !list.Sessions[0].Current {
		t.Error("the requesting device is not marked current")
	}
	for _, row := range list.Sessions[1:] {
		if row.Current {
			t.Errorf("device %q is marked current but did not make the request", row.UserAgent)
		}
	}
	if list.Sessions[0].Ip == nil || *list.Sessions[0].Ip != "203.0.113.10" {
		t.Errorf("ip = %v, want the address the session was created from", list.Sessions[0].Ip)
	}

	// The bystander sees exactly one row: their own.
	other := listSessions(t, handler, bystander)
	if got := deviceNames(other); !slices.Equal(got, []string{"device-stranger"}) {
		t.Errorf("bystander's list:\n got %v\nwant [device-stranger]", got)
	}

	// Refreshing is the heartbeat: it moves a family up and updates the row.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "",
		withSession(phone), withUserAgent("device-phone-renamed"), withRemoteAddr("203.0.113.21:5000")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh the phone: got status %d (body %s)", rec.Code, rec.Body.String())
	}

	list = listSessions(t, handler, caller)
	want = []string{"device-desktop", "device-phone-renamed", "device-tablet"}
	if got := deviceNames(list); !slices.Equal(got, want) {
		t.Fatalf("session rows after a refresh:\n got %v\nwant %v", got, want)
	}
	if list.Sessions[1].Ip == nil || *list.Sessions[1].Ip != "203.0.113.21" {
		t.Errorf("ip = %v, want the newest generation's address", list.Sessions[1].Ip)
	}

	// A revoked family leaves the list.
	tabletFamily := familyOf(t, list, "device-tablet")
	rec = doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+tabletFamily.String(), "",
		withSession(caller)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke the tablet: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := deviceNames(listSessions(t, handler, caller)); !slices.Equal(got, []string{"device-desktop", "device-phone-renamed"}) {
		t.Errorf("session rows after revoking the tablet: got %v", got)
	}
	if me(t, handler, tablet) != http.StatusUnauthorized {
		t.Error("the revoked device's access token still authenticates")
	}
}

// TestRevokeSessionFamilyIntegration walks remote sign-out through the real
// stack, including the two answers that must never be confused: the current
// family refuses, and another account's family is not found AND not touched.
func TestRevokeSessionFamilyIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	const pw = "the revoking passphrase"
	mustSignUp(ctx, t, store, "revokecaller", pw)
	mustSignUp(ctx, t, store, "revokevictim", pw)

	caller := signIn(t, handler, "revokecaller", pw, "device-here", "203.0.113.30:5000")
	away := signIn(t, handler, "revokecaller", pw, "device-away", "203.0.113.31:5000")
	victim := signIn(t, handler, "revokevictim", pw, "device-victim", "203.0.113.32:5000")

	// The away device has rotated once, so its family has two generations:
	// revocation has to reach every one of them.
	rec := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "",
		withSession(away), withUserAgent("device-away")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("refresh the away device: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	rotatedAway := away
	rotatedAway.access = cookieByName(t, responseCookies(rec), session.AccessCookie).Value
	rotatedAway.refresh = cookieByName(t, responseCookies(rec), session.RefreshCookie).Value

	callerList := listSessions(t, handler, caller)
	awayFamily := familyOf(t, callerList, "device-away")
	callerFamily := familyOf(t, callerList, "device-here")

	t.Run("the current family refuses and keeps working", func(t *testing.T) {
		rec := doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+callerFamily.String(), "",
			withSession(caller)))
		wantError(t, rec, http.StatusBadRequest, "cannot_revoke_current_session")

		if me(t, handler, caller) != http.StatusOK {
			t.Error("the refused request signed the caller out anyway")
		}
	})

	t.Run("another account's family is not found and is not revoked", func(t *testing.T) {
		victimList := listSessions(t, handler, victim)
		victimFamily := familyOf(t, victimList, "device-victim")

		rec := doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+victimFamily.String(), "",
			withSession(caller)))
		wantError(t, rec, http.StatusNotFound, "session_not_found")

		// The status code alone would hide the real bug.
		if me(t, handler, victim) != http.StatusOK {
			t.Error("the victim's session was revoked by a caller who does not own it")
		}
	})

	t.Run("an unknown family is not found", func(t *testing.T) {
		rec := doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+uuid.New().String(), "",
			withSession(caller)))
		wantError(t, rec, http.StatusNotFound, "session_not_found")
	})

	t.Run("revoking kills every generation of the family", func(t *testing.T) {
		rec := doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+awayFamily.String(), "",
			withSession(caller)))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("revoke the away device: got status %d (body %s)", rec.Code, rec.Body.String())
		}

		if me(t, handler, rotatedAway) != http.StatusUnauthorized {
			t.Error("the revoked family's access token still authenticates")
		}
		// The live refresh token is the one that matters: a half-revoked
		// family would mint a brand new session from it.
		refreshed := doHandler(t, handler, request(http.MethodPost, "/api/v1/auth/refresh", "",
			withSession(rotatedAway)))
		if refreshed.Code != http.StatusUnauthorized {
			t.Errorf("the revoked family still refreshes: got status %d", refreshed.Code)
		}
		// The rotated-away generation is dead too.
		if me(t, handler, away) != http.StatusUnauthorized {
			t.Error("an older generation of the revoked family still authenticates")
		}
	})

	t.Run("revoking again is still 204", func(t *testing.T) {
		rec := doHandler(t, handler, request(http.MethodDelete, sessionsPath+"/"+awayFamily.String(), "",
			withSession(caller)))
		if rec.Code != http.StatusNoContent {
			t.Errorf("re-revoking an already revoked family: got status %d (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestRevokeOtherSessionsIntegration walks "sign out everywhere else": the
// caller survives alone, other accounts are untouched, and doing it with
// nothing else signed in still succeeds.
func TestRevokeOtherSessionsIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	handler := httpserver.Handler(store)

	const pw = "the sweeping passphrase"
	mustSignUp(ctx, t, store, "sweepcaller", pw)
	mustSignUp(ctx, t, store, "sweepbystander", pw)

	caller := signIn(t, handler, "sweepcaller", pw, "device-here", "203.0.113.40:5000")
	first := signIn(t, handler, "sweepcaller", pw, "device-one", "203.0.113.41:5000")
	second := signIn(t, handler, "sweepcaller", pw, "device-two", "203.0.113.42:5000")
	bystander := signIn(t, handler, "sweepbystander", pw, "device-stranger", "203.0.113.99:5000")

	rec := doHandler(t, handler, request(http.MethodPost, sessionsPath+"/revoke-others", "", withSession(caller)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke others: got status %d (body %s)", rec.Code, rec.Body.String())
	}

	if me(t, handler, caller) != http.StatusOK {
		t.Error("the calling session was swept away with the others")
	}
	for name, sc := range map[string]sessionCookies{"device-one": first, "device-two": second} {
		if me(t, handler, sc) != http.StatusUnauthorized {
			t.Errorf("%s survived sign-out-everywhere-else", name)
		}
	}
	if me(t, handler, bystander) != http.StatusOK {
		t.Error("an unrelated account was signed out")
	}

	if got := deviceNames(listSessions(t, handler, caller)); !slices.Equal(got, []string{"device-here"}) {
		t.Errorf("session rows after the sweep: got %v, want [device-here]", got)
	}

	// Nothing else is signed in; the answer is still 204.
	rec = doHandler(t, handler, request(http.MethodPost, sessionsPath+"/revoke-others", "", withSession(caller)))
	if rec.Code != http.StatusNoContent {
		t.Errorf("revoke others with nothing else signed in: got status %d (body %s)", rec.Code, rec.Body.String())
	}
	if me(t, handler, caller) != http.StatusOK {
		t.Error("the second sweep signed the caller out")
	}
}
