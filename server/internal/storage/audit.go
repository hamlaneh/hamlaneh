package storage

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuditEntry is a row of audit_entries: one recorded action, and its place
// in the hash chain. The hashes are computed outside this package, by
// internal/audit, which holds the key — storage stores what it is given and
// decides only where in the chain it goes.
type AuditEntry struct {
	// Seq is the chain's total order, assigned by the database.
	Seq    int64
	ID     uuid.UUID
	Action string
	// ActorID is nil for something the system did rather than a person.
	ActorID  *uuid.UUID
	TargetID *uuid.UUID
	// TargetLabel is what the target was called at the time, kept as text so
	// the log still reads after a rename or a deletion.
	TargetLabel *string
	// Detail is the entry's JSON detail, nil for none.
	Detail     []byte
	IP         *netip.Addr
	OccurredAt time.Time
	PrevHash   []byte
	EntryHash  []byte

	// Actor is the person ActorID names, filled by the read so a page can be
	// rendered without a lookup per row. It is deliberately outside the
	// chain: it is a copy of a name that can change, and a rename must not
	// read as a tamper.
	Actor *AuditActor
}

// AuditActor is the person an entry names, as the log displays them: the
// contract's UserSummary and nothing else.
type AuditActor struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
}

// auditHashLen is the width of one chain hash, pinned by the CHECK
// constraints on prev_hash and entry_hash (migration 0009).
const auditHashLen = 32

// auditChainLock is the advisory-lock key every append takes. Advisory
// locks are per database, so a test database and a production one never
// contend, and the constant is arbitrary beyond being ours alone.
const auditChainLock int64 = 0x48414D4C41554449 // "HAMLAUDI"

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
// handed the entry it will seal — that is the whole ordering guarantee.
// Two concurrent appends cannot claim the same predecessor because the
// transaction takes an advisory lock before it reads the chain's head:
// the second one waits, and by the time it reads, the first has committed
// and is what it links to. Without the lock both would read the same head
// under READ COMMITTED, and the chain would fork into two entries claiming
// one predecessor — a break that no verification could distinguish from a
// tamper.
//
// ponytail: one instance-wide lock serializes every append. An audit log
// writes once per administrative action, so the contention ceiling is far
// above anything this can reach; per-shard chains would be the upgrade if
// that ever stopped being true.
func (s *Store) AppendAuditEntry(ctx context.Context, e AuditEntry, seal func(AuditEntry) []byte) (AuditEntry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("append audit entry: %w", err)
	}
	defer func() {
		// Rollback after a successful Commit is a no-op returning
		// pgx.ErrTxClosed; nothing here is worth reporting over the error
		// this function already returns.
		_ = tx.Rollback(ctx)
	}()

	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, auditChainLock); err != nil {
		return AuditEntry{}, fmt.Errorf("append audit entry: lock chain: %w", err)
	}

	var tailAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT entry_hash, occurred_at FROM audit_entries ORDER BY seq DESC LIMIT 1`,
	).Scan(&e.PrevHash, &tailAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The first entry follows 32 zero bytes (migration 0009).
		e.PrevHash = make([]byte, auditHashLen)
	case err != nil:
		return AuditEntry{}, fmt.Errorf("append audit entry: read chain head: %w", err)
	}

	// The log's clock only moves forward. Readers page by occurred_at while
	// the chain is ordered by seq, and an entry stamped before the one it
	// follows would put those two orders in disagreement — which reads as a
	// break that never happened. Truncated to microseconds because that is
	// what timestamptz keeps, so the value hashed is the value stored.
	e.OccurredAt = time.Now().UTC().Truncate(time.Microsecond)
	if !e.OccurredAt.After(tailAt) {
		e.OccurredAt = tailAt.UTC()
	}
	e.EntryHash = seal(e)

	err = tx.QueryRow(ctx,
		`INSERT INTO audit_entries
		     (id, action, actor_id, target_id, target_label, detail, ip, occurred_at, prev_hash, entry_hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING seq`,
		e.ID, e.Action, e.ActorID, e.TargetID, e.TargetLabel,
		e.Detail, e.IP, e.OccurredAt, e.PrevHash, e.EntryHash,
	).Scan(&e.Seq)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("append audit entry: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return AuditEntry{}, fmt.Errorf("append audit entry: %w", err)
	}
	return e, nil
}

// AuditCursor is the keyset position of the last row of a page: the
// (occurred_at, seq) of that entry. The pair, not the timestamp alone —
// two entries can share a microsecond, and a timestamp-only cursor would
// either skip one or repeat it.
type AuditCursor struct {
	OccurredAt time.Time
	Seq        int64
}

// ListAuditParams pages the log, newest first. Both filters are the ones
// the contract offers; empty means no filter.
type ListAuditParams struct {
	Action  string
	ActorID *uuid.UUID
	// Before is the keyset anchor: entries strictly older than it.
	Before *AuditCursor
	Limit  int
}

// ListAuditEntries returns one page of the log, newest first, with each
// entry's actor joined in.
//
// The order is (occurred_at DESC, seq DESC), which is the index the
// migration builds and — because an append never stamps a time before the
// entry it follows — is also the chain's own order reversed. That is what
// lets a caller check the linkage across a page: an unfiltered page is a
// contiguous run of the chain.
func (s *Store) ListAuditEntries(ctx context.Context, params ListAuditParams) ([]AuditEntry, error) {
	var action *string
	if params.Action != "" {
		action = &params.Action
	}
	var beforeAt *time.Time
	var beforeSeq *int64
	if params.Before != nil {
		beforeAt = &params.Before.OccurredAt
		beforeSeq = &params.Before.Seq
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+auditColumns+`
		 FROM audit_entries a
		 LEFT JOIN users u ON u.id = a.actor_id
		 WHERE ($1::text IS NULL OR a.action = $1)
		   AND ($2::uuid IS NULL OR a.actor_id = $2)
		   AND ($3::timestamptz IS NULL OR (a.occurred_at, a.seq) < ($3, $4))
		 ORDER BY a.occurred_at DESC, a.seq DESC
		 LIMIT $5`,
		action, params.ActorID, beforeAt, beforeSeq, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]AuditEntry, 0, params.Limit)
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

func scanAuditEntry(row pgx.Row) (AuditEntry, error) {
	var e AuditEntry
	var actorID *uuid.UUID
	var username, displayName *string
	err := row.Scan(
		&e.Seq, &e.ID, &e.Action, &e.ActorID, &e.TargetID, &e.TargetLabel,
		&e.Detail, &e.IP, &e.OccurredAt, &e.PrevHash, &e.EntryHash,
		&actorID, &username, &displayName,
	)
	if err != nil {
		return AuditEntry{}, err
	}
	// The join is a LEFT one: an entry with no actor, and an actor whose
	// account is gone, both arrive as nulls.
	if actorID != nil && username != nil && displayName != nil {
		e.Actor = &AuditActor{ID: *actorID, Username: *username, DisplayName: *displayName}
	}
	e.OccurredAt = e.OccurredAt.UTC()
	return e, nil
}
