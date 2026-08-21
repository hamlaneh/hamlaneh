// Command hamlaneh-server runs the Hamlaneh backend HTTP server.
//
// Usage:
//
//	hamlaneh-server              start the server on :8080
//	hamlaneh-server healthcheck  probe /healthz of a local instance; exit 0 if healthy, 1 otherwise
//
// The server needs PostgreSQL, configured via the standard libpq environment
// variables (PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD, PGSSLMODE), and
// applies its schema migrations itself at startup — no manual migrate step.
// On a fresh instance (empty users table), HAMLANEH_ADMIN_USERNAME and
// HAMLANEH_ADMIN_PASSWORD (optionally HAMLANEH_ADMIN_LOCALE) create the
// first admin account; once any user exists they are ignored.
// The healthcheck subcommand needs neither a database nor those variables.
//
// The server speaks plain HTTP; TLS termination is the reverse proxy's job.
// It shuts down gracefully on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hamlaneh/hamlaneh/server/internal/bootstrap"
	"github.com/hamlaneh/hamlaneh/server/internal/healthcheck"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

const (
	listenAddr         = ":8080"
	shutdownTimeout    = 10 * time.Second
	healthcheckTimeout = 5 * time.Second

	// dbStartupTimeout caps how long startup waits for the database to
	// accept connections and for migrations to apply; "docker compose up"
	// starts both containers together and PostgreSQL needs a few seconds.
	dbStartupTimeout = 60 * time.Second
)

// healthcheckURL is where the healthcheck subcommand probes the local
// server. A var, not a const, so tests can point it at a test server.
var healthcheckURL = "http://127.0.0.1:8080/healthz"

const usage = `usage:
  hamlaneh-server              start the server on ` + listenAddr + `
  hamlaneh-server healthcheck  probe /healthz; exit 0 if healthy, 1 otherwise`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "hamlaneh-server: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches on the subcommand in args and returns the process outcome
// as an error (nil means exit 0).
func run(args []string) error {
	switch {
	case len(args) == 0:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		store, err := openStorage(ctx)
		if err != nil {
			return err
		}
		defer store.Close()

		adminCfg, present := bootstrap.AdminFromEnv()
		if err := bootstrap.EnsureAdmin(ctx, store, adminCfg, present); err != nil {
			return err
		}

		return serve(ctx, httpserver.New(listenAddr, store))
	case args[0] == "healthcheck":
		if len(args) > 1 {
			return fmt.Errorf("healthcheck takes no arguments\n%s", usage)
		}
		ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
		defer cancel()
		return healthcheck.Check(ctx, healthcheckURL)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

// openStorage connects to PostgreSQL (configured via the standard libpq
// environment variables) and applies pending schema migrations, waiting up
// to dbStartupTimeout for a database that is still starting.
func openStorage(ctx context.Context) (*storage.Store, error) {
	slog.Info("connecting to database and applying migrations")
	dbCtx, cancel := context.WithTimeout(ctx, dbStartupTimeout)
	defer cancel()

	store, err := storage.Open(dbCtx, "")
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	slog.Info("database ready")
	return store, nil
}

// serve runs srv until ctx is canceled, then shuts it down gracefully within
// shutdownTimeout.
func serve(ctx context.Context, srv *http.Server) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()
	slog.Info("hamlaneh-server listening", "addr", srv.Addr)

	select {
	case err := <-serveErr:
		// ListenAndServe failed before any shutdown was requested
		// (e.g. the address is already in use).
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
