package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// auditHashLen is the width of one chain hash, pinned by the CHECK
// constraints on prev_hash and entry_hash (migration 0009).
const auditHashLen = 32

// auditColumns is the canonical column list every audit query selects, in
// the order scanAuditEntry expects. Alias-qualified, because the read joins
// the actor.
const auditColumns = `a.seq, a.id, a.action, a.actor_id, a.target_id, a.target_label,
	a.detail, a.ip, a.occurred_at, a.prev_hash, a.entry_hash,
	u.id, u.username, u.display_name`

// AppendAuditEntry appends e to the log, sealing it with seal once its
// place in the chain is fixed. It returns the stored entry, with Seq,
// PrevHash, EntryHash and OccurredAt as they were written.
//
// seal is called INSIDE the transaction that decides the position, and is
// handed the entry it will seal — that is the whole ordering guarantee, and
// it is unchanged here: storage decides where in the chain the entry goes,
// internal/audit holds the key that seals it there.
//
// # Why there is no advisory lock
//
// storage.AppendAuditEntry takes pg_advisory_xact_lock before it reads the
// chain's head, because two appends must not claim the same predecessor:
// under READ COMMITTED both would read the same head and the chain would
// fork into two entries naming one parent — a break no verification could
// tell from a tamper. SQLite has nothing to serialize. This transaction
// holds the database's write lock from BEGIN (_txlock=immediate), so two
// appends run strictly one after the other: the second one waits at BEGIN
// and, by the time it reads the head, the first has committed and is what it
// links to. The lock is absent because a single writer IS the lock; the
// outcome — one chain, one predecessor per entry — is identical.
//
// What is lost is only the PostgreSQL-specific shape: there the lock is
// audit-specific, so an append serializes against other appends alone, while
// here it queues behind every other write in the process.
func (s *Store) AppendAuditEntry(ctx context.Context, e storage.AuditEntry, seal func(storage.AuditEntry) []byte) (storage.AuditEntry, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var tailAt time.Time
		err := tx.QueryRowContext(ctx,
			`SELECT entry_hash, occurred_at FROM audit_entries ORDER BY seq DESC LIMIT 1`,
		).Scan(&e.PrevHash, timeScan{dst: &tailAt})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// The first entry follows 32 zero bytes (migration 0009).
			e.PrevHash = make([]byte, auditHashLen)
		case err != nil:
			return fmt.Errorf("read chain head: %w", err)
		}

		// The log's clock only moves forward. Readers page by occurred_at
		// while the chain is ordered by seq, and an entry stamped before the
		// one it follows would put those two orders in disagreement — which
		// reads as a break that never happened. Truncated to microseconds
		// because that is what the stored encoding keeps (codec.go's six
		// fractional digits, chosen to match timestamptz), so the value
		// hashed is the value stored.
		e.OccurredAt = s.clock().Truncate(time.Microsecond)
		if !e.OccurredAt.After(tailAt) {
			e.OccurredAt = tailAt.UTC()
		}
		e.EntryHash = seal(e)

		// seq is omitted so SQLite assigns it: the column is INTEGER PRIMARY
		// KEY AUTOINCREMENT where PostgreSQL has GENERATED ALWAYS AS
		// IDENTITY, which carries the two properties the chain needs —
		// monotonic, and never reused even after a delete.
		return tx.QueryRowContext(ctx,
			`INSERT INTO audit_entries
			     (id, action, actor_id, target_id, target_label, detail, ip, occurred_at, prev_hash, entry_hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 RETURNING seq`,
			e.ID, e.Action, e.ActorID, e.TargetID, nullString(e.TargetLabel),
			auditDetail(e.Detail), auditIP(e.IP), asTime(e.OccurredAt), e.PrevHash, e.EntryHash,
		).Scan(&e.Seq)
	})
	if err != nil {
		return storage.AuditEntry{}, fmt.Errorf("append audit entry: %w", err)
	}
	return e, nil
}

// ListAuditEntries returns one page of the log, newest first, with each
// entry's actor joined in.
//
// The order is (occurred_at DESC, seq DESC), which is the index the
// migration builds and — because an append never stamps a time before the
// entry it follows — is also the chain's own order reversed. That is what
// lets a caller check the linkage across a page: an unfiltered page is a
// contiguous run of the chain.
//
// The keyset is spelled out rather than compared as a tuple: PostgreSQL
// writes (a.occurred_at, a.seq) < ($3, $4) and SQLite has no row values, so
// the same predicate is the expanded disjunction. It compares identically
// because occurred_at is stored in one fixed-width UTC layout, where
// lexicographic order is chronological order (codec.go).
func (s *Store) ListAuditEntries(ctx context.Context, params storage.ListAuditParams) ([]storage.AuditEntry, error) {
	var action *string
	if params.Action != "" {
		action = &params.Action
	}
	var (
		beforeAt  any
		beforeSeq *int64
	)
	if params.Before != nil {
		beforeAt = asTime(params.Before.OccurredAt)
		beforeSeq = &params.Before.Seq
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+auditColumns+`
		 FROM audit_entries a
		 LEFT JOIN users u ON u.id = a.actor_id
		 WHERE (? IS NULL OR a.action = ?)
		   AND (? IS NULL OR a.actor_id = ?)
		   AND (? IS NULL OR a.occurred_at < ? OR (a.occurred_at = ? AND a.seq < ?))
		 ORDER BY a.occurred_at DESC, a.seq DESC
		 LIMIT ?`,
		nullString(action), nullString(action),
		params.ActorID, params.ActorID,
		beforeAt, beforeAt, beforeAt, beforeSeq,
		params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	// See ListChannelsForUser on why the close error is discarded here.
	defer func() { _ = rows.Close() }()

	entries := make([]storage.AuditEntry, 0, params.Limit)
	for rows.Next() {
		e, scanErr := scanAuditEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list audit entries: %w", scanErr)
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	return entries, nil
}

// auditDetail binds an entry's JSON detail. The column is jsonb on
// PostgreSQL and TEXT here, and it is bound as a Go string so the value
// lands with TEXT affinity rather than as a blob.
//
// SQLite neither validates nor normalises the JSON, so what goes in comes
// out byte for byte. That is stricter than jsonb, which reorders keys and
// drops whitespace, and it is what a tamper-evident log wants: the entry
// hash is computed over these bytes, so a store that rewrote them would
// break every chain it touched.
func auditDetail(detail []byte) any {
	if detail == nil {
		return nil
	}
	return string(detail)
}

// auditIP binds an entry's address. The column is inet on PostgreSQL, where
// pgx maps netip.Addr directly, and TEXT here — netip.Addr is not a
// driver.Valuer, so the encoding is explicit in both directions
// (auditParseIP decodes).
func auditIP(ip *netip.Addr) any {
	if ip == nil {
		return nil
	}
	return ip.String()
}

// auditParseIP decodes a stored address. A value this package did not write
// is a bug rather than a supported input, so it is reported instead of
// silently becoming a nil address.
func auditParseIP(ns sql.NullString) (*netip.Addr, error) {
	if !ns.Valid {
		return nil, nil
	}
	addr, err := netip.ParseAddr(ns.String)
	if err != nil {
		return nil, fmt.Errorf("decode address %q: %w", ns.String, err)
	}
	return &addr, nil
}

// auditUUIDPtr decodes a nullable uuid column into *uuid.UUID.
func auditUUIDPtr(nu uuid.NullUUID) *uuid.UUID {
	if !nu.Valid {
		return nil
	}
	id := nu.UUID
	return &id
}

// scanAuditEntry scans one auditColumns row.
func scanAuditEntry(row rowScanner) (storage.AuditEntry, error) {
	var (
		e           storage.AuditEntry
		entryActor  uuid.NullUUID
		targetID    uuid.NullUUID
		targetLabel sql.NullString
		detail      sql.NullString
		ip          sql.NullString
		joinedActor uuid.NullUUID
		username    sql.NullString
		displayName sql.NullString
	)
	err := row.Scan(
		&e.Seq, &e.ID, &e.Action, &entryActor, &targetID, &targetLabel,
		&detail, &ip, timeScan{dst: &e.OccurredAt}, &e.PrevHash, &e.EntryHash,
		&joinedActor, &username, &displayName,
	)
	if err != nil {
		return storage.AuditEntry{}, notFound(err)
	}

	e.ActorID = auditUUIDPtr(entryActor)
	e.TargetID = auditUUIDPtr(targetID)
	e.TargetLabel = stringPtr(targetLabel)
	if detail.Valid {
		e.Detail = []byte(detail.String)
	}
	if e.IP, err = auditParseIP(ip); err != nil {
		return storage.AuditEntry{}, err
	}

	// The join is a LEFT one: an entry with no actor, and an actor whose
	// account is gone, both arrive as nulls.
	if joinedActor.Valid && username.Valid && displayName.Valid {
		e.Actor = &storage.AuditActor{
			ID: joinedActor.UUID, Username: username.String, DisplayName: displayName.String,
		}
	}
	return e, nil
}
