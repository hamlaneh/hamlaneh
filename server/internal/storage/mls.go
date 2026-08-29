package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The MLS transport tables (migration 0017, ADR 006). Every []byte below is
// an opaque MLS artifact: this package stores, sequences and hands it back,
// and no query in this file looks inside one. That is a property of the SQL
// rather than a promise — there is no expression anywhere here that reads a
// byte of data, message, welcome, group_id or signature_public_key, so the
// server cannot start parsing MLS without a new query being written.
//
// # Lock order
//
// The MLS tables extend the package-wide order (see storage.go) at the end:
//
//	… → channels → channel_members → messages → attachments →
//	mls_devices → mls_key_packages → mls_groups → mls_commits → mls_welcomes
//
// Two operations here take locks, and neither can close a cycle with the
// other.
//
//   - ClaimMlsKeyPackages locks rows of mls_key_packages and nothing else.
//     A transaction locking one table cannot deadlock, and the lock is taken
//     with SKIP LOCKED, so a concurrent claimer never even waits.
//   - SubmitMlsCommit takes the group row first (the UPDATE takes FOR NO KEY
//     UPDATE implicitly), then inserts into mls_commits and mls_welcomes,
//     which is the declared order. The inserts take KEY SHARE on the rows
//     their foreign keys reference — the group row this transaction already
//     holds, and the device rows a claim never locks — so nothing it wants
//     is held by anything that wants what it holds.

// Sentinel errors for the MLS transport. ErrNotFound keeps its package-wide
// meaning ("no such user") on this surface too: ClaimMlsKeyPackages answers
// it for a target who is not a member of the channel, which is the contract's
// member_not_found.
var (
	// ErrMlsGroupNotFound reports that the channel has no MLS group yet. It
	// is the signal a client turns into a create.
	ErrMlsGroupNotFound = errors.New("storage: mls group not found")
	// ErrMlsGroupExists reports the create race's loser: a group already
	// exists for this channel, or the group id is already taken instance-wide.
	// Both are settled by a unique index rather than by a read-then-write
	// window, which is why there is no lock anywhere in CreateMlsGroup.
	ErrMlsGroupExists = errors.New("storage: mls group already exists")
	// ErrMlsEpochConflict reports that the epoch a commit was built at is no
	// longer the group's current one: another commit won this epoch.
	ErrMlsEpochConflict = errors.New("storage: mls epoch is no longer current")
	// ErrMlsDeviceNotFound reports a device id that names no device the
	// caller may act on — another user's, or none at all. The two are one
	// answer on purpose.
	ErrMlsDeviceNotFound = errors.New("storage: mls device not found")
	// ErrMlsWelcomeNotFound reports a welcome that exists and belongs to
	// somebody else. A welcome that is simply gone is not an error:
	// acknowledgement is idempotent.
	ErrMlsWelcomeNotFound = errors.New("storage: mls welcome not found")
)

// MlsDevice is one client instance's leaf identity. A leaf is a device, not a
// user (migration 0017): the (user_id, signature_public_key) pair is
// registration's idempotency rule.
type MlsDevice struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	SignaturePublicKey []byte
	CreatedAt          time.Time
}

// MlsGroup is the channel's group registration. Epoch counts accepted commits
// and asserts nothing cryptographic — it is the sequencing claim the commit
// compare-and-swap advances.
type MlsGroup struct {
	ChannelID uuid.UUID
	GroupID   []byte
	Epoch     int64
	CreatedAt time.Time
}

// MlsCommit is one row of the durable commit log. Epoch is the epoch the
// group reached by accepting this commit, so a client holding state at epoch
// N asks for everything after N and applies it in order.
type MlsCommit struct {
	Epoch     int64
	Message   []byte
	CreatedAt time.Time
}

// MlsWelcome is a Welcome waiting for one device, with the group it admits
// that device to. GroupID comes from the group row rather than from the
// welcome, which the server cannot read.
type MlsWelcome struct {
	ID        uuid.UUID
	ChannelID uuid.UUID
	GroupID   []byte
	DeviceID  uuid.UUID
	Welcome   []byte
	CreatedAt time.Time
}

// MlsKeyPackageClaim is one consumed key package and the device it addresses.
type MlsKeyPackageClaim struct {
	DeviceID   uuid.UUID
	KeyPackage []byte
}

// MlsWelcomeDelivery is one Welcome a commit carries, addressed to the device
// whose key package it was built against.
type MlsWelcomeDelivery struct {
	DeviceID uuid.UUID
	Welcome  []byte
}

// NewMlsCommit carries one commit submission. Epoch is what the commit was
// built at — the compare-and-swap expectation, not the epoch the row will
// carry.
//
// There is no sender device here because the contract's request carries none:
// mls_commits.sender_device_id and mls_groups.creator_device_id stay NULL
// until a contract change gives a client a way to name one. Filling either
// from anything else would be the server inventing an attribution.
type NewMlsCommit struct {
	ChannelID uuid.UUID
	Epoch     int64
	Message   []byte
	Welcomes  []MlsWelcomeDelivery
}

// MlsCommitOutcome is what an accepted commit produced: the group's new
// epoch, and the users whose devices now have a Welcome waiting.
type MlsCommitOutcome struct {
	Epoch int64
	// WelcomeUserIDs are the owners of the devices this commit addressed,
	// deduplicated. They are who the mls_welcome nudge goes to; the event
	// carries no payload, so naming a user reveals only that they have
	// something to fetch.
	WelcomeUserIDs []uuid.UUID
}

// claimKeyPackageQuery consumes one key package of one device, or returns no
// row when the pool is empty.
//
// The delete and the pick are one statement, so a package can never be handed
// to two adders: the row is gone in the same transaction that returned it.
// SKIP LOCKED is what makes a concurrent claimer report an empty pool instead
// of waiting for a row it is about to be denied anyway — two claimers racing
// for one package end with one claim and one missing_device_id, never two
// claims and never a deadlock.
//
// Oldest first, because a key package embeds an expiry the server cannot
// read: spending the oldest first is the only ordering that does not
// systematically strand the packages closest to expiring.
const claimKeyPackageQuery = `DELETE FROM mls_key_packages
	WHERE id = (
	    SELECT id FROM mls_key_packages
	    WHERE device_id = $1
	    ORDER BY created_at, id
	    FOR UPDATE SKIP LOCKED
	    LIMIT 1
	)
	RETURNING data`

// advanceEpochQuery is the compare-and-swap the whole design turns on
// (ADR 006). It advances the group by exactly one epoch, and only while the
// epoch the committer named is still current.
//
// Of two concurrent committers, the second blocks on the row until the first
// commits and then re-evaluates its predicate against the updated row — READ
// COMMITTED behaviour, which this statement depends on and which is the
// server default. The loser matches nothing, gets no row back, and is told to
// refetch. Nothing here reads the commit blob; the epoch is a claim the
// client makes in the envelope, and the server uses it for first-wins
// rejection and for nothing else.
const advanceEpochQuery = `UPDATE mls_groups SET epoch = epoch + 1
	WHERE channel_id = $1 AND epoch = $2
	RETURNING epoch`

// insertWelcomesQuery stores every Welcome of one commit in one statement.
//
// It inserts through unnest rather than joining mls_devices, so a device id
// naming nothing violates the foreign key instead of being silently dropped.
// A committed add whose Welcome vanished is a forked group; failing loudly
// inside the commit's transaction is what keeps that impossible.
const insertWelcomesQuery = `INSERT INTO mls_welcomes (channel_id, recipient_device_id, welcome)
	SELECT $1, w.device_id, w.welcome
	FROM unnest($2::uuid[], $3::bytea[]) AS w(device_id, welcome)`

// welcomeColumns is the canonical column list of a pending Welcome, in the
// order scanMlsWelcome expects.
const welcomeColumns = `w.id, w.channel_id, g.group_id, w.recipient_device_id, w.welcome, w.created_at`

// RegisterMlsDevice records one client instance's signature key, or returns
// the registration that already exists. created reports which happened — the
// contract's 201 and its 200.
//
// Idempotent on (user, signature_public_key), so a client may call it on
// every startup with no bookkeeping. Two startups racing serialize on the
// unique index rather than on any lock: ON CONFLICT DO NOTHING waits for the
// concurrent inserter to finish, and the follow-up read (a new statement, so
// a new READ COMMITTED snapshot) sees the row it committed.
//
// The key is stored as an opaque identifier. Nothing in this package ever
// verifies a signature with it, and a client that registers garbage only
// makes itself unaddable.
func (s *Store) RegisterMlsDevice(ctx context.Context, userID uuid.UUID, signatureKey []byte) (MlsDevice, bool, error) {
	device, err := scanMlsDevice(s.pool.QueryRow(ctx,
		`INSERT INTO mls_devices (user_id, signature_public_key) VALUES ($1, $2)
		 ON CONFLICT (user_id, signature_public_key) DO NOTHING
		 RETURNING id, user_id, signature_public_key, created_at`,
		userID, signatureKey))
	if err == nil {
		return device, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return MlsDevice{}, false, fmt.Errorf("register mls device: %w", mapMissingReference(err))
	}

	device, err = scanMlsDevice(s.pool.QueryRow(ctx,
		`SELECT id, user_id, signature_public_key, created_at FROM mls_devices
		 WHERE user_id = $1 AND signature_public_key = $2`,
		userID, signatureKey))
	if err != nil {
		return MlsDevice{}, false, fmt.Errorf("register mls device: %w", err)
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
// transaction as the write, so no concurrent registration can widen it.
func (s *Store) ReplaceMlsKeyPackages(ctx context.Context, userID, deviceID uuid.UUID, packages [][]byte) (int, error) {
	var count int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var owned bool
		err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM mls_devices WHERE id = $1 AND user_id = $2)`,
			deviceID, userID).Scan(&owned)
		if err != nil {
			return fmt.Errorf("check device ownership: %w", err)
		}
		if !owned {
			return ErrMlsDeviceNotFound
		}

		if _, clearErr := tx.Exec(ctx,
			`DELETE FROM mls_key_packages WHERE device_id = $1`, deviceID); clearErr != nil {
			return fmt.Errorf("clear key package pool: %w", clearErr)
		}
		tag, err := tx.Exec(ctx,
			`INSERT INTO mls_key_packages (device_id, data)
			 SELECT $1, kp FROM unnest($2::bytea[]) AS kp`,
			deviceID, packages)
		if err != nil {
			return fmt.Errorf("store key packages: %w", err)
		}
		count = int(tag.RowsAffected())
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
func (s *Store) MlsGroupByChannel(ctx context.Context, channelID uuid.UUID) (MlsGroup, error) {
	group, err := scanMlsGroup(s.pool.QueryRow(ctx,
		`SELECT channel_id, group_id, epoch, created_at FROM mls_groups WHERE channel_id = $1`,
		channelID))
	if err != nil {
		return MlsGroup{}, fmt.Errorf("mls group by channel: %w", err)
	}
	return group, nil
}

// CreateMlsGroup registers the channel's group at epoch 0.
//
// Exactly one group per channel, enforced by the primary key rather than by a
// lock: two clients racing to create both insert, one wins, and the loser's
// unique violation becomes ErrMlsGroupExists. There is no read-then-write
// window here to lose.
//
// A group id already taken by another channel is the same answer. It is a
// different index, but it is the same fact from the caller's side — the group
// it asked to register already exists — and the contract has one 409 for it.
func (s *Store) CreateMlsGroup(ctx context.Context, channelID uuid.UUID, groupID []byte) (MlsGroup, error) {
	group, err := scanMlsGroup(s.pool.QueryRow(ctx,
		`INSERT INTO mls_groups (channel_id, group_id) VALUES ($1, $2)
		 RETURNING channel_id, group_id, epoch, created_at`,
		channelID, groupID))
	if err != nil {
		return MlsGroup{}, fmt.Errorf("create mls group: %w", mapMlsGroupConflict(err))
	}
	return group, nil
}

// ClaimMlsKeyPackages consumes one key package per device of a member, so
// that member can be added to the channel's group.
//
// The read is consuming: every returned package is deleted in the same
// transaction that returned it (claimKeyPackageQuery), because a key package
// is single-use by protocol and handing one to two adders is the bug this
// method exists to make impossible.
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
func (s *Store) ClaimMlsKeyPackages(ctx context.Context, channelID, targetUserID uuid.UUID) ([]MlsKeyPackageClaim, []uuid.UUID, error) {
	claims := []MlsKeyPackageClaim{}
	missing := []uuid.UUID{}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var member bool
		err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2)`,
			channelID, targetUserID).Scan(&member)
		if err != nil {
			return fmt.Errorf("check target membership: %w", err)
		}
		if !member {
			return ErrNotFound
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
			scanErr := tx.QueryRow(ctx, claimKeyPackageQuery, deviceID).Scan(&data)
			if errors.Is(scanErr, pgx.ErrNoRows) {
				missing = append(missing, deviceID)
				continue
			}
			if scanErr != nil {
				return fmt.Errorf("claim key package: %w", scanErr)
			}
			claims = append(claims, MlsKeyPackageClaim{DeviceID: deviceID, KeyPackage: data})
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("claim mls key packages: %w", err)
	}
	return claims, missing, nil
}

// SubmitMlsCommit accepts a commit if the epoch it was built at is still the
// group's current one, and stores its Welcomes with it.
//
// The compare-and-swap (advanceEpochQuery) is the sequencing point: of two
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
// a commit nobody can apply.
//
// The blob is never parsed. A garbage commit that wins an epoch is rejected
// by every member's own MLS validation, and the group repairs by committing
// past it.
func (s *Store) SubmitMlsCommit(ctx context.Context, nc NewMlsCommit) (MlsCommitOutcome, error) {
	var out MlsCommitOutcome
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var newEpoch int64
		err := tx.QueryRow(ctx, advanceEpochQuery, nc.ChannelID, nc.Epoch).Scan(&newEpoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return mlsCASFailure(ctx, tx, nc.ChannelID)
		}
		if err != nil {
			return fmt.Errorf("advance epoch: %w", err)
		}

		if _, logErr := tx.Exec(ctx,
			`INSERT INTO mls_commits (channel_id, epoch, message) VALUES ($1, $2, $3)`,
			nc.ChannelID, newEpoch, nc.Message,
		); logErr != nil {
			return fmt.Errorf("store commit: %w", logErr)
		}

		out.Epoch = newEpoch
		if len(nc.Welcomes) == 0 {
			out.WelcomeUserIDs = []uuid.UUID{}
			return nil
		}

		deviceIDs := make([]uuid.UUID, 0, len(nc.Welcomes))
		blobs := make([][]byte, 0, len(nc.Welcomes))
		for _, w := range nc.Welcomes {
			deviceIDs = append(deviceIDs, w.DeviceID)
			blobs = append(blobs, w.Welcome)
		}
		if _, insErr := tx.Exec(ctx, insertWelcomesQuery, nc.ChannelID, deviceIDs, blobs); insErr != nil {
			return mapMlsWelcomeConflict(insErr)
		}

		out.WelcomeUserIDs, err = mlsDeviceOwners(ctx, tx, deviceIDs)
		return err
	})
	if err != nil {
		return MlsCommitOutcome{}, fmt.Errorf("submit mls commit: %w", err)
	}
	return out, nil
}

// mlsCASFailure names why the compare-and-swap matched nothing: the channel
// has no group at all, or the epoch has moved on. The two are separate
// answers because they call for different things from a client — create the
// group, or refetch the log and rebuild.
func mlsCASFailure(ctx context.Context, tx pgx.Tx, channelID uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mls_groups WHERE channel_id = $1)`, channelID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check group exists: %w", err)
	}
	if !exists {
		return ErrMlsGroupNotFound
	}
	return ErrMlsEpochConflict
}

// ListMlsCommits returns the commit log after an epoch, ascending — how a
// device that has been offline catches up. The log is durable, unlike the
// WebSocket replay buffer, because a device offline for a week must still be
// able to advance from its epoch to the current one.
//
// A channel with no group has no commits and is not an error: the log is a
// read, and whether a group exists is what MlsGroupByChannel answers.
func (s *Store) ListMlsCommits(ctx context.Context, channelID uuid.UUID, afterEpoch int64, limit int) ([]MlsCommit, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT epoch, message, created_at FROM mls_commits
		 WHERE channel_id = $1 AND epoch > $2
		 ORDER BY epoch
		 LIMIT $3`,
		channelID, afterEpoch, limit)
	if err != nil {
		return nil, fmt.Errorf("list mls commits: %w", err)
	}
	defer rows.Close()

	commits := []MlsCommit{}
	for rows.Next() {
		var c MlsCommit
		if scanErr := rows.Scan(&c.Epoch, &c.Message, &c.CreatedAt); scanErr != nil {
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
func (s *Store) ListMlsWelcomes(ctx context.Context, userID uuid.UUID) ([]MlsWelcome, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+welcomeColumns+`
		 FROM mls_welcomes w
		 JOIN mls_devices d ON d.id = w.recipient_device_id
		 JOIN mls_groups g ON g.channel_id = w.channel_id
		 WHERE d.user_id = $1
		 ORDER BY w.created_at, w.id`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("list mls welcomes: %w", err)
	}
	defer rows.Close()

	welcomes := []MlsWelcome{}
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
// Idempotent: a Welcome that is already gone is success, because that is the
// state the caller asked for. A Welcome that exists and belongs to somebody
// else is ErrMlsWelcomeNotFound — the contract distinguishes the two, and the
// distinction costs one EXISTS on the id that just matched nothing.
func (s *Store) DeleteMlsWelcome(ctx context.Context, userID, welcomeID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM mls_welcomes w
		 USING mls_devices d
		 WHERE w.id = $1 AND d.id = w.recipient_device_id AND d.user_id = $2`,
		welcomeID, userID)
	if err != nil {
		return fmt.Errorf("acknowledge mls welcome: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	var others bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM mls_welcomes WHERE id = $1)`, welcomeID,
	).Scan(&others); err != nil {
		return fmt.Errorf("acknowledge mls welcome: %w", err)
	}
	if others {
		return fmt.Errorf("acknowledge mls welcome: %w", ErrMlsWelcomeNotFound)
	}
	return nil
}

// mlsDeviceIDs lists one user's devices, oldest registration first.
func mlsDeviceIDs(ctx context.Context, q pgx.Tx, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx,
		`SELECT id FROM mls_devices WHERE user_id = $1 ORDER BY created_at, id`, userID)
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
func mlsDeviceOwners(ctx context.Context, q pgx.Tx, deviceIDs []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx,
		`SELECT DISTINCT user_id FROM mls_devices WHERE id = ANY($1::uuid[])`, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve welcome recipients: %w", err)
	}
	defer rows.Close()

	owners := []uuid.UUID{}
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

// scanMlsDevice scans one device row. pgx.ErrNoRows becomes ErrNotFound.
func scanMlsDevice(row pgx.Row) (MlsDevice, error) {
	var d MlsDevice
	err := row.Scan(&d.ID, &d.UserID, &d.SignaturePublicKey, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MlsDevice{}, ErrNotFound
	}
	if err != nil {
		return MlsDevice{}, err
	}
	return d, nil
}

// scanMlsGroup scans one group row. pgx.ErrNoRows becomes
// ErrMlsGroupNotFound.
func scanMlsGroup(row pgx.Row) (MlsGroup, error) {
	var g MlsGroup
	err := row.Scan(&g.ChannelID, &g.GroupID, &g.Epoch, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MlsGroup{}, ErrMlsGroupNotFound
	}
	if err != nil {
		return MlsGroup{}, err
	}
	return g, nil
}

// scanMlsWelcome scans one welcomeColumns row.
func scanMlsWelcome(row pgx.Row) (MlsWelcome, error) {
	var w MlsWelcome
	err := row.Scan(&w.ID, &w.ChannelID, &w.GroupID, &w.DeviceID, &w.Welcome, &w.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MlsWelcome{}, ErrMlsWelcomeNotFound
	}
	if err != nil {
		return MlsWelcome{}, err
	}
	return w, nil
}

// mapMlsGroupConflict translates the two unique indexes that settle the
// create race into ErrMlsGroupExists, and a missing channel into
// ErrChannelNotFound.
func mapMlsGroupConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case pgerrcode.UniqueViolation:
		return ErrMlsGroupExists
	case pgerrcode.ForeignKeyViolation:
		return ErrChannelNotFound
	default:
		return err
	}
}

// mapMlsWelcomeConflict translates a Welcome addressed to a device that does
// not exist. The channel foreign key cannot fail here — the commit's own CAS
// already found the group row this transaction is holding — so a violated key
// on this insert always names the device.
func mapMlsWelcomeConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return ErrMlsDeviceNotFound
	}
	return fmt.Errorf("store welcomes: %w", err)
}
