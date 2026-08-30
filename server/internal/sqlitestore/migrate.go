package sqlitestore

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// migrationsDir holds this driver's own NNNN_description.{up,down}.sql tree.
//
// It is a PARALLEL tree, not a translation of the PostgreSQL one: the
// PostgreSQL migrations are untouched (never edit an applied migration), and
// these are written for SQLite. The numbering is deliberately the same, so
// the two read side by side and a future slice adds one sibling to each.
//
// It lives inside this package rather than beside the PostgreSQL tree because
// go:embed cannot reach outside its own directory; the package name is the
// dialect name ADR 012 asks the directory to carry.
const migrationsDir = "migrations"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// runMigrations applies all pending schema migrations, so a first run creates
// the database and every later run is a no-op.
//
// It opens its own short-lived handle rather than borrowing the Store's pool,
// for the same reason the PostgreSQL driver does: golang-migrate's sqlite
// driver closes the *sql.DB it was given when the migration finishes, and
// handing it the pool would tangle connection ownership — here it would
// simply close the pool the Store is about to use.
func runMigrations(dsn string) (err error) {
	src, err := iofs.New(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return errors.Join(fmt.Errorf("open migration connection: %w", err), src.Close())
	}

	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return errors.Join(fmt.Errorf("prepare migration driver: %w", err), db.Close(), src.Close())
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
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

// latestMigrationVersion is storage.LatestMigrationVersion applied to this
// tree; Ready compares it against the database's recorded schema version.
var latestMigrationVersion = storage.LatestMigrationVersion
