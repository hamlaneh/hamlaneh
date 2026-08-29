package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/api"
	"github.com/hamlaneh/hamlaneh/server/internal/calls"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The conference surface: the widest door in the product, and the tests of
// what keeps it narrow.
//
// What is NOT here: the grant shape of a guest ticket, asserted against the
// decoded claims in internal/calls; the collapse of unknown, expired and
// revoked into one storage answer, asserted against a real database in
// internal/storage; and the authorization matrix, which runs all five
// endpoints against a real database in internal/authztest. What IS here is
// the boundary between them — that the three refusals are the same refusal at
// the HTTP edge, that a guest gets a ticket and nothing else, and that a
// revocation ends the meeting before it kills the link.

const testConferenceID = "dddddddd-0000-0000-0000-000000000001"

func conferenceUUID() uuid.UUID { return uuid.MustParse(testConferenceID) }

// meetPath is a link-holder's path, asked with a well-formed token.
func meetPath(suffix string) string {
	return "/api/v1/meet/" + strings.Repeat("z", session.TokenLen) + suffix
}

// liveConference is the store's answer for a link that still admits somebody.
func liveConference() storage.Conference {
	return storage.Conference{
		ID:        conferenceUUID(),
		Title:     "Weekly sync",
		CreatedBy: &storage.ConferenceCreator{ID: adminID, Username: "member", DisplayName: "Member"},
		CreatedAt: time.Now().Add(-time.Hour),
	}
}

// conferenceStore answers every conference read with one live conference, and
// nothing else. Every user-facing method is deliberately left unwired: a
// handler that reached for an account would fail with errFakeUnwired, which
// is how "a guest reads no account" is asserted rather than assumed.
func conferenceStore() *fakeStore {
	store := &fakeStore{}
	store.liveConferenceByTokenHash = func(context.Context, []byte) (storage.Conference, error) {
		return liveConference(), nil
	}
	return store
}

// doConference serves req with svc as the media plane and an audit recorder.
func doConference(t *testing.T, store httpserver.Store, svc *calls.Service,
	audit *recordingAudit, req *http.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	return doHandler(t, httpserver.Handler(store,
		httpserver.WithCalls(svc), httpserver.WithAudit(audit)), req)
}

// TestConferenceLinkRefusalsAreIndistinguishable is the property the public
// half of this surface rests on: a visitor learns whether their link works,
// never why it does not.
//
// The three ways a link can be dead reach this handler as one storage answer,
// so what is asserted here is that the HTTP edge does not put the distinction
// back: identical status, identical code, identical bytes, identical headers,
// on both public routes. Mutating the handler to answer any one of them
// differently turns this red.
func TestConferenceLinkRefusalsAreIndistinguishable(t *testing.T) {
	t.Parallel()

	// Storage collapses the three; the fake reproduces that, because what is
	// under test is everything above it.
	deadLink := func() *fakeStore {
		store := &fakeStore{}
		store.liveConferenceByTokenHash = func(context.Context, []byte) (storage.Conference, error) {
			return storage.Conference{}, storage.ErrNotFound
		}
		return store
	}

	svc, _ := mediaServer(t)
	for name, req := range map[string]func() *http.Request{
		"preview": func() *http.Request { return request(http.MethodGet, meetPath(""), "") },
		"join": func() *http.Request {
			return request(http.MethodPost, meetPath("/join"), `{"display_name":"A Guest"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var bodies []string
			for _, kind := range []string{"unknown", "expired", "revoked"} {
				rec := doConference(t, deadLink(), svc, &recordingAudit{}, req())
				wantError(t, rec, http.StatusNotFound, "conference_not_found")
				if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
					t.Errorf("%s answered Content-Type %q", kind, ct)
				}
				bodies = append(bodies, rec.Body.String())
			}
			for i := 1; i < len(bodies); i++ {
				if bodies[i] != bodies[0] {
					t.Errorf("two refusals differ:\n%s\n%s", bodies[0], bodies[i])
				}
			}
		})
	}
}

// TestConferenceRefusalTimingIsUniform is the other half of indistinguishable:
// a refusal that took visibly longer would answer the question the status code
// refuses to.
//
// The three dead links reach the handler through one call site and one query,
// so the medians should be indistinguishable. The tolerance is deliberately
// wide — this is a scheduler-noise-tolerant check for a CATEGORICAL
// difference, the kind an extra lookup or a hash on one path would produce,
// not a side-channel measurement.
func TestConferenceRefusalTimingIsUniform(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	store := &fakeStore{}
	store.liveConferenceByTokenHash = func(context.Context, []byte) (storage.Conference, error) {
		return storage.Conference{}, storage.ErrNotFound
	}
	handler := httpserver.Handler(store, httpserver.WithCalls(svc),
		httpserver.WithAudit(&recordingAudit{}))

	median := func(token string) time.Duration {
		const runs = 25
		samples := make([]time.Duration, 0, runs)
		for range runs {
			req := request(http.MethodGet, "/api/v1/meet/"+token, "")
			start := time.Now()
			handler.ServeHTTP(httptest.NewRecorder(), req)
			samples = append(samples, time.Since(start))
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[len(samples)/2]
	}

	// Three well-formed links that were never issued, an expired one and a
	// revoked one: from outside this handler they are the same request, and
	// the store answers all of them the same way. What is being watched for
	// is a handler that grew a branch one of them takes and the others do not.
	timings := map[string]time.Duration{
		"unknown": median(strings.Repeat("z", session.TokenLen)),
		"expired": median(strings.Repeat("y", session.TokenLen)),
		"revoked": median(strings.Repeat("x", session.TokenLen)),
	}
	var lo, hi time.Duration
	for _, d := range timings {
		if lo == 0 || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	// A factor of five over medians measured in microseconds is noise; a
	// branch that hashed, queried or slept on one path is orders of magnitude.
	if lo > 0 && hi > 5*lo {
		t.Errorf("refusal timings differ categorically: %v", timings)
	}
}

// TestJoinMintsATicketAndNothingElse is the guest boundary. What the response
// carries is the whole of what the link bought.
func TestJoinMintsATicketAndNothingElse(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	rec := doConference(t, conferenceStore(), svc, &recordingAudit{},
		request(http.MethodPost, meetPath("/join"), `{"display_name":"Sara"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// No session, and nothing that could become one. A guest becomes a member
	// of nothing, and a Set-Cookie here would be exactly that.
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("the join set %d cookies; a guest gets no session", len(cookies))
	}

	var ticket api.CallToken
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("body %q is not a CallToken: %v", rec.Body.String(), err)
	}
	if ticket.Room != "conf-"+testConferenceID {
		t.Errorf("room = %q, want the conference's room", ticket.Room)
	}
	if ticket.Token == "" {
		t.Error("no token in the ticket")
	}
	if lifetime := time.Until(ticket.ExpiresAt); lifetime <= 0 || lifetime > calls.JoinTTL {
		t.Errorf("ticket expires in %s, want within the member ticket's %s", lifetime, calls.JoinTTL)
	}

	// A CallToken and nothing more: no conference id, no title, no instance
	// name, and no field a member's ticket does not also have.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an object: %v", rec.Body.String(), err)
	}
	fields := make([]string, 0, len(body))
	for key := range body {
		fields = append(fields, key)
	}
	sort.Strings(fields)
	if !slices.Equal(fields, []string{"expires_at", "room", "token"}) {
		t.Errorf("the join answered with %v; a ticket is token, room and expiry", fields)
	}
}

// TestJoinRefusesTheLinkBeforeTheInstance pins the ordering that keeps a
// guesser from learning what this instance is running: the link is resolved
// first, so a dead one answers 404 whether or not calls are configured. A 503
// would say something about the instance to somebody owed nothing.
func TestJoinRefusesTheLinkBeforeTheInstance(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	store.liveConferenceByTokenHash = func(context.Context, []byte) (storage.Conference, error) {
		return storage.Conference{}, storage.ErrNotFound
	}

	rec := doConference(t, store, nil, &recordingAudit{},
		request(http.MethodPost, meetPath("/join"), `{"display_name":"A Guest"}`))

	wantError(t, rec, http.StatusNotFound, "conference_not_found")
	if strings.Contains(rec.Body.String(), "calls_unavailable") {
		t.Error("the refusal leaked the instance's call configuration")
	}
}

// TestJoinWithoutAMediaServer is a live link on an install with no media
// plane: the honest 503, to somebody who has already proved they hold a link.
func TestJoinWithoutAMediaServer(t *testing.T) {
	t.Parallel()

	rec := doConference(t, conferenceStore(), nil, &recordingAudit{},
		request(http.MethodPost, meetPath("/join"), `{"display_name":"A Guest"}`))
	wantError(t, rec, http.StatusServiceUnavailable, "calls_unavailable")
}

// TestGuestNameBounds pins the contract's own bounds on a name that ends up in
// a ticket and an audit entry and in no column at all.
func TestGuestNameBounds(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	for name, body := range map[string]string{
		"empty":                   `{"display_name":""}`,
		"whitespace only":         `{"display_name":"   "}`,
		"past the contract's 64":  `{"display_name":"` + strings.Repeat("n", 65) + `"}`,
		"carrying a control rune": `{"display_name":"Sara\u0007Ahmadi"}`,
		"carrying a newline":      `{"display_name":"Sara\nAhmadi"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := doConference(t, conferenceStore(), svc, &recordingAudit{},
				request(http.MethodPost, meetPath("/join"), body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestPreviewIsThin pins what a link-holder may see before joining: a title
// and whether anybody is in there, and deliberately nothing about who made it
// or who is in it.
func TestPreviewIsThin(t *testing.T) {
	t.Parallel()

	svc, lk := mediaServer(t)
	lk.Rooms = []string{"conf-" + testConferenceID}

	rec := doConference(t, conferenceStore(), svc, &recordingAudit{},
		request(http.MethodGet, meetPath(""), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var preview api.ConferencePreview
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("body %q is not a ConferencePreview: %v", rec.Body.String(), err)
	}
	if preview.Title != "Weekly sync" || !preview.Active {
		t.Errorf("preview = %+v, want the title and a live meeting", preview)
	}
	if body := rec.Body.String(); strings.Contains(body, "created_by") ||
		strings.Contains(body, testConferenceID) || strings.Contains(body, "member") {
		t.Errorf("the preview leaked more than a title and a state: %s", body)
	}
}

// TestRevokeClosesTheRoomThenKillsTheLink is the whole of ADR 005's
// revocation: a flag with the meeting still running is not a revocation.
func TestRevokeClosesTheRoomThenKillsTheLink(t *testing.T) {
	t.Parallel()

	revoked := make(chan uuid.UUID, 1)
	store := adminStore()
	store.conferenceByID = func(context.Context, uuid.UUID) (storage.Conference, error) {
		return liveConference(), nil
	}
	store.revokeConference = func(_ context.Context, id uuid.UUID) error {
		revoked <- id
		return nil
	}

	svc, lk := mediaServer(t)
	audit := &recordingAudit{}
	rec := doConference(t, store, svc, audit,
		adminAPI(http.MethodDelete, "/api/v1/conferences/"+testConferenceID, ""))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if room := lk.AwaitDeletion(t); room != "conf-"+testConferenceID {
		t.Errorf("closed %q, want the conference's room", room)
	}
	select {
	case id := <-revoked:
		if id != conferenceUUID() {
			t.Errorf("revoked %s, want the named conference", id)
		}
	default:
		t.Error("the room was closed but the link was left live")
	}
	if got := audit.actions(); !equalStrings(got, []string{"conference.revoked"}) {
		t.Errorf("recorded %v, want the revocation", got)
	}
}

// TestRevocationOutlivesTheTicketsAlreadyOut is the security review's finding
// at the endpoint that has to honour it.
//
// The old test asserted DeleteRoom was called, which a guest who reconnects
// and rebuilds the room satisfies just as well. This asserts the room is
// still gone afterwards: the guest's pre-revocation ticket names nothing, and
// with `auto_create: false` it cannot make the room again, so "the link is
// dead and the room is closed" stays true rather than being true for an
// instant.
func TestRevocationOutlivesTheTicketsAlreadyOut(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.liveConferenceByTokenHash = func(context.Context, []byte) (storage.Conference, error) {
		return liveConference(), nil
	}
	store.conferenceByID = func(context.Context, uuid.UUID) (storage.Conference, error) {
		return liveConference(), nil
	}
	store.revokeConference = func(context.Context, uuid.UUID) error { return nil }

	svc, lk := mediaServer(t)
	room := "conf-" + testConferenceID
	handler := httpserver.Handler(store,
		httpserver.WithCalls(svc), httpserver.WithAudit(&recordingAudit{}))

	// A guest joins on the link, which is what brings the room into being.
	joined := doHandler(t, handler, request(http.MethodPost, meetPath("/join"), `{"display_name":"A Guest"}`))
	if joined.Code != http.StatusCreated {
		t.Fatalf("join answered %d, want 201 (body %s)", joined.Code, joined.Body.String())
	}
	if !lk.RoomLives(room) {
		t.Fatal("the guest's room was never created")
	}

	revoked := doHandler(t, handler,
		adminAPI(http.MethodDelete, "/api/v1/conferences/"+testConferenceID, ""))
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke answered %d, want 204 (body %s)", revoked.Code, revoked.Body.String())
	}

	if lk.RoomLives(room) {
		t.Error("the room survived the revocation; the guest reconnects and the meeting continues")
	}
}

// TestRevokeReportsAFailedClose is the failure mode the ordering exists for. A
// media server that will not close the room must not produce a 204: that would
// claim a meeting ended while it is still running. The link stays live so the
// caller's retry can do both halves.
func TestRevokeReportsAFailedClose(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.conferenceByID = func(context.Context, uuid.UUID) (storage.Conference, error) {
		return liveConference(), nil
	}
	store.revokeConference = func(context.Context, uuid.UUID) error {
		t.Error("the link was killed although the meeting could not be ended")
		return nil
	}

	svc, lk := mediaServer(t)
	lk.DeleteFails = true

	rec := doConference(t, store, svc, &recordingAudit{},
		adminAPI(http.MethodDelete, "/api/v1/conferences/"+testConferenceID, ""))
	wantError(t, rec, http.StatusInternalServerError, "internal_error")
}

// TestRevokeAuthority is ADR 005's revocation authority at the HTTP edge: its
// owner or an administrator, and the same 404 for everybody else that a
// conference which does not exist gets.
//
// The ownerless case is the one migration 0016 exists for — created_by is SET
// NULL, so a conference outlives the account that made it, and an
// administrator must still be able to reach it.
func TestRevokeAuthority(t *testing.T) {
	t.Parallel()

	owner := fixtureUser()
	stranger := fixtureUser()
	stranger.ID = uuid.MustParse("77777777-7777-7777-7777-777777777777")
	admin := fixtureUser()
	admin.ID = stranger.ID
	admin.IsAdmin = true

	ownerless := liveConference()
	ownerless.CreatedBy = nil

	tests := map[string]struct {
		user       storage.User
		conference storage.Conference
		wantStatus int
	}{
		"the owner may":                       {owner, liveConference(), http.StatusNoContent},
		"an admin may revoke another's":       {admin, liveConference(), http.StatusNoContent},
		"an admin may revoke an ownerless":    {admin, ownerless, http.StatusNoContent},
		"a stranger gets the 404":             {stranger, liveConference(), http.StatusNotFound},
		"the ownerless one is not everyone's": {stranger, ownerless, http.StatusNotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := authedStore(tc.user)
			store.conferenceByID = func(context.Context, uuid.UUID) (storage.Conference, error) {
				return tc.conference, nil
			}
			store.revokeConference = func(context.Context, uuid.UUID) error {
				if tc.wantStatus != http.StatusNoContent {
					t.Error("a caller who may not revoke reached the store")
				}
				return nil
			}
			svc, lk := mediaServer(t)

			rec := doConference(t, store, svc, &recordingAudit{},
				adminAPI(http.MethodDelete, "/api/v1/conferences/"+testConferenceID, ""))

			if rec.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusNotFound {
				wantError(t, rec, http.StatusNotFound, "conference_not_found")
				// A refusal must not end somebody else's meeting either.
				lk.NoDeletion(t)
			}
		})
	}
}

// TestRevokeUnknownConference pins the other half of the uniform refusal: an
// id naming nothing and an id naming somebody else's are the same answer, so
// neither confirms that a conference exists.
func TestRevokeUnknownConference(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.conferenceByID = func(context.Context, uuid.UUID) (storage.Conference, error) {
		return storage.Conference{}, storage.ErrNotFound
	}
	svc, lk := mediaServer(t)

	rec := doConference(t, store, svc, &recordingAudit{},
		adminAPI(http.MethodDelete, "/api/v1/conferences/"+testConferenceID, ""))
	wantError(t, rec, http.StatusNotFound, "conference_not_found")
	lk.NoDeletion(t)
}

// TestCreateConferenceShowsTheLinkOnce pins the mint: the link is in the
// response and nowhere else, only its digest reaches the store, and the token
// rides in the fragment so it cannot land in an access log on its way to the
// join screen.
func TestCreateConferenceShowsTheLinkOnce(t *testing.T) {
	t.Parallel()

	var storedHash []byte
	var storedTitle string
	var storedExpiry *time.Time
	store := adminStore()
	store.createConference = func(_ context.Context, createdBy uuid.UUID, tokenHash []byte,
		title string, expiresAt *time.Time,
	) (storage.Conference, error) {
		storedHash, storedTitle, storedExpiry = tokenHash, title, expiresAt
		if createdBy != adminID {
			t.Errorf("created_by = %s, want the acting caller", createdBy)
		}
		return liveConference(), nil
	}

	svc, _ := mediaServer(t)
	audit := &recordingAudit{}
	rec := doConference(t, store, svc, audit, adminAPI(http.MethodPost,
		"/api/v1/conferences", `{"title":"  Weekly sync  "}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var created api.CreatedConference
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("body %q is not a CreatedConference: %v", rec.Body.String(), err)
	}

	// The link points at the webapp's guest page, which serves /meet/ and
	// everything below it (webapp.go).
	rawToken, found := strings.CutPrefix(created.Url, "/meet/")
	if !found {
		t.Fatalf("url = %q, want the guest page with the token as a path segment", created.Url)
	}
	if len(rawToken) != session.TokenLen {
		t.Errorf("the link carries a %d-character token, want %d", len(rawToken), session.TokenLen)
	}
	// Only the digest is stored, so a stolen database yields nothing that can
	// be presented — the invitation posture, applied to a wider door.
	if string(storedHash) != string(session.HashToken(rawToken)) {
		t.Error("the stored value is not the digest of the link that was handed back")
	}
	if storedTitle != "Weekly sync" {
		t.Errorf("stored title = %q, want it trimmed", storedTitle)
	}
	if storedExpiry != nil {
		t.Errorf("stored expiry = %v, want a link that does not expire by default", storedExpiry)
	}
	// Nobody can be in a room that did not exist a moment ago.
	if created.Conference.Active {
		t.Error("a brand-new conference reports a live meeting")
	}
	if got := audit.actions(); !equalStrings(got, []string{"conference.created"}) {
		t.Errorf("recorded %v, want the creation", got)
	}
}

// TestCreateConferenceValidation pins the bounds, including the expiry that
// would mint a link nobody could ever use.
func TestCreateConferenceValidation(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	for name, body := range map[string]string{
		"a title past 120":      `{"title":"` + strings.Repeat("t", 121) + `"}`,
		"a title with a NUL":    `{"title":"sync\u0000"}`,
		"an expiry in the past": `{"expires_at":"` + past + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := adminStore()
			store.createConference = func(context.Context, uuid.UUID, []byte, string, *time.Time) (storage.Conference, error) {
				t.Error("an invalid request reached the store")
				return storage.Conference{}, nil
			}
			rec := doConference(t, store, svc, &recordingAudit{},
				adminAPI(http.MethodPost, "/api/v1/conferences", body))
			wantError(t, rec, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestCreateConferenceWithoutAMediaServer: a link to a room this instance
// cannot open is worse than a refusal.
func TestCreateConferenceWithoutAMediaServer(t *testing.T) {
	t.Parallel()

	store := adminStore()
	store.createConference = func(context.Context, uuid.UUID, []byte, string, *time.Time) (storage.Conference, error) {
		t.Error("a conference was created on an instance with no media plane")
		return storage.Conference{}, nil
	}
	rec := doConference(t, store, nil, &recordingAudit{},
		adminAPI(http.MethodPost, "/api/v1/conferences", `{}`))
	wantError(t, rec, http.StatusServiceUnavailable, "calls_unavailable")
}

// TestListConferencesScope pins the one thing that separates the two callers
// of this endpoint: an administrator sees every conference on the instance,
// because they must be able to find what they may revoke, and everybody else
// sees their own.
func TestListConferencesScope(t *testing.T) {
	t.Parallel()

	svc, lk := mediaServer(t)
	lk.Rooms = []string{"conf-" + testConferenceID}

	for name, tc := range map[string]struct {
		user    storage.User
		wantAll bool
	}{
		"a member sees their own": {fixtureUser(), false},
		"an admin sees them all":  {adminUserFixture(), true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := authedStore(tc.user)
			store.listConferences = func(_ context.Context, ownerID uuid.UUID, all bool) ([]storage.Conference, error) {
				if all != tc.wantAll {
					t.Errorf("asked the store for all=%v, want %v", all, tc.wantAll)
				}
				if ownerID != tc.user.ID {
					t.Errorf("asked for %s's conferences, want the caller's", ownerID)
				}
				return []storage.Conference{liveConference()}, nil
			}

			rec := doConference(t, store, svc, &recordingAudit{},
				request(http.MethodGet, "/api/v1/conferences", "", withSessionCookie("tok")))
			if rec.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}

			var page api.ConferencePage
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatalf("body %q is not a ConferencePage: %v", rec.Body.String(), err)
			}
			if len(page.Conferences) != 1 || !page.Conferences[0].Active {
				t.Fatalf("conferences = %+v, want one with its live state read", page.Conferences)
			}
			// The link is not among the fields anywhere but the creation
			// response.
			if strings.Contains(rec.Body.String(), "url") {
				t.Errorf("the list carries a link: %s", rec.Body.String())
			}
		})
	}
}

// TestConferenceJoinIsAudited pins the entry an administrator investigating
// abuse works from. It is the instance's only anonymous-access event, which is
// why it is recorded when channel call activity deliberately is not.
func TestConferenceJoinIsAudited(t *testing.T) {
	t.Parallel()

	svc, _ := mediaServer(t)
	audit := &recordingAudit{}
	rec := doConference(t, conferenceStore(), svc, audit,
		request(http.MethodPost, meetPath("/join"), `{"display_name":"  Sara Ahmadi  "}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got status %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	events := audit.snapshot()
	if len(events) != 1 || events[0].Action != "conference.joined" {
		t.Fatalf("recorded %v, want one conference.joined", audit.actions())
	}
	ev := events[0]
	// No actor: the route is public and no session exists. The guest is not
	// an account and must not be recorded as one.
	if ev.ActorID != uuid.Nil {
		t.Errorf("actor = %s, want none: a guest has no account", ev.ActorID)
	}
	if ev.TargetID != conferenceUUID() || ev.TargetLabel != "Weekly sync" {
		t.Errorf("target = %s/%q, want the conference", ev.TargetID, ev.TargetLabel)
	}
	if ev.Detail["display_name"] != "Sara Ahmadi" {
		t.Errorf("detail = %v, want the name the guest chose", ev.Detail)
	}
	if !ev.IP.IsValid() {
		t.Error("no client address recorded; it is most of what an investigation has")
	}
}

// adminUserFixture is the fixture account with the instance role set.
func adminUserFixture() storage.User {
	admin := fixtureUser()
	admin.IsAdmin = true
	return admin
}
