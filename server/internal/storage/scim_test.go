package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// The SCIM storage surface against real PostgreSQL. The facts here are the
// database's own — which unique constraint a conflict names, what a NULL
// password_hash reads back as, and what an UPDATE that does not mention
// is_admin leaves it as — so mocks could only restate the assumptions being
// tested.

// scimFixture is a store with one administrator to hang credentials off.
func scimFixture(t *testing.T) (testdb.Store, storage.User) {
	t.Helper()

	store, _ := testdb.New(t)
	admin, err := store.CreateUser(context.Background(), storage.NewUser{
		Username:     "admin",
		PasswordHash: "$argon2id$fixture",
		Locale:       "en",
		IsAdmin:      true,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return store, admin
}

func TestScimTokenLifecycle(t *testing.T) {
	t.Parallel()

	store, admin := scimFixture(t)
	ctx := context.Background()
	hash := []byte("0123456789abcdef0123456789abcdef")

	tok, err := store.CreateScimToken(ctx, admin.ID, hash, "okta production")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tok.Note != "okta production" || tok.CreatedBy.Username != "admin" {
		t.Errorf("stored token = %+v", tok)
	}
	if tok.LastUsedAt != nil {
		t.Error("a freshly minted token already claims to have been used")
	}

	// Resolving it records the use, which is what tells a configured
	// credential from a forgotten one.
	id, err := store.ScimTokenByHash(ctx, hash)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != tok.ID {
		t.Errorf("resolved %s, want %s", id, tok.ID)
	}
	tokens, err := store.ListScimTokens(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tokens) != 1 || tokens[0].LastUsedAt == nil {
		t.Fatalf("list after use = %+v", tokens)
	}

	// The touch is throttled: a provider's full sync is hundreds of reads,
	// and writing the same row on every one of them would cost a dead tuple
	// apiece for a value nobody reads at that resolution. The id still comes
	// back on the requests that skip the write, which is the part a
	// conditional UPDATE would have got wrong.
	firstUse := *tokens[0].LastUsedAt
	for range 5 {
		if again, resolveErr := store.ScimTokenByHash(ctx, hash); resolveErr != nil || again != tok.ID {
			t.Fatalf("repeat resolve = %s, %v; want the same id and no error", again, resolveErr)
		}
	}
	tokens, err = store.ListScimTokens(ctx)
	if err != nil {
		t.Fatalf("list after repeats: %v", err)
	}
	if !tokens[0].LastUsedAt.Equal(firstUse) {
		t.Errorf("last_used_at moved from %s to %s within the throttle window",
			firstUse, *tokens[0].LastUsedAt)
	}

	if revokeErr := store.RevokeScimToken(ctx, tok.ID); revokeErr != nil {
		t.Fatalf("revoke: %v", revokeErr)
	}
	// A revoked token resolves to nothing and leaves the list: both are what
	// "takes effect immediately" means.
	if _, resolveErr := store.ScimTokenByHash(ctx, hash); !errors.Is(resolveErr, storage.ErrNotFound) {
		t.Errorf("a revoked token resolved: %v", resolveErr)
	}
	tokens, err = store.ListScimTokens(ctx)
	if err != nil {
		t.Fatalf("list after revoke: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("a revoked token is still listed: %+v", tokens)
	}
	// Revoking again names no live credential, and says so.
	if again := store.RevokeScimToken(ctx, tok.ID); !errors.Is(again, storage.ErrNotFound) {
		t.Errorf("second revoke = %v, want ErrNotFound", again)
	}
	if unknown := store.RevokeScimToken(ctx, uuid.New()); !errors.Is(unknown, storage.ErrNotFound) {
		t.Errorf("revoking an unknown id = %v, want ErrNotFound", unknown)
	}
}

// TestCreateScimUserHasNoPasswordCredential is the migration 0014 property
// read back through the canonical projection: the column is NULL and every
// existing reader sees "".
func TestCreateScimUserHasNoPasswordCredential(t *testing.T) {
	t.Parallel()

	store, _ := scimFixture(t)
	ctx := context.Background()
	email := "amir@example.com"

	created, err := store.CreateScimUser(ctx, storage.NewScimUser{
		Username:     "amir",
		ScimUserName: "amir@example.com",
		ExternalID:   ptr("00u1abc"),
		Email:        &email,
		DisplayName:  "Amir",
		Locale:       "en",
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.PasswordHash != "" {
		t.Errorf("password hash = %q, want empty", created.PasswordHash)
	}
	if created.IsAdmin {
		t.Error("a provisioned account was created as an administrator")
	}

	// Every other read path goes through the same projection.
	readBack, err := store.UserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if readBack.PasswordHash != "" {
		t.Errorf("password hash on read = %q, want empty", readBack.PasswordHash)
	}
	if readBack.ScimUserName == nil || *readBack.ScimUserName != "amir@example.com" {
		t.Errorf("scim_user_name = %v", readBack.ScimUserName)
	}
	byIdentifier, err := store.UserByIdentifier(ctx, "amir")
	if err != nil {
		t.Fatalf("read by identifier: %v", err)
	}
	if byIdentifier.PasswordHash != "" {
		t.Errorf("password hash by identifier = %q, want empty", byIdentifier.PasswordHash)
	}
}

// TestCreateScimUserConflicts pins which sentinel each unique column
// produces, because the caller treats them differently: a local username
// clash is retried with a suffix, and the other two are the 409 a provider
// must see.
func TestCreateScimUserConflicts(t *testing.T) {
	t.Parallel()

	store, _ := scimFixture(t)
	ctx := context.Background()
	email := "taken@example.com"

	base := storage.NewScimUser{
		Username:     "taken",
		ScimUserName: "taken@example.com",
		ExternalID:   ptr("ext-taken"),
		Email:        &email,
		Locale:       "en",
		IsActive:     true,
	}
	if _, err := store.CreateScimUser(ctx, base); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(nu *storage.NewScimUser)
		wantErr error
	}{
		{"local username", func(nu *storage.NewScimUser) {
			nu.ScimUserName = "other@example.com"
			nu.ExternalID = ptr("ext-other")
			nu.Email = nil
		}, storage.ErrUsernameTaken},
		{"provider userName", func(nu *storage.NewScimUser) {
			nu.Username = "other"
			nu.ExternalID = ptr("ext-other")
			nu.Email = nil
		}, storage.ErrScimIdentifierTaken},
		{"externalId", func(nu *storage.NewScimUser) {
			nu.Username = "other"
			nu.ScimUserName = "other@example.com"
			nu.Email = nil
		}, storage.ErrScimIdentifierTaken},
		{"email", func(nu *storage.NewScimUser) {
			nu.Username = "other"
			nu.ScimUserName = "other@example.com"
			nu.ExternalID = ptr("ext-other")
		}, storage.ErrEmailTaken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nu := base
			tt.mutate(&nu)
			_, err := store.CreateScimUser(ctx, nu)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("create = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestReplaceScimUserCannotReachTheRoleOrTheFlag is the statement-level
// version of the property the whole surface rests on. The UPDATE does not
// mention is_admin or is_active, so no caller of it can change either — no
// matter what a request body said.
func TestReplaceScimUserCannotReachTheRoleOrTheFlag(t *testing.T) {
	t.Parallel()

	store, admin := scimFixture(t)
	ctx := context.Background()

	// Both directions have to be pinned, and a single fixture can only pin
	// one: an admin proves the write cannot demote, and an ordinary account
	// proves it cannot promote. The second is the one that matters.
	member, err := store.CreateUser(ctx, storage.NewUser{
		Username: "member", PasswordHash: "$argon2id$fixture", Locale: "en",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	promoted, err := store.ReplaceScimUser(ctx, member.ID, storage.ScimUserAttributes{
		ScimUserName: "member@example.com",
		DisplayName:  "Renamed",
	})
	if err != nil {
		t.Fatalf("replace member: %v", err)
	}
	if promoted.IsAdmin {
		t.Error("a SCIM attribute write promoted an ordinary account")
	}

	updated, err := store.ReplaceScimUser(ctx, admin.ID, storage.ScimUserAttributes{
		ScimUserName: "admin@example.com",
		ExternalID:   ptr("00uADMIN"),
		DisplayName:  "Renamed By The Directory",
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !updated.IsAdmin {
		t.Error("a SCIM attribute write demoted an administrator")
	}
	if !updated.IsActive {
		t.Error("a SCIM attribute write deactivated an account")
	}
	if updated.DisplayName != "Renamed By The Directory" {
		t.Errorf("display name = %q", updated.DisplayName)
	}
	// And the password credential the account already had is untouched: the
	// statement does not mention that column either.
	if updated.PasswordHash != "$argon2id$fixture" {
		t.Errorf("password hash = %q, want the one the account already had", updated.PasswordHash)
	}

	if _, err := store.ReplaceScimUser(ctx, uuid.New(), storage.ScimUserAttributes{ScimUserName: "nobody"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("replacing an unknown account = %v, want ErrNotFound", err)
	}
}

func TestListScimUsers(t *testing.T) {
	t.Parallel()

	store, _ := scimFixture(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		email := name + "@example.com"
		if _, err := store.CreateScimUser(ctx, storage.NewScimUser{
			Username:     name + "person",
			ScimUserName: name + "@directory.example",
			ExternalID:   ptr("ext-" + name),
			Email:        &email,
			Locale:       "en",
			IsActive:     true,
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	t.Run("no filter walks everyone", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{}, 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 4 || len(users) != 4 {
			t.Errorf("got %d of %d, want 4 of 4", len(users), total)
		}
	})

	t.Run("userName matches the provider value", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{UserName: ptr("b@directory.example")}, 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].Username != "bperson" {
			t.Errorf("got %+v (total %d)", users, total)
		}
	})

	t.Run("userName also matches the email, which is how adoption works", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{UserName: ptr("b@example.com")}, 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].Username != "bperson" {
			t.Errorf("got %+v (total %d)", users, total)
		}
	})

	t.Run("the match is case-insensitive because the columns are citext", func(t *testing.T) {
		_, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{UserName: ptr("B@DIRECTORY.EXAMPLE")}, 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 {
			t.Errorf("total = %d, want 1", total)
		}
	})

	t.Run("externalId matches only itself", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{ExternalID: ptr("ext-c")}, 0, 100)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 1 || len(users) != 1 || users[0].Username != "cperson" {
			t.Errorf("got %+v (total %d)", users, total)
		}
	})

	t.Run("a page is a window on a stable total", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{}, 1, 2)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 4 {
			t.Errorf("total = %d, want 4", total)
		}
		if len(users) != 2 {
			t.Errorf("page has %d rows, want 2", len(users))
		}
	})

	t.Run("a zero limit answers the total and no rows", func(t *testing.T) {
		users, total, err := store.ListScimUsers(ctx, storage.ScimUserFilter{}, 0, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 4 || len(users) != 0 {
			t.Errorf("got %d rows of %d, want 0 of 4", len(users), total)
		}
	})
}
