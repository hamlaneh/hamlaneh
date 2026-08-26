package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/session"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

const testIssuer = "https://idp.example.com"

// oidcRawConn opens a raw connection for assertions the storage API has no
// reason to expose (last_login_at, a hard delete).
func oidcRawConn(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("raw connection: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("close raw connection: %v", err)
		}
	})
	return conn
}

// newOidcFixtureUser creates one account for these tests.
func newOidcFixtureUser(ctx context.Context, t *testing.T, store *storage.Store, username string) storage.User {
	t.Helper()
	user, err := store.CreateUser(ctx, storage.NewUser{
		Username:     username,
		PasswordHash: "a stand-in hash",
		Locale:       "en",
	})
	if err != nil {
		t.Fatalf("create fixture user %s: %v", username, err)
	}
	return user
}

// TestOidcIdentityLifecycle walks link, lookup, conflict and unlink against
// the real schema: one identity per account, one account per identity, and
// (issuer, subject) as the whole login key.
func TestOidcIdentityLifecycle(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	alice := newOidcFixtureUser(ctx, t, store, "alice")
	bob := newOidcFixtureUser(ctx, t, store, "bob")

	if alice.SsoLinked {
		t.Error("a fresh account reports sso_linked")
	}

	// An identity linked to nobody resolves to nobody.
	if _, err := store.UserByOidcIdentity(ctx, testIssuer, "sub-alice"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unlinked identity lookup: got %v, want ErrNotFound", err)
	}

	email := "alice@corp.example"
	if err := store.LinkOidcIdentity(ctx, alice.ID, testIssuer, "sub-alice", &email); err != nil {
		t.Fatalf("link: %v", err)
	}

	// The lookup resolves by (issuer, subject) — and by nothing else: the
	// same subject at a different issuer is a different identity.
	got, err := store.UserByOidcIdentity(ctx, testIssuer, "sub-alice")
	if err != nil {
		t.Fatalf("lookup after link: %v", err)
	}
	if got.ID != alice.ID {
		t.Fatalf("lookup resolved %s, want %s", got.ID, alice.ID)
	}
	if !got.SsoLinked {
		t.Error("resolved user does not report sso_linked")
	}
	if _, lookupErr := store.UserByOidcIdentity(ctx, "https://other.example.com", "sub-alice"); !errors.Is(lookupErr, storage.ErrNotFound) {
		t.Errorf("same subject at another issuer resolved: got %v, want ErrNotFound", lookupErr)
	}

	// The lookup recorded the use.
	var lastLogin *string
	conn := oidcRawConn(ctx, t, dsn)
	if scanErr := conn.QueryRow(ctx,
		`SELECT last_login_at::text FROM oidc_identities WHERE user_id = $1`, alice.ID,
	).Scan(&lastLogin); scanErr != nil {
		t.Fatalf("read last_login_at: %v", scanErr)
	}
	if lastLogin == nil {
		t.Error("a sign-in lookup left last_login_at null")
	}

	// One identity per account.
	err = store.LinkOidcIdentity(ctx, alice.ID, testIssuer, "sub-alice-2", nil)
	if !errors.Is(err, storage.ErrOidcAccountLinked) {
		t.Errorf("second identity on one account: got %v, want ErrOidcAccountLinked", err)
	}
	// One account per identity.
	err = store.LinkOidcIdentity(ctx, bob.ID, testIssuer, "sub-alice", nil)
	if !errors.Is(err, storage.ErrOidcIdentityTaken) {
		t.Errorf("one identity on two accounts: got %v, want ErrOidcIdentityTaken", err)
	}

	// The flag surfaces through every user read path.
	byID, err := store.UserByID(ctx, alice.ID)
	if err != nil || !byID.SsoLinked {
		t.Errorf("UserByID sso_linked: got (%v, %v), want linked", byID.SsoLinked, err)
	}
	byIdent, err := store.UserByIdentifier(ctx, "alice")
	if err != nil || !byIdent.SsoLinked {
		t.Errorf("UserByIdentifier sso_linked: got (%v, %v), want linked", byIdent.SsoLinked, err)
	}
	_, accessHash := session.NewToken()
	_, refreshHash := session.NewToken()
	if _, sessErr := store.CreateSession(ctx, storage.NewSession{
		UserID: alice.ID,
		SessionTokens: storage.SessionTokens{
			AccessTokenHash: accessHash, RefreshTokenHash: refreshHash,
			AccessTTL: session.AccessTTL, RefreshTTL: session.RefreshTTL,
		},
	}); sessErr != nil {
		t.Fatalf("create session: %v", sessErr)
	}
	_, sessUser, err := store.SessionUserByAccessHash(ctx, accessHash)
	if err != nil || !sessUser.SsoLinked {
		t.Errorf("SessionUserByAccessHash sso_linked: got (%v, %v), want linked", sessUser.SsoLinked, err)
	}

	// Unlink removes it; unlinking nothing is ErrNotFound.
	if unlinkErr := store.UnlinkOidcIdentity(ctx, alice.ID); unlinkErr != nil {
		t.Fatalf("unlink: %v", unlinkErr)
	}
	if unlinkErr := store.UnlinkOidcIdentity(ctx, alice.ID); !errors.Is(unlinkErr, storage.ErrNotFound) {
		t.Errorf("second unlink: got %v, want ErrNotFound", unlinkErr)
	}
	if _, goneErr := store.UserByOidcIdentity(ctx, testIssuer, "sub-alice"); !errors.Is(goneErr, storage.ErrNotFound) {
		t.Errorf("lookup after unlink: got %v, want ErrNotFound", goneErr)
	}
	after, err := store.UserByID(ctx, alice.ID)
	if err != nil || after.SsoLinked {
		t.Errorf("sso_linked after unlink: got (%v, %v), want unlinked", after.SsoLinked, err)
	}
}

// TestOidcIdentityGoneWithUser pins ON DELETE CASCADE: the identity row
// follows its account.
func TestOidcIdentityGoneWithUser(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	user := newOidcFixtureUser(ctx, t, store, "leaver")

	if err := store.LinkOidcIdentity(ctx, user.ID, testIssuer, "sub-leaver", nil); err != nil {
		t.Fatalf("link: %v", err)
	}
	conn := oidcRawConn(ctx, t, dsn)
	if _, err := conn.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := store.UserByOidcIdentity(ctx, testIssuer, "sub-leaver"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("identity outlived its account: got %v, want ErrNotFound", err)
	}
}

// TestOidcLinkRequestSingleUseAndExpiry pins the pending-link row's security
// properties: a consume requires BOTH the state hash and the secret hash, it
// deletes the row (a replay finds nothing), and an already-expired row is
// refused. All are enforced by the one DELETE, so there is no read-then-write
// window.
func TestOidcLinkRequestSingleUseAndExpiry(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	user := newOidcFixtureUser(ctx, t, store, "linkreq")

	_, liveState := session.NewToken()
	_, liveSecret := session.NewToken()

	// Both factors required: the right state with a wrong secret matches no
	// row and leaves the pending link intact for the real completer.
	if err := store.CreateOidcLinkRequest(ctx, liveState, liveSecret, user.ID, time.Hour); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, wrongSecret := session.NewToken()
	if _, err := store.ConsumeOidcLinkRequest(ctx, liveState, wrongSecret); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("right state, wrong secret: got %v, want ErrNotFound", err)
	}

	// Single use: the correct pair returns the account once, then never again.
	got, err := store.ConsumeOidcLinkRequest(ctx, liveState, liveSecret)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got != user.ID {
		t.Fatalf("consume returned %s, want %s", got, user.ID)
	}
	if _, err := store.ConsumeOidcLinkRequest(ctx, liveState, liveSecret); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("second consume: got %v, want ErrNotFound", err)
	}

	// Expiry: a row already past its expiry is never returned.
	_, staleState := session.NewToken()
	_, staleSecret := session.NewToken()
	if err := store.CreateOidcLinkRequest(ctx, staleState, staleSecret, user.ID, -time.Second); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, err := store.ConsumeOidcLinkRequest(ctx, staleState, staleSecret); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired consume: got %v, want ErrNotFound", err)
	}

	// A lookup for a state nobody registered is ErrNotFound, not an error.
	_, unknownState := session.NewToken()
	_, unknownSecret := session.NewToken()
	if _, err := store.ConsumeOidcLinkRequest(ctx, unknownState, unknownSecret); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown consume: got %v, want ErrNotFound", err)
	}
}

// TestOidcLinkRequestGoneWithUser pins ON DELETE CASCADE for the pending
// row, matching the identity table.
func TestOidcLinkRequestGoneWithUser(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()
	user := newOidcFixtureUser(ctx, t, store, "linkreqgone")

	_, state := session.NewToken()
	_, secret := session.NewToken()
	if err := store.CreateOidcLinkRequest(ctx, state, secret, user.ID, time.Hour); err != nil {
		t.Fatalf("create: %v", err)
	}
	conn := oidcRawConn(ctx, t, dsn)
	if _, err := conn.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := store.ConsumeOidcLinkRequest(ctx, state, secret); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("pending link outlived its account: got %v, want ErrNotFound", err)
	}
}
