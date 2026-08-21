// Package storage owns the PostgreSQL connection pool and schema migrations
// for the Hamlaneh server; it is the one place the rest of the server goes
// through to reach the database.
//
// Configuration comes from the standard libpq environment variables (PGHOST,
// PGPORT, PGDATABASE, PGUSER, PGPASSWORD, PGSSLMODE), which pgx honors
// natively — no DSN string is ever assembled, so passwords need no URL
// escaping.
package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requiredEnv lists the libpq environment variables that must be set when
// Open is called without an explicit connection string. PGPORT and PGSSLMODE
// have workable pgx defaults and stay optional.
var requiredEnv = []string{"PGHOST", "PGDATABASE", "PGUSER", "PGPASSWORD"}

// Ping retry pacing while the database container is still starting. The
// overall budget comes from the caller's context deadline.
const (
	initialRetryInterval = 250 * time.Millisecond
	maxRetryInterval     = 3 * time.Second
	pingTimeout          = 5 * time.Second
)

// Store is a handle to the Hamlaneh database: a pgx connection pool plus the
// schema version this binary expects.
type Store struct {
	pool        *pgxpool.Pool
	wantVersion uint64
}

// Open connects to PostgreSQL and brings the schema up to date.
//
// connString may be empty, in which case configuration comes from the
// standard libpq environment variables (missing ones fail fast with a clear
// error). Connection attempts retry with backoff until ctx expires — the
// database may still be starting — and then all pending migrations run
// before Open returns. The caller owns the returned Store and must Close it.
func Open(ctx context.Context, connString string) (*Store, error) {
	if connString == "" {
		if missing := missingEnv(); len(missing) > 0 {
			return nil, fmt.Errorf(
				"missing required PostgreSQL environment variables: %s "+
					"(the server reads the standard libpq environment variables; "+
					"see deploy/.env.example)",
				strings.Join(missing, ", "),
			)
		}
	}

	wantVersion, err := latestMigrationVersion(migrationFiles, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("embedded migrations: %w", err)
	}

	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	if pingErr := pingUntilReady(ctx, pool); pingErr != nil {
		pool.Close()
		return nil, pingErr
	}

	if migErr := runMigrations(cfg.ConnConfig); migErr != nil {
		pool.Close()
		return nil, migErr
	}

	return &Store{pool: pool, wantVersion: wantVersion}, nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ready reports whether the database answers a ping and its schema matches
// the migrations embedded in this binary. It backs the /readyz probe; the
// caller bounds it via ctx.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	var version uint64
	var dirty bool
	row := s.pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations")
	if err := row.Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return fmt.Errorf("schema version %d is dirty (a migration failed)", version)
	}
	if version != s.wantVersion {
		return fmt.Errorf("schema version is %d, binary expects %d", version, s.wantVersion)
	}
	return nil
}

// missingEnv returns the required libpq environment variables that are unset
// or empty.
func missingEnv() []string {
	missing := []string{}
	for _, name := range requiredEnv {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// pingUntilReady pings pool with growing backoff until it answers or ctx
// expires. The retry exists because "docker compose up" starts the server
// and the database together, and the database routinely needs a few more
// seconds.
func pingUntilReady(ctx context.Context, pool *pgxpool.Pool) error {
	interval := initialRetryInterval
	for {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		err := pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}

		slog.Info("database not ready yet, retrying", "retry_in", interval, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("database not reachable before deadline: %w", errors.Join(ctx.Err(), err))
		case <-time.After(interval):
		}
		interval = min(2*interval, maxRetryInterval)
	}
}
