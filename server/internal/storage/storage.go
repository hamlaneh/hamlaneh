// Package storage owns the PostgreSQL connection pool and schema migrations
// for the Hamlaneh server; it is the one place the rest of the server goes
// through to reach the database.
//
// Configuration comes from the standard libpq environment variables (PGHOST,
// PGPORT, PGDATABASE, PGUSER, PGPASSWORD, PGSSLMODE), which pgx honors
// natively — no DSN string is ever assembled, so passwords need no URL
// escaping.
//
// # Lock order
//
// Much of what this package does races by nature — an attempt counter, a
// single-use consumption, an activation switch — and each of those runs in a
// transaction that locks the rows it is about to change with
// SELECT ... FOR UPDATE. Two transactions taking the same locks in opposite
// orders deadlock, so the whole package, across files, obeys one order:
//
//	users → password_reset_tokens → totp_challenges → user_totp →
//	user_recovery_codes → sessions
//
// with one rule on top of it: an account-scoped transaction takes the
// account's own row FIRST, via lockAccount:
//
//	SELECT ... FROM users WHERE id = $1 FOR UPDATE
//
// That single lock is what serializes concurrent operations on one account,
// and it is the reason CreatePasswordResetToken can promise one live link
// per account and ConsumePasswordReset can promise that no session survives
// a completed reset. Both promises are about a writer this transaction
// cannot see: under READ COMMITTED a sweep never observes another
// transaction's uncommitted INSERT, so the writer has to be excluded rather
// than out-read. Because every account-scoped transaction takes that lock
// before it takes any other, two of them can never hold conflicting locks on
// one account at the same time, and no cycle between them can form.
//
// Two corollaries that are easy to get wrong:
//
//   - A transaction addressed by a token hash (ConsumePasswordReset,
//     CompleteTotpChallenge) does not know the account yet. It resolves the
//     owner with an UNLOCKED read first — user_id never changes on those
//     rows — then takes the account lock, then re-reads the row FOR UPDATE.
//     The unlocked read decides nothing; the re-read under the lock is what
//     decides liveness. Locking the token row first and the account second
//     is the shape that deadlocks, because CreatePasswordResetToken holds
//     the account and wants the token rows.
//   - A statement that locks rows in exactly one table cannot deadlock and
//     needs no account lock (CreateTotpChallenge, ActivateTotp,
//     RotateSession, RevokeFamily, RevokeUserFamily, RevokeOtherFamilies).
//     Adding one there would be lock traffic with nothing to serialize.
//
// Operations that never touch users at all — DisableTotp, StartTotpSetup,
// VerifyTotpSetup, ReplaceRecoveryCodes — still obey the table order among
// themselves (totp_challenges → user_totp → user_recovery_codes).
package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
	cfg.AfterConnect = registerCitext

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

	// Connections opened before the migrations ran (the readiness pings)
	// could not register citext because the extension did not exist yet on a
	// fresh database. Dropping them forces every future connection through
	// registerCitext with the extension in place.
	pool.Reset()

	return &Store{pool: pool, wantVersion: wantVersion}, nil
}

// registerCitext teaches each new connection the citext extension type, so
// username and email parameters and results round-trip as ordinary strings
// with case-insensitive matching done by PostgreSQL. The OID is resolved by
// hand and bound to the text codec (citext is wire-compatible with text)
// because pgx's LoadType only handles derived types, not extension base
// types.
//
// On a fresh database the type does not exist until migration 0001 runs
// (PostgreSQL undefined_object, 42704); only that is tolerated — the
// pre-migration connections just ping and are reset after migrating. Every
// other failure propagates: swallowing it would leave connections silently
// unable to bind citext columns.
func registerCitext(ctx context.Context, conn *pgx.Conn) error {
	var oid uint32
	if err := conn.QueryRow(ctx, `select 'citext'::regtype::oid`).Scan(&oid); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UndefinedObject {
			return nil
		}
		return fmt.Errorf("look up citext type: %w", err)
	}
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "citext", OID: oid, Codec: pgtype.TextCodec{}})
	return nil
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
