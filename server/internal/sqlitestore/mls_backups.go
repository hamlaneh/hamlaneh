package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The sealed verification backup (migration 0019, ADR 010), the home-mode half
// of storage/mls_backups.go. Its other half — DeregisterMlsDevice, the
// lost-device directory write the backup restore is useless without — lives in
// mls.go here, beside the rest of the device surface it deletes from.
//
// The envelope is opaque here in the strongest sense in this package: not
// merely unparsed, but sealed under a key derived from a recovery key that was
// generated on the device, shown once, and never sent to this server. The two
// integers beside it — the counter and the timestamp — are the entire extent
// of what any query here reads, and the counter is a convenience rail rather
// than the anti-rollback control (which is the client's own floor against the
// counter sealed INSIDE the envelope; see the migration's note).
//
// # No lock order
//
// Neither operation extends anything, on either driver. PutMlsBackup is one
// upsert of one row keyed by user id and takes no other table, and the
// PostgreSQL driver's note that two of the owner's devices racing "serialize
// on that row" is here the flatter fact that they serialize on the database
// write lock — same winner, same refusal for the loser.

// mlsPutBackupQuery stores or replaces the account's envelope, and refuses a
// counter that does not move forward.
//
// One statement rather than a read-then-write: the WHERE on the conflict
// target is evaluated against the row this statement holds, so two of the
// owner's devices racing serialize on it and exactly one of them lands — the
// loser matches nothing and is told its write is stale, rather than silently
// overwriting a newer backup with an older one.
//
// The comparison is a plain > and not a ceremony, exactly as the PostgreSQL
// driver and the migration say: it stops the ordinary lost update between two
// of one person's own devices, and it is worth nothing against a server that
// wants to serve an old blob.
//
// updated_at is bound rather than defaulted on both halves of the upsert. The
// PostgreSQL column defaults to now() and so needs the explicit value only on
// the UPDATE branch; the SQLite column (migration 0019) carries no default at
// all, because this driver takes every timestamp from the one process clock
// that is also the database's. Either way a replacement must not keep the
// original's timestamp: a restoring person would be shown a date from the
// wrong backup, and that date is the only freshness signal a first restore has
// (ADR 010, decision 3).
const mlsPutBackupQuery = `INSERT INTO mls_backups (user_id, envelope, counter, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT (user_id) DO UPDATE
	SET envelope = excluded.envelope, counter = excluded.counter, updated_at = excluded.updated_at
	WHERE mls_backups.counter < excluded.counter`

// PutMlsBackup stores this account's sealed backup, replacing any earlier one.
//
// The counter must exceed the stored one or the write is refused with
// ErrMlsBackupStale. That check is honestly labeled in the contract and in the
// migration: it stops an ordinary lost update, and it is worth nothing against
// a server that wants to serve an old blob, which is what the client's own
// floor is for.
//
// The envelope is never inspected. There is no expression in this file that
// reads a byte of it, which is what makes "the server cannot open your backup"
// a property of the SQL rather than a promise in a comment.
func (s *Store) PutMlsBackup(ctx context.Context, userID uuid.UUID, envelope []byte, counter int64) error {
	res, err := s.db.ExecContext(ctx, mlsPutBackupQuery, userID, envelope, counter, s.nowText())
	if err != nil {
		return fmt.Errorf("put mls backup: %w", err)
	}
	written, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("put mls backup: %w", err)
	}
	// A conflict whose DO UPDATE predicate fails changes no row, which is the
	// refusal — the same zero the PostgreSQL command tag reports for the same
	// reason.
	if written == 0 {
		return storage.ErrMlsBackupStale
	}
	return nil
}

// MlsBackupByUser returns this account's sealed backup, or
// ErrMlsBackupNotFound.
//
// The user id is the primary key, so ownership is the lookup rather than a
// check made around it: there is no argument here that could name somebody
// else's row.
func (s *Store) MlsBackupByUser(ctx context.Context, userID uuid.UUID) (storage.MlsBackup, error) {
	var b storage.MlsBackup
	err := s.db.QueryRowContext(ctx,
		`SELECT envelope, counter, updated_at FROM mls_backups WHERE user_id = ?`,
		userID).Scan(&b.Envelope, &b.Counter, timeScan{dst: &b.UpdatedAt})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.MlsBackup{}, storage.ErrMlsBackupNotFound
	}
	if err != nil {
		return storage.MlsBackup{}, fmt.Errorf("mls backup by user: %w", err)
	}
	return b, nil
}

// DeleteMlsBackup forgets this account's backup. Idempotent: deleting one that
// is not there is the same success, because "there is no copy of this off my
// device" is the state the caller asked for and it is already true.
func (s *Store) DeleteMlsBackup(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mls_backups WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete mls backup: %w", err)
	}
	return nil
}
