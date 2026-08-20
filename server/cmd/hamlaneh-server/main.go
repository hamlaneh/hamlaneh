// Command hamlaneh-server runs the Hamlaneh backend HTTP server.
//
// Usage:
//
//	hamlaneh-server              start the server on :8080
//	hamlaneh-server healthcheck  probe /healthz of a local instance; exit 0 if healthy, 1 otherwise
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

	"github.com/hamlaneh/hamlaneh/server/internal/healthcheck"
	"github.com/hamlaneh/hamlaneh/server/internal/httpserver"
)

const (
	listenAddr         = ":8080"
	shutdownTimeout    = 10 * time.Second
	healthcheckTimeout = 5 * time.Second
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
		return serve(ctx, listenAddr)
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

// serve runs the HTTP server on addr until ctx is canceled, then shuts it
// down gracefully within shutdownTimeout.
func serve(ctx context.Context, addr string) error {
	srv := httpserver.New(addr)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe()
	}()
	slog.Info("hamlaneh-server listening", "addr", addr)

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
