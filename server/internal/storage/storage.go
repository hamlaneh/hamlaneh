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
//
// # Lock order: the messaging tables
//
// The conversation tables extend the same order at the end:
//
//	… → sessions → channels → channel_members → messages → attachments
//
// No messaging operation calls lockAccount, and all but one of them take no
// explicit row lock at all. That is deliberate rather than an oversight.
// What these operations need to serialize on is a uniqueness claim — one DM
// per user pair, one message per (channel, author, client_msg_id) — and a
// unique index already serializes exactly that, at the row that matters,
// without pulling an unrelated account row into the transaction. An account
// lock here would be lock traffic with nothing to serialize.
//
// Two consequences worth stating, because both are easy to reason about
// wrongly:
//
//   - Inserting a message or a membership row takes an implicit KEY SHARE
//     lock on every row its foreign keys reference — the author's and the DM
//     participants' users rows, the channel row. KEY SHARE conflicts with
//     the FOR UPDATE that lockAccount takes, so a message insert can block
//     behind a password reset on its own author. Blocking is not deadlock:
//     the messaging transaction never goes on to ask for a lock an
//     account-scoped transaction holds, so no cycle can close.
//   - The multi-statement transactions (CreateChannel, OpenDirectMessage)
//     insert into channels before channel_members, matching the order above.
//
// The one exception is RemoveChannelMember, which enforces the contract's
// last_member refusal and so does take an explicit row lock. It needs one
// because a unique index cannot express what it must serialize: two members
// leaving at the same instant delete two different rows, nothing brings
// those two statements into conflict, and both therefore read a member count
// of two and both succeed — write skew, which READ COMMITTED does not
// prevent. So the channels row is taken first, as the one thing both
// removals must agree on:
//
//	SELECT kind FROM channels WHERE id = $1 FOR NO KEY UPDATE
//
// and the count and the delete follow it inside the same transaction. Two
// removals of one channel then run strictly one after the other, and the
// second counts what the first left behind — the count is a separate
// statement, so under READ COMMITTED it takes a fresh snapshot once the wait
// ends. RemoveChannelMember asks for that isolation level explicitly rather
// than inheriting it, because at REPEATABLE READ the count would read the
// snapshot from before the wait and the rule would silently stop holding.
//
// The lock strength is the entire design, and FOR UPDATE — the obvious
// choice, and the one the hazard note this paragraph replaces was written
// about — is the one that must not be used. Every AddChannelMember takes KEY
// SHARE on this same channels row, because that is what its foreign key
// check takes, and it takes it while already holding the channel_members row
// it has just inserted. KEY SHARE conflicts with FOR UPDATE, so a removal
// and an add on one channel would queue against each other on a row neither
// of them changes: whichever arrives second waits for the first. A single
// row cannot deadlock on its own, but a remover that can be made to wait for
// an adder is the edge a cycle needs, and it is the edge the old hazard note
// was about. KEY SHARE does not conflict with FOR NO KEY UPDATE, so under
// this lock the two never queue there at all
// (TestRemoveChannelMemberDoesNotBlockOnAddIntegration holds an add open at
// exactly that instant and fails if a removal waits for it).
//
// The other direction is closed by construction rather than by lock
// strength: the remover never waits on an adder anywhere. Under the lock it
// reads channel_members with a plain snapshot read, which waits for nothing,
// and then deletes one row it can already see — never a row an adder is
// still inserting, which its snapshot cannot contain, and never a row an
// adder holds, because ON CONFLICT DO NOTHING takes no lock on the committed
// row it declines to touch. An adder may wait for a remover; a remover never
// waits for an adder, and a cycle needs both.
//
// The MLS transport tables extend the same order once more at the end —
// mls_devices → mls_key_packages → mls_groups → mls_commits → mls_welcomes —
// and mls.go's header carries the argument for why neither operation that
// takes a lock there can close a cycle with the other or with anything above.
//
// Anything that later wants to serialize on a channel takes the same lock at
// the same strength. FOR UPDATE on channels is what puts removals and adds
// back in each other's way; a plain UPDATE of that row (UpdateChannelTopic)
// already takes FOR NO KEY UPDATE implicitly, so it queues behind a removal
// and holds nothing a removal wants.
//
// The second explicit lock is the attachment claim in CreateMessage. A send
// carrying files inserts its message and then attaches its uploads in one
// transaction — messages before attachments, as the order above says — and
// the claim takes its rows with SELECT ... FOR UPDATE inside the UPDATE:
//
//	SELECT id FROM attachments WHERE id = ANY($ids) AND … ORDER BY id FOR UPDATE
//
// The ORDER BY is the whole point of doing it explicitly. Without it
// PostgreSQL locks the matched rows in whatever order the scan yields, and
// two sends claiming an overlapping set of ids could take the same two rows
// in opposite orders — the one cycle this path could form. With it every
// claim walks the ids in the same order, so the second sender queues behind
// the first instead of crossing it, and then (READ COMMITTED re-evaluating
// the predicate against the updated row) finds message_id no longer NULL,
// returns a short count, and the send fails with ErrAttachmentNotFound. The
// insert half adds no edge: it locks a row nobody else can name yet, plus
// the KEY SHARE its foreign keys take, and the transaction never goes back
// for anything a concurrent one holds.
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
