package storage

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// migrationsDir is the embedded directory holding NNNN_description.{up,down}.sql
// files in golang-migrate's layout. Never edit an applied migration.
const migrationsDir = "migrations"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// runMigrations applies all pending schema migrations so an install never
// needs a manual migrate step. golang-migrate speaks database/sql, so it
// gets its own short-lived connection built from the pool's parsed config
// (handing it the shared pool would tangle connection ownership). Applying
// an already-applied set is a no-op, which keeps startup idempotent.
func runMigrations(connCfg *pgx.ConnConfig) (err error) {
	src, err := iofs.New(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	db := stdlib.OpenDB(*connCfg)
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return errors.Join(fmt.Errorf("prepare migration driver: %w", err), db.Close(), src.Close())
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		// Closing the driver also closes db.
		return errors.Join(fmt.Errorf("prepare migrations: %w", err), driver.Close(), src.Close())
	}
	defer func() {
		srcErr, dbErr := m.Close()
		err = errors.Join(err, srcErr, dbErr)
	}()

	if upErr := m.Up(); upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	return nil
}

// LatestMigrationVersion returns the highest migration version among the
// *.up.sql files in dir of fsys. Ready compares it against the database's
// schema_migrations row to detect a schema that lags (or leads) this binary.
//
// It is exported because the naming convention it enforces is the contract
// between a driver and its own migration tree, and home mode's SQLite driver
// (internal/sqlitestore) keeps a parallel tree under the same convention. The
// rule belongs in one place; a second copy would be a second rule.
func LatestMigrationVersion(fsys fs.FS, dir string) (uint64, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("read migrations directory: %w", err)
	}

	var latest uint64
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, hasSep := strings.Cut(name, "_")
		if !hasSep {
			return 0, fmt.Errorf("migration %q: name must look like NNNN_description.up.sql", name)
		}
		version, err := strconv.ParseUint(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: parse version: %w", name, err)
		}
		latest = max(latest, version)
		found = true
	}
	if !found {
		return 0, errors.New("no *.up.sql migrations embedded")
	}
	return latest, nil
}
