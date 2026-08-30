package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The MLS transport tables (migration 0017, ADR 006), the home-mode half of
// storage/mls.go. Every []byte below is an opaque MLS artifact: this package
// stores, sequences and hands it back, and no query in this file looks inside
// one. That is a property of the SQL rather than a promise — there is no
// expression anywhere here that reads a byte of data, message, welcome,
// group_id or signature_public_key, so the server cannot start parsing MLS
// without a new query being written.
//
// # No lock order, and why the essay it replaces is not missing
//
// storage/mls.go extends the package-wide lock order with five tables and
// argues that its two locking operations cannot close a cycle between them:
// ClaimMlsKeyPackages takes mls_key_packages rows FOR UPDATE SKIP LOCKED and
// touches nothing else, and SubmitMlsCommit takes the mls_groups row first and
// only then inserts into the tables below it. Both clauses exist to serialize
// writers PostgreSQL would otherwise run simultaneously.
//
// There is no ordering problem to solve here, because there is nothing to
// order: one write transaction holds the database's write lock and every other
// write waits at BEGIN, so no two of these operations overlap and no cycle is
// constructible. What that costs, and why each outcome is nevertheless the
// same, is stated at the site of every clause dropped — a reader arrives at
// the method, not at this header.

// mlsClaimKeyPackageQuery consumes one key package of one device, or returns
// no row when the pool is empty.
//
// The delete and the pick are one statement, so a package can never be handed
// to two adders: the row is gone in the same transaction that returned it.
//
// The PostgreSQL driver takes the picked row FOR UPDATE SKIP LOCKED, so that a
// second claimer racing for the same package neither blocks on a row it is
// about to be denied anyway nor deadlocks against the first — it reports an
// empty pool immediately. There are no row locks here to skip: this statement
// runs alone under the database write lock, so the first claimer takes the
// package and the second finds nothing left. SKIP LOCKED's never-wait property
// is what is lost — the second claimer waits, briefly, for the write lock —
// and the OUTCOME is kept exactly: one claim, one honest empty answer, no
// deadlock, never two claims.
//
// Oldest first, because a key package embeds an expiry the server cannot read:
// spending the oldest first is the only ordering that does not systematically
// strand the packages closest to expiring.
const mlsClaimKeyPackageQuery = `DELETE FROM mls_key_packages
	WHERE id = (
	    SELECT id FROM mls_key_packages
	    WHERE device_id = ?
	    ORDER BY created_at, id
	    LIMIT 1
	)
	RETURNING data`

// mlsAdvanceEpochQuery is the compare-and-swap the whole design turns on
// (ADR 006). It advances the group by exactly one epoch, and only while the
// epoch the committer named is still current.
//
// The PostgreSQL driver runs the same conditional UPDATE and reads the new
// epoch back with RETURNING; of two concurrent committers the second blocks on
// the row until the first commits and then re-evaluates its predicate against
// the updated row, which is READ COMMITTED behaviour it depends on. Here the
// second committer cannot even start until the first has committed, because it
// waits for the write lock at BEGIN — so it evaluates the same predicate
// against the same already-updated row, matches nothing, and is told to
// refetch. The row count decides the winner, and the new epoch needs no
// RETURNING: a match on epoch = ? means the row now carries that plus one.
//
// Nothing here reads the commit blob; the epoch is a claim the client makes in
// the envelope, and the server uses it for first-wins rejection and nothing
// else.
const mlsAdvanceEpochQuery = `UPDATE mls_groups SET epoch = epoch + 1
	WHERE channel_id = ? AND epoch = ?`

// mlsInsertWelcomeQuery stores one pending Welcome for one recipient device.
//
// The PostgreSQL driver fans one Welcome out to all its devices in a single
// INSERT ... SELECT over unnest, so the blob is bound once per statement no
// matter how many devices it covers. SQLite has no array type and so no
// unnest, and this driver's rule for a set-valued WRITE is a loop of
// single-row statements inside the transaction: the write lock held for the
// whole transaction makes the loop exactly as atomic as the one statement was,
// and at home-mode scale — a commit the contract caps at eight Welcomes, each
// naming the devices of one household — the difference is not measurable.
// What it does cost is re-binding the blob once per recipient rather than once
// per Welcome, which is the one place this port is genuinely more work than
// its original.
//
// It inserts against the foreign key rather than joining mls_devices, exactly
// as the original does, so a device id naming nothing violates the key instead
// of being silently dropped. A committed add whose Welcome vanished is a
// forked group; failing loudly inside the commit's transaction is what keeps
// that impossible.
const mlsInsertWelcomeQuery = `INSERT INTO mls_welcomes (id, channel_id, recipient_device_id, welcome, created_at)
	VALUES (?, ?, ?, ?, ?)`

// mlsWelcomeOutsidersQuery counts, among the named devices, those whose owner
// is not a member of the channel — what keeps a Welcome from being planted on
// a stranger.
//
// The foreign key alone constrains only that a device exists, and a device id
// is not a secret between members: whoever claims key packages in any shared
// channel learns them. Without this count, a member of channel X could attach
// a Welcome addressed to somebody who is not in X — and because
// ListMlsWelcomes returns the row's channel id and group id to the device's
// owner, the row itself would hand a non-member the identifiers the
// channel-scoped 404s exist to withhold. The recipient could never join it
// either, so it would be re-fetched and re-attempted forever.
//
// It drives from mls_devices filtered by the id list, where the PostgreSQL
// driver drives from unnest and joins mls_devices. Same set: a device id
// naming nothing matches no row here and still reaches the foreign key, so a
// Welcome for a device that does not exist stays the loud failure it was. A
// repeated id is counted once rather than twice, which the caller cannot
// observe — it compares the count against zero.
func mlsWelcomeOutsidersQuery(ids int) string {
	return `SELECT count(*) FROM mls_devices dev
		WHERE NOT EXISTS (
			SELECT 1 FROM channel_members m WHERE m.channel_id = ? AND m.user_id = dev.user_id
		)
		AND dev.id IN (` + mlsPlaceholders(ids) + `)`
}

// mlsMemberDevicesQuery is the eviction allow-list, one page at a time
// (ADR 007 decision 2): every CURRENT member of the channel, each with the
// signature keys of every device they have registered.
//
// The LEFT JOIN is the security-relevant word in it, and it cannot be narrowed
// to an inner join by anybody optimizing this later. The sweep it feeds evicts
// every leaf whose key is not in the answer, so a member dropped from a page —
// which is what an inner join does to a member who has registered no device
// yet — is a member whose real leaves the next reconciliation evicts. Their
// absence would read as "nobody", and the empty key list is what says
// "somebody, with nothing registered".
//
// It drives from channel_members rather than from mls_devices for the mirror
// reason: a device belonging to a NON-member has no row here to be aggregated
// onto, so it can never reach a page, and the allow-list never authorizes a
// leaf of somebody who has left.
//
// PostgreSQL aggregates the keys into an array per member with
// array_remove(array_agg(...), NULL) and applies LIMIT to the grouped result.
// SQLite has no array type, so the join is returned flat — one row per member
// per device, and one row with a NULL key for a member who has none — and
// ListMlsMemberDevices groups it in Go. The LIMIT therefore has to be applied
// to the MEMBERS before the join, which is what the subquery is for: applied
// to the flat result it would cut a member's devices in half and call it a
// page.
//
// Paging is a keyset on user id — the channel_members primary key
// (channel_id, user_id), so a page is an index range scan rather than an
// offset — and the order is total, so a full walk covers every member exactly
// once.
const mlsMemberDevicesQuery = `SELECT m.user_id, d.signature_public_key
	FROM (
	    SELECT user_id FROM channel_members
	    WHERE channel_id = ? AND (? IS NULL OR user_id > ?)
	    ORDER BY user_id
	    LIMIT ?
	) m
	LEFT JOIN mls_devices d ON d.user_id = m.user_id
	ORDER BY m.user_id, d.created_at, d.id`

// mlsWelcomeColumns is the canonical column list of a pending Welcome, in the
// order scanMlsWelcome expects.
const mlsWelcomeColumns = `w.id, w.channel_id, g.group_id, w.recipient_device_id, w.welcome, w.created_at`

// RegisterMlsDevice records one client instance's signature key, or returns
// the registration that already exists. created reports which happened — the
// contract's 201 and its 200.
//
// Idempotent on (user, signature_public_key), so a client may call it on
// every startup with no bookkeeping. Two startups racing serialize on the
// unique index in the PostgreSQL driver: ON CONFLICT DO NOTHING waits for the
// concurrent inserter, and the follow-up read takes a new READ COMMITTED
// snapshot that sees the row it committed. Here the two inserts cannot overlap
// at all — the second waits for the write lock — so the loser's ON CONFLICT
// sees a committed row and its follow-up read finds it.
//
// The key is stored as an opaque identifier. Nothing in this package ever
// verifies a signature with it, and a client that registers garbage only
// makes itself unaddable.
func (s *Store) RegisterMlsDevice(ctx context.Context, userID uuid.UUID, signatureKey []byte) (storage.MlsDevice, bool, error) {
	device, err := scanMlsDevice(s.db.QueryRowContext(ctx,
		`INSERT INTO mls_devices (id, user_id, signature_public_key, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id, signature_public_key) DO NOTHING
		 RETURNING id, user_id, signature_public_key, created_at`,
		uuid.New(), userID, signatureKey, s.nowText()))
	if err == nil {
		return device, true, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.MlsDevice{}, false, fmt.Errorf("register mls device: %w", mlsMissingReference(err))
	}

	device, err = scanMlsDevice(s.db.QueryRowContext(ctx,
		`SELECT id, user_id, signature_public_key, created_at FROM mls_devices
		 WHERE user_id = ? AND signature_public_key = ?`,
		userID, signatureKey))
	if err != nil {
		return storage.MlsDevice{}, false, fmt.Errorf("register mls device: %w", err)
	}
	return device, false, nil
}

// ReplaceMlsKeyPackages replaces one device's whole pool of unclaimed key
// packages and reports how many it now holds.
//
// Replace-all in one transaction, because a key package embeds an expiry the
// server cannot read: rather than the server guessing at staleness, the
// client publishes a fresh batch and the previous unclaimed pool goes with
// it. Claimed packages are already gone — claims are consuming — so nothing
// here can retract a package somebody is mid-add with.
//
// A device that is not this user's answers ErrMlsDeviceNotFound, and so does
// one that does not exist: the ownership check is part of the same
// transaction as the write, so no concurrent registration can widen it. That
// is stronger here than it needs to be — a concurrent registration cannot run
// during this transaction at all — and it is kept because the property the
// caller relies on is the transaction, not the engine underneath it.
func (s *Store) ReplaceMlsKeyPackages(ctx context.Context, userID, deviceID uuid.UUID, packages [][]byte) (int, error) {
	var count int
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var owned bool
		err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM mls_devices WHERE id = ? AND user_id = ?)`,
			deviceID, userID).Scan(&owned)
		if err != nil {
			return fmt.Errorf("check device ownership: %w", err)
		}
		if !owned {
			return storage.ErrMlsDeviceNotFound
		}

		if _, clearErr := tx.ExecContext(ctx,
			`DELETE FROM mls_key_packages WHERE device_id = ?`, deviceID); clearErr != nil {
			return fmt.Errorf("clear key package pool: %w", clearErr)
		}

		// The PostgreSQL driver inserts the whole batch in one statement over
		// unnest($2::bytea[]). SQLite has no array type, so this is the same
		// loop of single-row statements every set-valued write in this file
		// uses: the transaction's write lock is held across all of them, which
		// makes the loop as atomic as the single statement was, and a pool a
		// client publishes on connect is tens of packages — not a size at
		// which the extra round trips inside one open transaction are worth a
		// JSON encoding to avoid.
		now := s.nowText()
		for _, pkg := range packages {
			if _, insErr := tx.ExecContext(ctx,
				`INSERT INTO mls_key_packages (id, device_id, data, created_at) VALUES (?, ?, ?, ?)`,
				uuid.New(), deviceID, pkg, now,
			); insErr != nil {
				return fmt.Errorf("store key packages: %w", insErr)
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("replace mls key packages: %w", err)
	}
	return count, nil
}

// MlsGroupByChannel returns the channel's group, or ErrMlsGroupNotFound.
//
// It applies no visibility check: who may read a channel's group is the authz
// layer's question, decided from the same membership every other
// channel-scoped path is decided from.
func (s *Store) MlsGroupByChannel(ctx context.Context, channelID uuid.UUID) (storage.MlsGroup, error) {
	group, err := scanMlsGroup(s.db.QueryRowContext(ctx,
		`SELECT channel_id, group_id, epoch, created_at FROM mls_groups WHERE channel_id = ?`,
		channelID))
	if err != nil {
		return storage.MlsGroup{}, fmt.Errorf("mls group by channel: %w", err)
	}
	return group, nil
}

// CreateMlsGroup registers the channel's group at epoch 0.
//
// Exactly one group per channel, enforced by the primary key rather than by a
// lock: two clients racing to create both insert, one wins, and the loser's
// unique violation becomes ErrMlsGroupExists. There is no read-then-write
// window here to lose, and that is true on both drivers for the same reason —
// the arbiter is an index, not a lock, so nothing about it changes when the
// engine underneath serializes writers by itself.
//
// A group id already taken by another channel is the same answer. It is a
// different index, but it is the same fact from the caller's side — the group
// it asked to register already exists — and the contract has one 409 for it.
func (s *Store) CreateMlsGroup(ctx context.Context, channelID uuid.UUID, groupID []byte) (storage.MlsGroup, error) {
	group, err := scanMlsGroup(s.db.QueryRowContext(ctx,
		`INSERT INTO mls_groups (channel_id, group_id, created_at) VALUES (?, ?, ?)
		 RETURNING channel_id, group_id, epoch, created_at`,
		channelID, groupID, s.nowText()))
	if err != nil {
		return storage.MlsGroup{}, fmt.Errorf("create mls group: %w", mapMlsGroupConflict(err))
	}
	return group, nil
}

// ClaimMlsKeyPackages consumes one key package per device of a member, so
// that member can be added to the channel's group.
//
// The read is consuming: every returned package is deleted in the same
// transaction that returned it (mlsClaimKeyPackageQuery), because a key
// package is single-use by protocol and handing one to two adders is the bug
// this method exists to make impossible.
//
// The target must be a member of this channel, checked inside the same
// transaction as the consumption so a claim cannot outlive the membership
// that justified it; a non-member answers ErrNotFound, which is the
// contract's member_not_found. Who may ASK is the authz layer's question and
// is not decided here.
//
// A device whose pool is empty appears in missing rather than failing the
// call: it cannot be added until it replenishes, and the other devices of the
// same person still can. Both lists empty means the target has no MLS devices
// at all.
func (s *Store) ClaimMlsKeyPackages(ctx context.Context, channelID, targetUserID uuid.UUID) ([]storage.MlsKeyPackageClaim, []uuid.UUID, error) {
	claims := []storage.MlsKeyPackageClaim{}
	missing := []uuid.UUID{}

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var member bool
		err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id = ? AND user_id = ?)`,
			channelID, targetUserID).Scan(&member)
		if err != nil {
			return fmt.Errorf("check target membership: %w", err)
		}
		if !member {
			return storage.ErrNotFound
		}

		deviceIDs, err := mlsDeviceIDs(ctx, tx, targetUserID)
		if err != nil {
			return err
		}

		// One statement per device rather than one for the whole set: a
		// person holds a handful of devices, and the per-device statement is
		// what lets an empty pool be reported as missing instead of failing
		// the claim for everybody else.
		for _, deviceID := range deviceIDs {
			var data []byte
			scanErr := tx.QueryRowContext(ctx, mlsClaimKeyPackageQuery, deviceID).Scan(&data)
			if errors.Is(scanErr, sql.ErrNoRows) {
				missing = append(missing, deviceID)
				continue
			}
			if scanErr != nil {
				return fmt.Errorf("claim key package: %w", scanErr)
			}
			claims = append(claims, storage.MlsKeyPackageClaim{DeviceID: deviceID, KeyPackage: data})
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("claim mls key packages: %w", err)
	}
	return claims, missing, nil
}

// ListMlsMemberDevices returns one page of the channel's current members with
// their device signature keys, ordered by user id.
//
// This is the allow-list an eviction sweep is built from (ADR 007): after
// reconciliation, every leaf whose signature key these pages do not map to a
// current member has been evicted. A client must therefore read EVERY page
// before sweeping — an allow-list built from half the roster evicts the half
// it never read — which is why after is a position in a total order rather
// than a filter.
//
// A member with no devices is returned with an empty key list rather than
// omitted. In the PostgreSQL driver the LEFT JOIN plus array_remove makes that
// structural; here the same LEFT JOIN yields one row with a NULL key, and the
// grouping below turns it into a member with an empty list. Neither the join
// nor the NULL row may be optimized away.
//
// It applies no visibility check: who may read this is the authz layer's
// question, decided from the same membership every other channel-scoped path
// is decided from.
func (s *Store) ListMlsMemberDevices(ctx context.Context, channelID uuid.UUID, after *uuid.UUID, limit int) ([]storage.MlsMemberDevice, error) {
	cursor := uuid.NullUUID{}
	if after != nil {
		cursor = uuid.NullUUID{UUID: *after, Valid: true}
	}

	rows, err := s.db.QueryContext(ctx, mlsMemberDevicesQuery, channelID, cursor, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list mls member devices: %w", err)
	}
	defer rows.Close()

	// The result is flat and ordered by user id, so a member's rows are
	// adjacent and the grouping is a single pass: a new user id opens a member
	// with an empty key list, and every non-NULL key appends to whichever
	// member is open.
	members := []storage.MlsMemberDevice{}
	for rows.Next() {
		var userID uuid.UUID
		var key []byte
		if scanErr := rows.Scan(&userID, &key); scanErr != nil {
			return nil, fmt.Errorf("list mls member devices: %w", scanErr)
		}
		if len(members) == 0 || members[len(members)-1].UserID != userID {
			members = append(members, storage.MlsMemberDevice{UserID: userID, SignaturePublicKeys: [][]byte{}})
		}
		if key != nil {
			current := &members[len(members)-1]
			current.SignaturePublicKeys = append(current.SignaturePublicKeys, key)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mls member devices: %w", err)
	}
	return members, nil
}

// SubmitMlsCommit accepts a commit if the epoch it was built at is still the
// group's current one, and stores its Welcomes with it.
//
// The compare-and-swap (mlsAdvanceEpochQuery) is the sequencing point: of two
// concurrent committers exactly one advances the group, and the other gets
// ErrMlsEpochConflict. The commit row's own primary key (channel_id, epoch)
// makes accepting two commits at one epoch impossible even if the CAS were
// somehow bypassed, so first-wins is structural rather than procedural.
//
// The Welcomes go in the SAME transaction as the CAS and the commit row. That
// is group integrity, not tidiness: a committed add whose Welcome was lost is
// a group the new member can never join and the old members believe they
// added. Any failure among them — a device id naming nothing included —
// rolls the epoch back with it, so the group is never left one epoch ahead of
// a commit nobody can apply. That is why the per-recipient fan-out loop is
// inside this transaction and not around it.
//
// The blob is never parsed. A garbage commit that wins an epoch is rejected
// by every member's own MLS validation, and the group repairs by committing
// past it.
func (s *Store) SubmitMlsCommit(ctx context.Context, nc storage.NewMlsCommit) (storage.MlsCommitOutcome, error) {
	var out storage.MlsCommitOutcome
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, mlsAdvanceEpochQuery, nc.ChannelID, nc.Epoch)
		if err != nil {
			return fmt.Errorf("advance epoch: %w", err)
		}
		advanced, err := rowsAffected(res)
		if err != nil {
			return fmt.Errorf("advance epoch: %w", err)
		}
		if advanced == 0 {
			return mlsCASFailure(ctx, tx, nc.ChannelID)
		}
		// The CAS matched on epoch = nc.Epoch and set epoch = epoch + 1, so
		// the row now carries exactly this. The PostgreSQL driver reads the
		// same number back with RETURNING; both are the value the row holds.
		newEpoch := nc.Epoch + 1

		now := s.nowText()
		if _, logErr := tx.ExecContext(ctx,
			`INSERT INTO mls_commits (channel_id, epoch, message, created_at) VALUES (?, ?, ?, ?)`,
			nc.ChannelID, newEpoch, nc.Message, now,
		); logErr != nil {
			return fmt.Errorf("store commit: %w", logErr)
		}

		out.Epoch = newEpoch
		if len(nc.Welcomes) == 0 {
			out.WelcomeUserIDs = []uuid.UUID{}
			return nil
		}

		var recipients []uuid.UUID
		for _, w := range nc.Welcomes {
			recipients = append(recipients, w.DeviceIDs...)
		}

		// Membership is checked before anything is written, inside the
		// commit's own transaction, so a refused Welcome leaves neither a row
		// nor an advanced epoch behind.
		if len(recipients) > 0 {
			var outsiders int
			if checkErr := tx.QueryRowContext(ctx,
				mlsWelcomeOutsidersQuery(len(recipients)),
				mlsIDArgs([]any{nc.ChannelID}, recipients)...,
			).Scan(&outsiders); checkErr != nil {
				return fmt.Errorf("check welcome recipients: %w", checkErr)
			}
			if outsiders > 0 {
				return storage.ErrMlsWelcomeOutsider
			}
		}

		// One statement per recipient device, where the PostgreSQL driver
		// needs one per Welcome (mlsInsertWelcomeQuery says why). The contract
		// caps a commit at eight Welcomes and a household's device count is
		// small, so this loop is a few dozen statements at worst — inside one
		// already-open write transaction, which is where the cost of a
		// statement is a bind and a step rather than a round trip.
		for _, w := range nc.Welcomes {
			for _, deviceID := range w.DeviceIDs {
				if _, insErr := tx.ExecContext(ctx, mlsInsertWelcomeQuery,
					uuid.New(), nc.ChannelID, deviceID, w.Welcome, now,
				); insErr != nil {
					return mapMlsWelcomeConflict(insErr)
				}
			}
		}

		out.WelcomeUserIDs, err = mlsDeviceOwners(ctx, tx, recipients)
		return err
	})
	if err != nil {
		return storage.MlsCommitOutcome{}, fmt.Errorf("submit mls commit: %w", err)
	}
	return out, nil
}

// mlsCASFailure names why the compare-and-swap matched nothing: the channel
// has no group at all, or the epoch has moved on. The two are separate
// answers because they call for different things from a client — create the
// group, or refetch the log and rebuild.
func mlsCASFailure(ctx context.Context, q querier, channelID uuid.UUID) error {
	var exists bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM mls_groups WHERE channel_id = ?)`, channelID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check group exists: %w", err)
	}
	if !exists {
		return storage.ErrMlsGroupNotFound
	}
	return storage.ErrMlsEpochConflict
}

// ListMlsCommits returns the commit log after an epoch, ascending — how a
// device that has been offline catches up. The log is durable, unlike the
// WebSocket replay buffer, because a device offline for a week must still be
// able to advance from its epoch to the current one.
//
// A channel with no group has no commits and is not an error: the log is a
// read, and whether a group exists is what MlsGroupByChannel answers.
func (s *Store) ListMlsCommits(ctx context.Context, channelID uuid.UUID, afterEpoch int64, limit int) ([]storage.MlsCommit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT epoch, message, created_at FROM mls_commits
		 WHERE channel_id = ? AND epoch > ?
		 ORDER BY epoch
		 LIMIT ?`,
		channelID, afterEpoch, limit)
	if err != nil {
		return nil, fmt.Errorf("list mls commits: %w", err)
	}
	defer rows.Close()

	commits := []storage.MlsCommit{}
	for rows.Next() {
		var c storage.MlsCommit
		if scanErr := rows.Scan(&c.Epoch, &c.Message, timeScan{dst: &c.CreatedAt}); scanErr != nil {
			return nil, fmt.Errorf("list mls commits: %w", scanErr)
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mls commits: %w", err)
	}
	return commits, nil
}

// ListMlsWelcomes returns every Welcome waiting for any of one user's
// devices, oldest first.
//
// All of a user's devices, on any of their sockets: a Welcome is encrypted to
// one device's key package, so a sibling device receives bytes it cannot
// open. Rows survive until they are acknowledged rather than being deleted on
// read, because a client that fetches and then crashes before joining must
// find its Welcome still there.
func (s *Store) ListMlsWelcomes(ctx context.Context, userID uuid.UUID) ([]storage.MlsWelcome, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mlsWelcomeColumns+`
		 FROM mls_welcomes w
		 JOIN mls_devices d ON d.id = w.recipient_device_id
		 JOIN mls_groups g ON g.channel_id = w.channel_id
		 WHERE d.user_id = ?
		 ORDER BY w.created_at, w.id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list mls welcomes: %w", err)
	}
	defer rows.Close()

	welcomes := []storage.MlsWelcome{}
	for rows.Next() {
		w, scanErr := scanMlsWelcome(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list mls welcomes: %w", scanErr)
		}
		welcomes = append(welcomes, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mls welcomes: %w", err)
	}
	return welcomes, nil
}

// DeleteMlsWelcome acknowledges a Welcome after the caller's device joined.
//
// The user id is part of the WHERE clause, not a check made before it, and
// that single fact carries the whole security property: an id naming somebody
// else's Welcome matches nothing, so it can neither remove foreign state nor
// be told apart from an id naming nothing at all. Success either way — how
// many rows it touched is not something this reports, because reporting it is
// exactly the distinguisher the uniform 204 exists to remove.
//
// It is idempotent for free, for the same reason: an already-acknowledged
// Welcome is a row that is no longer there.
//
// The ownership join is a correlated EXISTS where the PostgreSQL driver writes
// DELETE ... USING, which SQLite does not have. It is the same join and, more
// to the point, the same placement: the user id stays inside the WHERE clause
// rather than becoming a lookup made before it.
func (s *Store) DeleteMlsWelcome(ctx context.Context, userID, welcomeID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM mls_welcomes
		 WHERE id = ? AND EXISTS (
		     SELECT 1 FROM mls_devices d
		     WHERE d.id = mls_welcomes.recipient_device_id AND d.user_id = ?
		 )`,
		welcomeID, userID)
	if err != nil {
		return fmt.Errorf("acknowledge mls welcome: %w", err)
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
// never acknowledge. SQLite defaults foreign keys — and therefore those
// cascades — OFF per connection; the DSN turns them on (sqlitestore.go), which
// is what makes this paragraph true here and not merely inherited.
func (s *Store) DeregisterMlsDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM mls_devices WHERE id = ? AND user_id = ?`, deviceID, userID)
	if err != nil {
		return fmt.Errorf("deregister mls device: %w", err)
	}
	deleted, err := rowsAffected(res)
	if err != nil {
		return fmt.Errorf("deregister mls device: %w", err)
	}
	if deleted == 0 {
		return storage.ErrMlsDeviceNotFound
	}
	return nil
}

// mlsDeviceIDs lists one user's devices, oldest registration first.
func mlsDeviceIDs(ctx context.Context, q querier, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM mls_devices WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list mls devices: %w", err)
	}
	defer rows.Close()

	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("list mls devices: %w", scanErr)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mls devices: %w", err)
	}
	return ids, nil
}

// mlsDeviceOwners returns the distinct users behind a set of device ids —
// who to nudge about a Welcome. It runs inside the commit's transaction, so
// the ids it resolves are the ones the insert just accepted.
func mlsDeviceOwners(ctx context.Context, q querier, deviceIDs []uuid.UUID) ([]uuid.UUID, error) {
	owners := []uuid.UUID{}
	if len(deviceIDs) == 0 {
		return owners, nil
	}

	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM mls_devices WHERE id IN (`+mlsPlaceholders(len(deviceIDs))+`)`,
		mlsIDArgs(nil, deviceIDs)...)
	if err != nil {
		return nil, fmt.Errorf("resolve welcome recipients: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("resolve welcome recipients: %w", scanErr)
		}
		owners = append(owners, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve welcome recipients: %w", err)
	}
	return owners, nil
}

// mlsIDArgs appends ids to args as bind parameters, for the IN lists
// mlsPlaceholders renders.
//
// PostgreSQL passes a whole set as one uuid[] parameter (= ANY, unnest).
// SQLite has no array type, so this file splits the difference by kind: a
// set-valued READ becomes an IN list built at call time, which stays one
// statement and is the shape the house dialect prefers for small sets, and a
// set-valued WRITE becomes a loop of single-row statements under the
// transaction's write lock, which is what keeps a per-row failure attributable
// to its row. Every caller guards against an empty set, so the placeholder
// list is never empty.
func mlsIDArgs(args []any, ids []uuid.UUID) []any {
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

// mlsPlaceholders renders n comma-separated bind placeholders.
func mlsPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// scanMlsDevice scans one device row. sql.ErrNoRows becomes ErrNotFound.
func scanMlsDevice(row rowScanner) (storage.MlsDevice, error) {
	var d storage.MlsDevice
	err := row.Scan(&d.ID, &d.UserID, &d.SignaturePublicKey, timeScan{dst: &d.CreatedAt})
	if err != nil {
		return storage.MlsDevice{}, notFound(err)
	}
	return d, nil
}

// scanMlsGroup scans one group row. sql.ErrNoRows becomes
// ErrMlsGroupNotFound.
func scanMlsGroup(row rowScanner) (storage.MlsGroup, error) {
	var g storage.MlsGroup
	err := row.Scan(&g.ChannelID, &g.GroupID, &g.Epoch, timeScan{dst: &g.CreatedAt})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.MlsGroup{}, storage.ErrMlsGroupNotFound
	}
	if err != nil {
		return storage.MlsGroup{}, err
	}
	return g, nil
}

// scanMlsWelcome scans one mlsWelcomeColumns row. Its only caller iterates a
// result set, so there is no no-rows case to translate: an empty list is a
// user with nothing waiting, not an error.
func scanMlsWelcome(row rowScanner) (storage.MlsWelcome, error) {
	var w storage.MlsWelcome
	if err := row.Scan(&w.ID, &w.ChannelID, &w.GroupID, &w.DeviceID, &w.Welcome, timeScan{dst: &w.CreatedAt}); err != nil {
		return storage.MlsWelcome{}, err
	}
	return w, nil
}

// mapMlsGroupConflict translates the two unique indexes that settle the
// create race into ErrMlsGroupExists, and a missing channel into
// ErrChannelNotFound.
//
// The PostgreSQL driver reads pgerrcode; here the classification comes from
// SQLite's extended result codes (errors.go). Both unique indexes are one
// answer, so there is nothing the column list would add — which is why this
// checks the kind of violation and not which index carried it.
func mapMlsGroupConflict(err error) error {
	switch {
	case isUniqueViolation(err):
		return storage.ErrMlsGroupExists
	case isForeignKeyViolation(err):
		return storage.ErrChannelNotFound
	default:
		return err
	}
}

// mapMlsWelcomeConflict translates a Welcome addressed to a device that does
// not exist. The channel foreign key cannot fail here — the commit's own CAS
// already found the group row this transaction is holding — so a violated key
// on this insert always names the device.
func mapMlsWelcomeConflict(err error) error {
	if isForeignKeyViolation(err) {
		return storage.ErrMlsDeviceNotFound
	}
	return fmt.Errorf("store welcomes: %w", err)
}

// mlsMissingReference turns a foreign-key violation into ErrNotFound — the
// counterpart of storage.mapMissingReference, which reads the PostgreSQL error
// code for the same fact. A device registered for a user id that names nobody
// is that user not existing, not a constraint the caller could act on.
func mlsMissingReference(err error) error {
	if isForeignKeyViolation(err) {
		return storage.ErrNotFound
	}
	return err
}
