package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The sealed verification backup and the lost-device path (migration 0019,
// ADR 010).
//
// Two operations that look unrelated and are one story. A person who lost a
// device needs both halves: the blob that carries what that device knew, and
// the directory write that stops its signature key being listed as theirs.
// Neither is useful alone — a restore onto a fresh device while the lost one
// stays allow-listed leaves the stolen leaf inside every group, and dropping
// the leaf without a backup leaves the new device with no trust decisions at
// all.
//
// The envelope is opaque here in the strongest sense in this package: not
// merely unparsed, but sealed under a key derived from a recovery key that was
// generated on the device, shown once, and never sent to this server. The two
// integers beside it — the counter and the timestamp — are the entire extent
// of what any query here reads, and the counter is a convenience rail rather
// than the anti-rollback control (which is the client's own floor against the
// counter sealed INSIDE the envelope; see the migration's note).
//
// # Lock order
//
// Neither operation extends the package-wide order. PutMlsBackup is one
// upsert of one row keyed by user id and takes no other table; DeregisterMlsDevice
// is one delete on mls_devices whose cascades (mls_key_packages, mls_welcomes)
// run below it in the declared order.

var (
	// ErrMlsBackupNotFound reports that this account has stored no backup —
	// a state a person can genuinely be in, and the contract's 404
	// mls_backup_not_found.
	ErrMlsBackupNotFound = errors.New("storage: mls backup not found")
	// ErrMlsBackupStale reports an upload whose counter does not move past the
	// stored one: the ordinary lost update between two of the owner's own
	// devices. It is the contract's 409 mls_backup_stale, and it is not a
	// security control — a hostile server ignores its own check.
	ErrMlsBackupStale = errors.New("storage: mls backup counter does not advance")
)

// MlsBackup is one account's sealed envelope with the two facts the server may
// read about it.
//
// Counter is the client's own convenience copy of the value sealed in the
// envelope's authenticated header. Nothing here treats the two as equal,
// because nothing here can: opening the envelope is what would prove them
// equal, and this server cannot.
type MlsBackup struct {
	Envelope  []byte
	Counter   int64
	UpdatedAt time.Time
}

// putBackupQuery stores or replaces the account's envelope, and refuses a
// counter that does not move forward.
//
// One statement rather than a read-then-write: the WHERE on the conflict
// target is evaluated against the row this statement holds, so two of the
// owner's devices racing serialize on that row and exactly one of them lands —
// the loser matches nothing and is told its write is stale, rather than
// silently overwriting a newer backup with an older one.
//
// updated_at is set explicitly rather than left to the column default, which
// applies only to an INSERT: a replacement that kept the original timestamp
// would show a restoring person a date from the wrong backup, and that date is
// the only freshness signal a first restore has (ADR 010, decision 3).
const putBackupQuery = `INSERT INTO mls_backups (user_id, envelope, counter)
	VALUES ($1, $2, $3)
	ON CONFLICT (user_id) DO UPDATE
	SET envelope = EXCLUDED.envelope, counter = EXCLUDED.counter, updated_at = now()
	WHERE mls_backups.counter < EXCLUDED.counter`

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
	tag, err := s.pool.Exec(ctx, putBackupQuery, userID, envelope, counter)
	if err != nil {
		return fmt.Errorf("put mls backup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMlsBackupStale
	}
	return nil
}

// MlsBackupByUser returns this account's sealed backup, or
// ErrMlsBackupNotFound.
//
// The user id is the primary key, so ownership is the lookup rather than a
// check made around it: there is no argument here that could name somebody
// else's row.
func (s *Store) MlsBackupByUser(ctx context.Context, userID uuid.UUID) (MlsBackup, error) {
	var b MlsBackup
	err := s.pool.QueryRow(ctx,
		`SELECT envelope, counter, updated_at FROM mls_backups WHERE user_id = $1`,
		userID).Scan(&b.Envelope, &b.Counter, &b.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MlsBackup{}, ErrMlsBackupNotFound
	}
	if err != nil {
		return MlsBackup{}, fmt.Errorf("mls backup by user: %w", err)
	}
	return b, nil
}

// DeleteMlsBackup forgets this account's backup. Idempotent: deleting one that
// is not there is the same success, because "there is no copy of this off my
// device" is the state the caller asked for and it is already true.
func (s *Store) DeleteMlsBackup(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM mls_backups WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("delete mls backup: %w", err)
	}
	return nil
}

// DeregisterMlsDevice drops one of this account's devices from the directory —
// the lost-device write ADR 007's sweep needs in order to act.
//
// The user id is part of the WHERE clause rather than a check made before it,
// which is what makes another account's device id and an id naming nothing one
// answer: neither matches, so neither can remove foreign state and neither can
// be told apart from the other. ErrMlsDeviceNotFound covers both, and an
// already-deregistered device is in exactly that set.
//
// What it deliberately does NOT do: revoke sessions, and touch messages.
// Signing a device out and un-listing its key answer different questions, and
// a person who lost a laptop needs both — coupling them here would make the
// directory write reach into session state it has no business in.
//
// The cascades in migration 0017 do the rest, and they are the reason a
// pending Welcome cannot strand anything: mls_key_packages and mls_welcomes
// both reference mls_devices ON DELETE CASCADE, so an unclaimed package can
// never be handed out for a device that no longer exists, and a Welcome
// addressed to it goes with it rather than sitting in a list its recipient can
// never acknowledge.
func (s *Store) DeregisterMlsDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM mls_devices WHERE id = $1 AND user_id = $2`, deviceID, userID)
	if err != nil {
		return fmt.Errorf("deregister mls device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMlsDeviceNotFound
	}
	return nil
}
