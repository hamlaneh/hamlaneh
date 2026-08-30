package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// newUser builds a NewUser fixture; the hash is opaque to storage.
func newUser(name string) storage.NewUser {
	email := name + "@example.com"
	return storage.NewUser{
		Username:     name,
		Email:        &email,
		DisplayName:  "Display " + name,
		PasswordHash: "fake-hash-" + name,
		Locale:       "en",
	}
}

func mustCreateUser(ctx context.Context, t *testing.T, store testdb.Store, nu storage.NewUser) storage.User {
	t.Helper()
	u, err := store.CreateUser(ctx, nu)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", nu.Username, err)
	}
	return u
}

func TestUsersIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	t.Run("create and read back", func(t *testing.T) {
		nu := newUser("alice")
		nu.IsAdmin = true
		nu.MustChangePassword = true
		created := mustCreateUser(ctx, t, store, nu)

		if created.ID == uuid.Nil {
			t.Error("created user has nil id")
		}
		if created.Username != "alice" || !created.IsAdmin || !created.MustChangePassword {
			t.Errorf("created user fields wrong: %+v", created)
		}
		if created.Email == nil || *created.Email != "alice@example.com" {
			t.Errorf("email = %v, want alice@example.com", created.Email)
		}
		if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
			t.Error("timestamps not populated")
		}

		byID, err := store.UserByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if byID.Username != "alice" {
			t.Errorf("UserByID returned %q", byID.Username)
		}
	})

	t.Run("identifier lookup is case-insensitive on username and email", func(t *testing.T) {
		mustCreateUser(ctx, t, store, newUser("bob"))

		for _, identifier := range []string{"bob", "BOB", "Bob", "bob@example.com", "BOB@EXAMPLE.COM"} {
			u, err := store.UserByIdentifier(ctx, identifier)
			if err != nil {
				t.Errorf("UserByIdentifier(%q): %v", identifier, err)
				continue
			}
			if u.Username != "bob" {
				t.Errorf("UserByIdentifier(%q) found %q", identifier, u.Username)
			}
		}
	})

	t.Run("unknown identifier is ErrNotFound", func(t *testing.T) {
		_, err := store.UserByIdentifier(ctx, "nobody-here")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
		_, err = store.UserByID(ctx, uuid.New())
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("UserByID of random uuid: got %v, want ErrNotFound", err)
		}
	})

	t.Run("duplicate username maps to ErrUsernameTaken even with different case", func(t *testing.T) {
		mustCreateUser(ctx, t, store, newUser("carol"))

		dup := newUser("CAROL")
		otherEmail := "carol2@example.com"
		dup.Email = &otherEmail
		_, err := store.CreateUser(ctx, dup)
		if !errors.Is(err, storage.ErrUsernameTaken) {
			t.Errorf("got %v, want ErrUsernameTaken", err)
		}
	})

	t.Run("duplicate email maps to ErrEmailTaken", func(t *testing.T) {
		mustCreateUser(ctx, t, store, newUser("dave"))

		dup := newUser("dave2")
		sameEmail := "DAVE@example.com" // citext: case-insensitive conflict
		dup.Email = &sameEmail
		_, err := store.CreateUser(ctx, dup)
		if !errors.Is(err, storage.ErrEmailTaken) {
			t.Errorf("got %v, want ErrEmailTaken", err)
		}
	})

	t.Run("nil email does not conflict", func(t *testing.T) {
		for _, name := range []string{"erin", "frank"} {
			nu := newUser(name)
			nu.Email = nil
			mustCreateUser(ctx, t, store, nu)
		}
	})

	t.Run("UpdatePasswordHash swaps the hash and keeps the flag", func(t *testing.T) {
		nu := newUser("grace")
		nu.MustChangePassword = true
		created := mustCreateUser(ctx, t, store, nu)

		if err := store.UpdatePasswordHash(ctx, created.ID, "rehashed"); err != nil {
			t.Fatalf("UpdatePasswordHash: %v", err)
		}
		got, err := store.UserByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("UserByID: %v", err)
		}
		if got.PasswordHash != "rehashed" {
			t.Errorf("hash = %q, want rehashed", got.PasswordHash)
		}
		if !got.MustChangePassword {
			t.Error("rehash cleared must_change_password; only a real change may do that")
		}
	})

	t.Run("UpdatePasswordHash of unknown user is ErrNotFound", func(t *testing.T) {
		err := store.UpdatePasswordHash(ctx, uuid.New(), "x")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("UpdateUserProfile changes only the fields the patch names", func(t *testing.T) {
		created := mustCreateUser(ctx, t, store, newUser("ivan"))
		if created.Locale != "en" {
			t.Fatalf("fixture locale = %q, want en", created.Locale)
		}

		// A language change: the display name must survive it untouched.
		locale := "fa"
		updated, err := store.UpdateUserProfile(ctx, created.ID,
			storage.UserProfileUpdate{Locale: &locale})
		if err != nil {
			t.Fatalf("UpdateUserProfile(locale): %v", err)
		}
		if updated.Locale != "fa" {
			t.Errorf("locale = %q, want fa", updated.Locale)
		}
		if updated.DisplayName != created.DisplayName {
			t.Errorf("display_name = %q, want the untouched %q", updated.DisplayName, created.DisplayName)
		}

		// And the mirror image: a rename must not send the reader back to
		// English.
		name := "ایوان"
		updated, err = store.UpdateUserProfile(ctx, created.ID,
			storage.UserProfileUpdate{DisplayName: &name})
		if err != nil {
			t.Fatalf("UpdateUserProfile(display_name): %v", err)
		}
		if updated.DisplayName != name {
			t.Errorf("display_name = %q, want %q", updated.DisplayName, name)
		}
		if updated.Locale != "fa" {
			t.Errorf("locale = %q, want the untouched fa", updated.Locale)
		}

		// Nothing else moved: this patch owns two columns and no more.
		if updated.Username != created.Username || updated.IsAdmin != created.IsAdmin ||
			updated.MustChangePassword != created.MustChangePassword ||
			updated.PasswordHash != created.PasswordHash {
			t.Errorf("patch reached beyond its two columns: %+v", updated)
		}
	})

	t.Run("UpdateUserProfile of an unknown user is ErrNotFound", func(t *testing.T) {
		locale := "fa"
		_, err := store.UpdateUserProfile(ctx, uuid.New(), storage.UserProfileUpdate{Locale: &locale})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("CountUsers counts", func(t *testing.T) {
		before, err := store.CountUsers(ctx)
		if err != nil {
			t.Fatalf("CountUsers: %v", err)
		}
		mustCreateUser(ctx, t, store, newUser("heidi"))
		after, err := store.CountUsers(ctx)
		if err != nil {
			t.Fatalf("CountUsers: %v", err)
		}
		if after != before+1 {
			t.Errorf("count went %d -> %d, want +1", before, after)
		}
	})
}

// TestListUsersKeysetIntegration walks the whole set through the cursor and
// proves every user appears exactly once, including users sharing a
// created_at timestamp (id is the tiebreak).
func TestListUsersKeysetIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()

	const total = 7
	for i := range total {
		mustCreateUser(ctx, t, store, newUser(fmt.Sprintf("user%02d", i)))
	}

	seen := map[string]int{}
	var after *storage.UserCursor
	pages := 0
	for {
		page, err := store.ListUsers(ctx, storage.ListUsersParams{After: after, Limit: 3})
		if err != nil {
			t.Fatalf("ListUsers page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		pages++
		if pages > total {
			t.Fatal("pagination never terminates")
		}
		for _, u := range page {
			seen[u.Username]++
		}
		last := page[len(page)-1]
		after = &storage.UserCursor{CreatedAt: last.CreatedAt, ID: last.ID}

		// Stable order inside a page.
		for i := 1; i < len(page); i++ {
			prev, cur := page[i-1], page[i]
			if cur.CreatedAt.Before(prev.CreatedAt) {
				t.Errorf("page out of order: %s before %s", prev.Username, cur.Username)
			}
			if cur.CreatedAt.Equal(prev.CreatedAt) && cur.ID.String() <= prev.ID.String() {
				t.Errorf("tie not broken by id: %s / %s", prev.Username, cur.Username)
			}
		}
	}

	if len(seen) != total {
		t.Errorf("walked %d distinct users, want %d", len(seen), total)
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("user %s appeared %d times, want exactly once", name, count)
		}
	}
}

// TestListUsersEqualCreatedAtIntegration proves the (created_at, id) row
// comparison really breaks created_at ties by id: five rows share one
// explicit created_at (inserted via raw SQL, bypassing DEFAULT now()) and a
// small-page walk must return every row exactly once in a stable order,
// with cursors landing mid-tie.
func TestListUsersEqualCreatedAtIntegration(t *testing.T) {
	t.Parallel()

	store, dsn := testdb.New(t)
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect for raw inserts: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(context.Background()); closeErr != nil {
			t.Errorf("close raw connection: %v", closeErr)
		}
	}()
	// The raw connection needs the citext type registered to bind the
	// username parameter, exactly like the pool's connections.
	if err := storage.RegisterCitext(ctx, conn); err != nil {
		t.Fatalf("register citext on raw connection: %v", err)
	}

	shared := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insert := func(name string, createdAt time.Time) {
		t.Helper()
		if _, execErr := conn.Exec(ctx,
			`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ($1, $2, $3, $3)`,
			name, "fake-hash-"+name, createdAt,
		); execErr != nil {
			t.Fatalf("insert %s: %v", name, execErr)
		}
	}

	const tied = 5
	insert("earlier", shared.Add(-time.Hour))
	for i := range tied {
		insert(fmt.Sprintf("tied%d", i), shared)
	}
	insert("later", shared.Add(time.Hour))
	const total = tied + 2

	// Walk with a page size that forces cursors inside the tied run.
	var walked []storage.User
	var after *storage.UserCursor
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination never terminates")
		}
		page, listErr := store.ListUsers(ctx, storage.ListUsersParams{After: after, Limit: 2})
		if listErr != nil {
			t.Fatalf("ListUsers page %d: %v", pages, listErr)
		}
		if len(page) == 0 {
			break
		}
		walked = append(walked, page...)
		last := page[len(page)-1]
		after = &storage.UserCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}

	seen := map[string]int{}
	for _, u := range walked {
		seen[u.Username]++
	}
	if len(seen) != total {
		t.Errorf("walked %d distinct users, want %d", len(seen), total)
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("user %s appeared %d times, want exactly once", name, count)
		}
	}

	// Stable (created_at, id) order across the whole walk, including page
	// boundaries inside the tie.
	for i := 1; i < len(walked); i++ {
		prev, cur := walked[i-1], walked[i]
		if cur.CreatedAt.Before(prev.CreatedAt) {
			t.Errorf("walk out of order: %s (%v) before %s (%v)",
				prev.Username, prev.CreatedAt, cur.Username, cur.CreatedAt)
		}
		if cur.CreatedAt.Equal(prev.CreatedAt) && cur.ID.String() <= prev.ID.String() {
			t.Errorf("tie not broken by id: %s (%s) then %s (%s)",
				prev.Username, prev.ID, cur.Username, cur.ID)
		}
	}
}
