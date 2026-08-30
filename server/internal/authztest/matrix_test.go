package authztest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hamlaneh/hamlaneh/server/internal/audit"
	"github.com/hamlaneh/hamlaneh/server/internal/blobstore"
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

// relation is what one principal is to the fixture channel and to the
// instance. It is the whole principal model: the provisioner realizes these
// facts against the entry's channel kind, and nothing else distinguishes
// one column from another.
type relation struct {
	session     bool
	admin       bool
	mustChange  bool
	totpPending bool
	member      bool
	creator     bool
	author      bool
}

// relations maps every matrix column to the world its cell is provisioned
// in. The channel columns differ only here — which is what makes them one
// mechanism instead of a case per column.
var relations = map[Principal]relation{
	Anonymous:                   {},
	Member:                      {session: true},
	MemberMustChange:            {session: true, mustChange: true},
	MemberTotpPending:           {session: true, totpPending: true},
	MemberMustChangeTotpPending: {session: true, mustChange: true, totpPending: true},
	Admin:                       {session: true, admin: true},
	ChannelNonMember:            {session: true},
	ChannelMember:               {session: true, member: true},
	ChannelOwner:                {session: true, member: true, creator: true},
	AdminNonMember:              {session: true, admin: true},
	AdminMember:                 {session: true, admin: true, member: true},
	MemberAuthor:                {session: true, member: true, author: true},
	// creator carries the same meaning it does for a channel — the acting
	// user made the fixture resource — so a conference needs no field of its
	// own. Every other principal's conference is somebody else's.
	ConferenceOwner: {session: true, creator: true},
}

// cell is one provisioned matrix cell: the acting user's fixture, its live
// session when the principal has one, and the sequence number that keeps the
// cell's identifiers — and its client address — its own.
type cell struct {
	fx           Fixture
	seq          int64
	accessToken  string
	refreshToken string
}

// provisionCell builds one cell's whole world directly through storage — no
// login round-trips, so the login rate limiter only ever sees the login row's
// own cells.
//
// Everything is fresh: the acting user, an outsider, and for a channel-scoped
// entry a channel of that entry's kind with its own members and message.
// Cells mutate — a removal, an edit, a send — and sharing any of this across
// cells would make a failure depend on the order they ran in.
func provisionCell(ctx context.Context, t *testing.T, store testdb.Store, pool *pgxpool.Pool, entry Entry, principal Principal) cell {
	t.Helper()

	rel, known := relations[principal]
	if !known {
		t.Fatalf("principal %s has no relation; declare one in relations", principal)
	}

	seq := cellSeq.Add(1)
	fx := Fixture{
		Username: fmt.Sprintf("mx%d", seq),
		Password: fixturePassword,
		Unique:   strconv.FormatInt(seq, 10),
	}

	actor := newFixtureUser(ctx, t, store, fx.Username, rel.admin, rel.mustChange)
	// Somebody who belongs to no channel: the target of an invite, the peer
	// of a newly opened DM. Every cell provisions its own rather than borrow
	// a neighbour's, which is the coupling this harness exists without.
	outsider := newFixtureUser(ctx, t, store, fmt.Sprintf("mx%do", seq), false, false)
	fx.OutsiderUserID = outsider.ID.String()

	c := cell{seq: seq}
	if rel.session {
		c.accessToken, c.refreshToken = newFixtureSession(ctx, t, store, actor.ID)
		if rel.totpPending {
			flagSessionTotpPending(ctx, t, pool, actor.ID)
		}
	}

	if entry.ChannelScoped() {
		channelID, messageID, otherMemberID := provisionChannel(ctx, t, store, entry.Kind, rel, seq, actor)
		fx.ChannelID = channelID.String()
		fx.MessageID = messageID.String()
		fx.MemberUserID = otherMemberID.String()
	}
	if entry.ConferenceScoped() {
		fx.ConferenceID = provisionConference(ctx, t, store, rel, seq, actor).String()
	}

	c.fx = fx
	return c
}

// provisionConference builds the cell's conference, made by the acting user
// when the principal owns it and by a fresh stranger otherwise.
//
// The stranger is the point: without it every cell would act on its own
// conference and the plain Member row would be asserting the owner's answer
// while claiming to assert somebody else's. A conference has no membership to
// arrange — that is the whole feature — so who made it is the only fact to
// set up.
func provisionConference(ctx context.Context, t *testing.T, store testdb.Store,
	rel relation, seq int64, actor storage.User,
) uuid.UUID {
	t.Helper()

	creator := actor
	if !rel.creator {
		creator = newFixtureUser(ctx, t, store, fmt.Sprintf("mx%dk", seq), false, false)
	}

	// The digest is per cell, because link_token_hash is UNIQUE and the cells
	// run in parallel against one database.
	_, tokenHash := session.NewToken()
	conf, err := store.CreateConference(ctx, creator.ID, tokenHash,
		fmt.Sprintf("matrix %d", seq), nil)
	if err != nil {
		t.Fatalf("create fixture conference: %v", err)
	}
	return conf.ID
}

// provisionChannel builds the cell's channel: one of the entry's kind,
// arranged so the acting user really is what its principal claims — creator,
// plain member, admin member, author, or stranger.
//
// It returns the channel, the message the message-scoped operations act on,
// and a member of the channel who is not the acting user: the target of a
// removal, and the fixture message's author unless the principal wrote it
// themselves. That last part is what makes the plain member columns genuine
// non-authors instead of accidental ones.
func provisionChannel(ctx context.Context, t *testing.T, store testdb.Store,
	kind storage.ChannelKind, rel relation, seq int64, actor storage.User,
) (channelID, messageID, otherMemberID uuid.UUID) {
	t.Helper()

	creator := actor
	if !rel.creator {
		creator = newFixtureUser(ctx, t, store, fmt.Sprintf("mx%dc", seq), false, false)
	}

	var (
		channel storage.Channel
		other   storage.User
	)
	if kind == storage.ChannelKindDM {
		// A DM's membership is its pair, fixed when it is opened, so the
		// acting user's relation decides which half of the pair it is: the
		// creator, the peer, or neither.
		peer := actor
		if !rel.member || rel.creator {
			peer = newFixtureUser(ctx, t, store, fmt.Sprintf("mx%dp", seq), false, false)
		}
		var err error
		channel, _, err = store.OpenDirectMessage(ctx, creator.ID, peer.ID, false)
		if err != nil {
			t.Fatalf("open fixture dm: %v", err)
		}
		other = creator
		if creator.ID == actor.ID {
			other = peer
		}
	} else {
		var err error
		channel, err = store.CreateChannel(ctx, storage.NewChannel{
			Kind:      kind,
			Slug:      fmt.Sprintf("mx%d", seq),
			CreatedBy: creator.ID,
		})
		if err != nil {
			t.Fatalf("create fixture %s channel: %v", kind, err)
		}
		// A member who is neither the creator nor the acting user: somebody
		// for a removal to remove without emptying the channel, and an author
		// the acting user is not.
		other = newFixtureUser(ctx, t, store, fmt.Sprintf("mx%ds", seq), false, false)
		addFixtureMember(ctx, t, store, channel.ID, other.ID, creator.ID)
		if rel.member && !rel.creator {
			addFixtureMember(ctx, t, store, channel.ID, actor.ID, creator.ID)
		}
	}

	author := other
	if rel.author {
		author = actor
	}
	message, _, err := store.CreateMessage(ctx, storage.NewMessage{
		ChannelID:   channel.ID,
		AuthorID:    author.ID,
		ClientMsgID: uuid.New(),
		Content:     "a fixture message",
	})
	if err != nil {
		t.Fatalf("create fixture message: %v", err)
	}

	return channel.ID, message.ID, other.ID
}

// newFixtureUser creates one account for a cell.
func newFixtureUser(ctx context.Context, t *testing.T, store testdb.Store,
	username string, admin, mustChange bool,
) storage.User {
	t.Helper()

	user, err := store.CreateUser(ctx, storage.NewUser{
		Username:           username,
		PasswordHash:       fixtureHash(),
		Locale:             "en",
		IsAdmin:            admin,
		MustChangePassword: mustChange,
	})
	if err != nil {
		t.Fatalf("create fixture user %s: %v", username, err)
	}
	return user
}

// newFixtureSession mints a live session for the acting user and returns its
// raw cookie values.
func newFixtureSession(ctx context.Context, t *testing.T, store testdb.Store,
	userID uuid.UUID,
) (accessToken, refreshToken string) {
	t.Helper()

	accessRaw, accessHash := session.NewToken()
	refreshRaw, refreshHash := session.NewToken()
	if _, err := store.CreateSession(ctx, storage.NewSession{
		UserID: userID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash:  accessHash,
			RefreshTokenHash: refreshHash,
			AccessTTL:        session.AccessTTL,
			RefreshTTL:       session.RefreshTTL,
		},
	}); err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	return accessRaw, refreshRaw
}

// flagSessionTotpPending puts the cell's session in the state a sign-in
// under an enforced two-step policy mints: totp_enrollment_required set.
//
// The row is written directly because the flag's only production writer is
// the mint reading org_settings, and org_settings is one row per database —
// flipping require_totp on for one cell would flag every session the
// parallel cells mint in the window. Direct row state is the same
// through-storage provisioning the rest of this file does; the flag's
// production semantics are pinned by the integration tests in
// internal/httpserver and internal/storage, not here.
func flagSessionTotpPending(ctx context.Context, t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) {
	t.Helper()

	if _, err := pool.Exec(ctx,
		`UPDATE sessions SET totp_enrollment_required = true WHERE user_id = $1`,
		userID,
	); err != nil {
		t.Fatalf("flag fixture session totp-pending: %v", err)
	}
}

// addFixtureMember puts a user in the cell's channel.
func addFixtureMember(ctx context.Context, t *testing.T, store testdb.Store,
	channelID, userID, addedBy uuid.UUID,
) {
	t.Helper()

	if err := store.AddChannelMember(ctx, channelID, userID, addedBy); err != nil {
		t.Fatalf("add fixture member: %v", err)
	}
}

// buildRequest assembles the cell's request: body from the entry's builder,
// cookies for authenticated principals, and the CSRF pair on mutations.
func buildRequest(entry Entry, principal Principal, c cell) *http.Request {
	body := ""
	if entry.Body != nil {
		body = entry.Body(c.fx)
	}
	req := httptest.NewRequest(entry.Method, entry.Target(c.fx), strings.NewReader(body))
	if body != "" {
		contentType := entry.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	// Every cell speaks from its own address. The matrix asserts
	// authorization, not budgets, and hundreds of cells sharing httptest's
	// one default RemoteAddr would let a per-IP rate limit on message send or
	// channel creation turn the grid red for a reason that is not permission.
	// The address is documentation-range and public, so the trusted-proxy
	// path that reads X-Forwarded-For never applies to it.
	req.RemoteAddr = fmt.Sprintf("[2001:db8::%x]:443", c.seq)

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
// real server on a real database and asserts every cell. Every cell gets its
// own users, channel and session, so mutating cells (a logout, a removal, an
// edit) cannot poison others — which is also why the cells can run in
// parallel.
func TestAuthzMatrix(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	// A raw connection beside the store, for the one fixture state no
	// storage API mints on demand: the totp-pending session flag.
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("matrix raw pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// The upload row posts a real file, so the server under the matrix needs
	// somewhere to put it. A per-run temporary directory keeps the grid from
	// touching anything an install would.
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("matrix blob store: %v", err)
	}
	// The audit row reads the log back, and reading it back means verifying
	// it, which needs the key. A throwaway one per run is right here: the
	// matrix asserts who may ask, and this is what makes the question
	// answerable at all.
	chain, err := audit.New([]byte("authz matrix audit chain key, 32+ bytes"))
	if err != nil {
		t.Fatalf("matrix audit chain: %v", err)
	}
	handler := httpserver.Handler(store,
		httpserver.WithUploads(blobs, nil),
		httpserver.WithAuditChain(chain))
	ctx := context.Background()

	entries := Registry()
	cells, wantCells := 0, 0
	for _, entry := range entries {
		// An entry runs the principals it declares, but it may never declare
		// fewer than its shape requires: a channel-scoped row that quietly
		// dropped AdminNonMember would still pass every cell it kept.
		for _, principal := range entry.RequiredPrincipals() {
			if _, ok := entry.Want[principal]; !ok {
				t.Fatalf("%s has no expectation for %s", entryName(entry), principal)
			}
		}
		wantCells += len(entry.Want)

		for _, principal := range entry.Principals() {
			cells++

			t.Run(fmt.Sprintf("%s as %s", entryName(entry), principal), func(t *testing.T) {
				t.Parallel()

				c := provisionCell(ctx, t, store, pool, entry, principal)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, buildRequest(entry, principal, c))

				want := entry.Want[principal]
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

	if cells != wantCells {
		t.Errorf("matrix ran %d cells for %d expectations; a Want key that is not a known "+
			"principal never runs at all", cells, wantCells)
	}
}

// entryName identifies one row in test output: the operation, plus the
// fixture kind that tells two rows of the same operation apart.
func entryName(e Entry) string {
	name := e.Method + " " + e.Path
	if e.ChannelScoped() {
		name += " [" + string(e.Kind) + "]"
	}
	return name
}
