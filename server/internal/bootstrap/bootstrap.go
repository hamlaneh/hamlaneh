// Package bootstrap creates the first admin account on a fresh instance.
//
// The install flow (deploy/install.sh) sets HAMLANEH_ADMIN_USERNAME and
// HAMLANEH_ADMIN_PASSWORD; at startup, after migrations, an empty users
// table plus those variables yields exactly one admin who must change the
// password on first login. A non-empty table makes startup ignore the
// variables entirely, so restarts and upgrades are no-ops and the variables
// can never overwrite live data.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/hamlaneh/hamlaneh/server/internal/password"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
	"github.com/hamlaneh/hamlaneh/server/internal/uservalidate"
)

// Environment variables the installer sets.
const (
	EnvUsername = "HAMLANEH_ADMIN_USERNAME"
	EnvPassword = "HAMLANEH_ADMIN_PASSWORD" // #nosec G101 -- the environment variable's NAME; its value is never in the repo
	EnvLocale   = "HAMLANEH_ADMIN_LOCALE"
)

// AdminConfig describes the admin account to create on a fresh instance.
type AdminConfig struct {
	Username string
	Password string
	Locale   string
}

// AdminFromEnv reads the bootstrap variables. present is false when
// username and password are not both set (locale alone bootstraps nothing).
func AdminFromEnv() (cfg AdminConfig, present bool) {
	cfg = AdminConfig{
		Username: os.Getenv(EnvUsername),
		Password: os.Getenv(EnvPassword),
		Locale:   os.Getenv(EnvLocale),
	}
	if cfg.Locale == "" {
		cfg.Locale = "en"
	}
	return cfg, cfg.Username != "" && cfg.Password != ""
}

// Store is what EnsureAdmin needs from storage.
type Store interface {
	CountUsers(ctx context.Context) (int64, error)
	CreateUser(ctx context.Context, nu storage.NewUser) (storage.User, error)
}

// EnsureAdmin makes sure a fresh instance gets its first admin:
//
//   - users exist            → nothing happens, cfg is ignored
//   - empty and !present     → a clear warning explains how to bootstrap
//   - empty and present      → the admin is created with is_admin and
//     must_change_password set
//
// An invalid cfg on a fresh instance fails startup loudly — a half-formed
// admin account would be worse. The password never reaches any log.
//
// created reports whether THIS call made the account, and is the condition a
// caller that generated the password shows it under (home mode does). It is
// false on every path that did not create one — a populated table, no
// configuration, or a race another process won — so a restart can neither
// mint a second admin nor put a live credential back on the console.
func EnsureAdmin(ctx context.Context, store Store, cfg AdminConfig, present bool) (created bool, err error) {
	count, err := store.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("bootstrap: count users: %w", err)
	}
	if count > 0 {
		return false, nil
	}

	if !present {
		slog.Warn("no users exist and no admin bootstrap is configured; " +
			"set " + EnvUsername + " and " + EnvPassword + " (optionally " +
			EnvLocale + ") and restart to create the first admin")
		return false, nil
	}

	if vErr := validate(cfg); vErr != nil {
		return false, fmt.Errorf("bootstrap: %w", vErr)
	}

	admin, err := store.CreateUser(ctx, storage.NewUser{
		Username:           cfg.Username,
		PasswordHash:       password.Hash(cfg.Password),
		Locale:             cfg.Locale,
		IsAdmin:            true,
		MustChangePassword: true,
	})
	if errors.Is(err, storage.ErrUsernameTaken) {
		// Another instance of a shared database won the race; the outcome
		// (an admin exists) is what matters. Not created by us, so a caller
		// holding a generated password must not show it: the account that
		// exists has the winner's, not ours.
		slog.Info("admin bootstrap: user already exists", "username", cfg.Username)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("bootstrap: create admin: %w", err)
	}

	slog.Info("admin bootstrap: created first admin; the password must be changed on first login",
		"username", admin.Username, "user_id", admin.ID)
	return true, nil
}

// validate applies the shared account rules (internal/uservalidate) to the
// configured admin, naming the offending environment variable.
func validate(cfg AdminConfig) error {
	if err := uservalidate.Username(cfg.Username); err != nil {
		return fmt.Errorf("%s %w", EnvUsername, err)
	}
	if err := uservalidate.Password(cfg.Password); err != nil {
		return fmt.Errorf("%s %w", EnvPassword, err)
	}
	if cfg.Locale != "en" && cfg.Locale != "fa" {
		return fmt.Errorf("%s must be one of: en, fa", EnvLocale)
	}
	return nil
}
