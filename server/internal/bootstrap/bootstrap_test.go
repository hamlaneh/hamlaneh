package bootstrap_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/testdb"
)

// fakeStore is an in-memory bootstrap.Store for unit tests.
type fakeStore struct {
	count   int64
	created []storage.NewUser
}

func (f *fakeStore) CountUsers(context.Context) (int64, error) { return f.count, nil }

func (f *fakeStore) CreateUser(_ context.Context, nu storage.NewUser) (storage.User, error) {
	f.created = append(f.created, nu)
	return storage.User{Username: nu.Username, IsAdmin: nu.IsAdmin, MustChangePassword: nu.MustChangePassword}, nil
}

func TestAdminFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		pw          string
		locale      string
		wantPresent bool
		wantLocale  string
	}{
		{"all set", "admin", "a long password", "fa", true, "fa"},
		{"locale defaults to en", "admin", "a long password", "", true, "en"},
		{"missing password", "admin", "", "", false, "en"},
		{"missing username", "", "a long password", "", false, "en"},
		{"nothing set", "", "", "", false, "en"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(bootstrap.EnvUsername, tt.username)
			t.Setenv(bootstrap.EnvPassword, tt.pw)
			t.Setenv(bootstrap.EnvLocale, tt.locale)

			cfg, present := bootstrap.AdminFromEnv()
			if present != tt.wantPresent {
				t.Errorf("present = %v, want %v", present, tt.wantPresent)
			}
			if cfg.Locale != tt.wantLocale {
				t.Errorf("locale = %q, want %q", cfg.Locale, tt.wantLocale)
			}
		})
	}
}

func TestEnsureAdminValidation(t *testing.T) {
	t.Parallel()

	valid := bootstrap.AdminConfig{Username: "admin", Password: "a long password", Locale: "en"}

	tests := []struct {
		name    string
		mutate  func(*bootstrap.AdminConfig)
		wantErr string
	}{
		{"short username", func(c *bootstrap.AdminConfig) { c.Username = "ab" }, "3 to 32"},
		{"bad username pattern", func(c *bootstrap.AdminConfig) { c.Username = "Admin!" }, "lowercase"},
		{"short password", func(c *bootstrap.AdminConfig) { c.Password = "elevenchars" }, "12 to 1024"},
		{"long password", func(c *bootstrap.AdminConfig) { c.Password = strings.Repeat("a", 1025) }, "12 to 1024"},
		{"bad locale", func(c *bootstrap.AdminConfig) { c.Locale = "de" }, "en, fa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStore{count: 0}
			cfg := valid
			tt.mutate(&cfg)

			err := bootstrap.EnsureAdmin(context.Background(), store, cfg, true)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if len(store.created) != 0 {
				t.Error("invalid config still created a user")
			}
		})
	}
}

func TestEnsureAdminUnit(t *testing.T) {
	t.Parallel()

	t.Run("non-empty table ignores config entirely", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{count: 3}
		// Even an invalid config must not matter once users exist.
		err := bootstrap.EnsureAdmin(context.Background(), store, bootstrap.AdminConfig{Username: "!"}, true)
		if err != nil {
			t.Errorf("EnsureAdmin: %v", err)
		}
		if len(store.created) != 0 {
			t.Error("bootstrap created a user although the table is not empty")
		}
	})

	t.Run("empty table without config only warns", func(t *testing.T) {
		t.Parallel()
		store := &fakeStore{count: 0}
		if err := bootstrap.EnsureAdmin(context.Background(), store, bootstrap.AdminConfig{}, false); err != nil {
			t.Errorf("EnsureAdmin: %v", err)
		}
		if len(store.created) != 0 {
			t.Error("bootstrap created a user without configuration")
		}
	})

	t.Run("username race maps to success", func(t *testing.T) {
		t.Parallel()
		race := &racingStore{}
		cfg := bootstrap.AdminConfig{Username: "admin", Password: "a long password", Locale: "en"}
		if err := bootstrap.EnsureAdmin(context.Background(), race, cfg, true); err != nil {
			t.Errorf("EnsureAdmin during race: %v", err)
		}
	})
}

// racingStore simulates another instance winning the bootstrap race.
type racingStore struct{}

func (racingStore) CountUsers(context.Context) (int64, error) { return 0, nil }
func (racingStore) CreateUser(context.Context, storage.NewUser) (storage.User, error) {
	return storage.User{}, storage.ErrUsernameTaken
}

// TestEnsureAdminIntegration exercises the real flow against PostgreSQL:
// fresh database + config → exactly one admin with the pinned flags; a
// second startup is a no-op; a non-empty table ignores the variables.
func TestEnsureAdminIntegration(t *testing.T) {
	t.Parallel()

	store, _ := testdb.New(t)
	ctx := context.Background()
	cfg := bootstrap.AdminConfig{Username: "firstadmin", Password: "initial admin password", Locale: "fa"}

	if err := bootstrap.EnsureAdmin(ctx, store, cfg, true); err != nil {
		t.Fatalf("EnsureAdmin: %v", err)
	}

	admin, err := store.UserByIdentifier(ctx, "firstadmin")
	if err != nil {
		t.Fatalf("admin not found after bootstrap: %v", err)
	}
	if !admin.IsAdmin {
		t.Error("bootstrap admin is not an admin")
	}
	if !admin.MustChangePassword {
		t.Error("bootstrap admin is not forced to change the password")
	}
	if admin.Locale != "fa" {
		t.Errorf("locale = %q, want fa", admin.Locale)
	}
	if ok, _, verifyErr := password.Verify("initial admin password", admin.PasswordHash); verifyErr != nil || !ok {
		t.Errorf("stored hash does not verify the configured password (ok=%v err=%v)", ok, verifyErr)
	}

	// Second startup: no second user.
	if againErr := bootstrap.EnsureAdmin(ctx, store, cfg, true); againErr != nil {
		t.Fatalf("second EnsureAdmin: %v", againErr)
	}
	count, err := store.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 1 {
		t.Errorf("after two bootstraps %d users exist, want 1", count)
	}

	// Non-empty table: different variables change nothing.
	other := bootstrap.AdminConfig{Username: "secondadmin", Password: "another admin password", Locale: "en"}
	if err := bootstrap.EnsureAdmin(ctx, store, other, true); err != nil {
		t.Fatalf("EnsureAdmin on non-empty table: %v", err)
	}
	if _, err := store.UserByIdentifier(ctx, "secondadmin"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("bootstrap on a non-empty table created a user (err=%v)", err)
	}
}
