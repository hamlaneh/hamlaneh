package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// The canonical column lists every channel query selects, in the order
// scanChannel expects. Both require the channels table to be aliased c.
//
// The head and tail exist so the two lists cannot drift apart: they differ in
// exactly the six caller-scoped columns and nowhere else.
//
// The newest message is max(created_at) on TEXT, which is the chronological
// maximum because the timestamp encoding is fixed width and UTC (codec.go).
// It is NULL in an empty channel, which is what the domain type wants.
const (
	channelColumnsHead = `c.id, c.kind, c.slug, c.topic, c.dm_user_a, c.dm_user_b, c.created_by, c.e2ee,
	        (SELECT count(*) FROM channel_members mc WHERE mc.channel_id = c.id), `

	channelColumnsTail = `,
	        (SELECT max(msg.created_at) FROM messages msg WHERE msg.channel_id = c.id),
	        c.created_at, c.updated_at`
)

// channelUnreadPredicate is what "unread for the caller" means: the messages
// after the caller's read position, less the deleted ones and less the
// caller's own. It is one string used twice — by the unread count and by the
// mention count — because those two must count exactly the same rows, and
// sharing the text is what makes that structural rather than a rule somebody
// has to remember when editing one of them.
//
// The bound is the caller's read position coalesced to (negInfinity, the
// smallest uuid) rather than guarded by an OR on NULL, exactly as the
// PostgreSQL driver does: a coalesced bound is a range the messages index can
// start at, an OR is a filter over the channel's whole history. negInfinity
// stands in for '-infinity'::timestamptz and sorts before every real
// timestamp because the layout is fixed width and its year is zero.
//
// PostgreSQL compares the row value (msg.created_at, msg.id) against the pair
// in one operator. SQLite has no row values, so the same comparison is
// written out: greater on the timestamp, or equal on it and greater on the id.
//
// It binds four parameters, in channelUnreadArgs' order.
const channelUnreadPredicate = `msg.channel_id = c.id
	           AND msg.deleted_at IS NULL
	           AND msg.author_id <> ?
	           AND (msg.created_at > coalesce(rp.last_read_at, ?)
	                OR (msg.created_at = coalesce(rp.last_read_at, ?)
	                    AND msg.id > coalesce(rp.last_read_message_id, ?)))`

// channelColumns is what a query selects when it knows who is asking. It
// requires the joins in callerJoins and the parameters channelSelectArgs
// builds.
//
// The two counts are correlated scalar subqueries, and this is the one place
// in this file where SQLite costs something real rather than only spelling
// things differently. PostgreSQL computes both in a single LEFT JOIN LATERAL
// pass — one range scan over the unread messages, the caller's mentions read
// once through message_mentions_user_id_idx, which measures ~40x fewer
// buffers than probing per message on a channel with a thousand unread.
// SQLite has no LATERAL, so each count is its own subquery evaluated once per
// row of the page. ADR 012 accepts that cost explicitly — a sidebar of dozens
// pays it and nobody notices — and this is the query where a home instance
// with a very large sidebar, or one channel with an enormous unread backlog,
// would feel the difference first.
//
// What the LATERAL also bought was that mention_count could not drift from
// unread_count: it counted the non-null side of a join over exactly the rows
// unread_count counted. Two subqueries cannot have that property by
// construction, so it is bought instead by both of them embedding
// channelUnreadPredicate — one string, one edit, no second WHERE clause to
// keep in step. The mention join is on the message_mentions primary key, so
// it can neither drop a row nor duplicate one, and the count stays a subset
// of the unread count.
const channelColumns = channelColumnsHead +
	`(SELECT count(*) FROM messages msg
	          WHERE ` + channelUnreadPredicate + `),
	        (SELECT count(*) FROM messages msg
	          JOIN message_mentions mn
	            ON mn.message_id = msg.id AND mn.mentioned_user_id = ?
	          WHERE ` + channelUnreadPredicate + `),
	        rp.last_read_message_id,
	        peer.id, peer.username, peer.display_name` +
	channelColumnsTail

// unscopedChannelColumns is what a query selects when there is no caller:
// literal zeros and nulls, never a computed value. See ChannelByID on why
// they are not an answer to anything. PostgreSQL has to cast its NULLs
// (NULL::uuid) to give the row a type; SQLite's columns have no declared type
// to satisfy, so they are bare.
const unscopedChannelColumns = channelColumnsHead +
	`0, 0, NULL,
	        NULL, NULL, NULL` +
	channelColumnsTail

// callerJoins resolve everything about a channel that depends on who is
// asking, for the two columns that are joins rather than subqueries. Every
// query selecting channelColumns must carry them after `FROM channels c`.
//
// The read position is a primary-key lookup, left-joined because a channel
// the caller has never opened has no row there; channelUnreadPredicate reads
// rp through this join.
//
// The peer is a join rather than a lookup per row for the reason the member
// count is a subquery: a sidebar with forty direct messages in it must cost
// one query, not forty-one. The join key is a CASE with no ELSE, so it is
// null — and the LEFT JOIN therefore finds nobody — in all three cases where
// there is no other participant to name: a named channel, whose dm columns
// are null; a direct message read by somebody in neither half of the pair;
// and any row where the caller is both halves, which the schema forbids.
// Matching on users' primary key makes it one index probe per direct message
// on the page.
//
// It binds three parameters, in channelJoinArgs' order.
const callerJoins = `LEFT JOIN channel_read_positions rp
	            ON rp.channel_id = c.id AND rp.user_id = ?
	     LEFT JOIN users peer
	            ON peer.id = CASE WHEN c.dm_user_a = ? THEN c.dm_user_b
	                              WHEN c.dm_user_b = ? THEN c.dm_user_a END`

// channelUnreadArgs are the four parameters channelUnreadPredicate binds, in
// the order its placeholders appear.
func channelUnreadArgs(userID uuid.UUID) []any {
	return []any{userID, negInfinity, negInfinity, uuid.Nil}
}

// channelSelectArgs are the parameters channelColumns binds, in the order its
// placeholders appear: the unread subquery, then the mentions subquery, whose
// own join parameter comes before its copy of the predicate.
//
// Placeholders here are positional `?`, so this order is the contract between
// the column list and every call site. It lives next to the SQL it serves for
// that reason, and no call site writes the list out by hand.
func channelSelectArgs(userID uuid.UUID) []any {
	args := channelUnreadArgs(userID)
	args = append(args, userID)
	return append(args, channelUnreadArgs(userID)...)
}

// channelJoinArgs are the parameters callerJoins binds: the read position's
// user, then the caller on both arms of the peer CASE.
func channelJoinArgs(userID uuid.UUID) []any {
	return []any{userID, userID, userID}
}

// channelCallerArgs are channelSelectArgs followed by channelJoinArgs — the
// full parameter list of a `SELECT channelColumns FROM channels c callerJoins`
// with nothing between the two, which is every caller-scoped read except the
// sidebar's (ListChannelsForUser puts its membership join in between).
func channelCallerArgs(userID uuid.UUID) []any {
	return append(channelSelectArgs(userID), channelJoinArgs(userID)...)
}

// CreateChannel inserts a public or private channel and makes its creator
// the first member, in one transaction: a channel nobody belongs to is
// unreachable, and the empty-channel screen promises the creator is already
// in it.
//
// A duplicate slug maps to storage.ErrChannelSlugTaken, a creator who does
// not exist to storage.ErrNotFound. Direct messages are refused outright —
// only OpenDirectMessage can produce a well-formed pair.
//
// The insert into channels comes before the one into channel_members, which
// is the table order the PostgreSQL driver's lock order fixes. Here nothing
// depends on it — one writer holds the database from BEGIN, so no second
// transaction is taking these rows in any order at all — but the two drivers
// are read side by side and there is no reason to make them differ.
func (s *Store) CreateChannel(ctx context.Context, nc storage.NewChannel) (storage.Channel, error) {
	if nc.Kind != storage.ChannelKindPublic && nc.Kind != storage.ChannelKindPrivate {
		return storage.Channel{}, fmt.Errorf("create channel: kind %q must be public or private", nc.Kind)
	}

	var ch storage.Channel
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// PostgreSQL takes the id from gen_random_uuid() and reads it back
		// with RETURNING. Nothing in the SQLite schema generates identifiers
		// (migration 0001's header), so it is generated here and the insert
		// needs no RETURNING at all.
		id := uuid.New()
		now := s.nowText()

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channels (id, kind, slug, topic, e2ee, created_by, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, string(nc.Kind), nc.Slug, nc.Topic, boolValue(nc.E2EE), nc.CreatedBy, now, now,
		); err != nil {
			return mapChannelConflict(err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channel_members (channel_id, user_id, added_by, joined_at) VALUES (?, ?, ?, ?)`,
			id, nc.CreatedBy, nc.CreatedBy, now,
		); err != nil {
			return fmt.Errorf("add creator as member: %w", mapMembershipConflict(err))
		}

		// Re-read rather than returning what was inserted: the member count is
		// only right once the creator's row exists.
		var err error
		ch, err = channelForUser(ctx, tx, id, nc.CreatedBy)
		return err
	})
	if err != nil {
		return storage.Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return ch, nil
}

// ChannelByID returns the channel with the given id, or
// storage.ErrChannelNotFound.
//
// It applies no visibility check at all. Callers serving a request must gate
// on membership — IsChannelMember, or the authz package — before handing any
// of it to a user; ListChannelsForUser is the scoped counterpart for lists.
//
// There is no caller here, so UnreadCount and MentionCount come back 0,
// LastReadMessageID nil and DMPeer nil — not because the channel is read and
// not because a direct message has nobody in it, but because "unread" and
// "the other one" are questions about a person and this function was not
// told one. Naming a peer here would mean picking a side of the pair at
// random, and being right half the time is worse than being silent.
// Anything that serves a channel to a user must read it through
// ChannelForUser, or it will render a zero nobody computed and a direct
// message labelled with whoever sorted first. LastMessageAt is filled: it
// needs no caller.
func (s *Store) ChannelByID(ctx context.Context, id uuid.UUID) (storage.Channel, error) {
	ch, err := scanChannel(s.db.QueryRowContext(ctx,
		`SELECT `+unscopedChannelColumns+` FROM channels c WHERE c.id = ?`, id))
	if err != nil {
		return storage.Channel{}, fmt.Errorf("channel by id: %w", err)
	}
	return ch, nil
}

// ChannelForUser returns one channel as one caller sees it: the same row
// ChannelByID returns, plus that caller's unread count, mention count and
// read position. It is the single-channel counterpart of
// ListChannelsForUser, and what a request handler serving a Channel to
// somebody should call.
//
// Like ChannelByID it applies no visibility check — userID says whose unread
// counts to compute, not who is allowed to look. Membership is still the
// authz layer's call, and userID must be the authenticated caller: passing an
// id from a request would hand back that person's read position, which no
// other user is ever entitled to see.
func (s *Store) ChannelForUser(ctx context.Context, channelID, userID uuid.UUID) (storage.Channel, error) {
	ch, err := channelForUser(ctx, s.db, channelID, userID)
	if err != nil {
		return storage.Channel{}, fmt.Errorf("channel for user: %w", err)
	}
	return ch, nil
}

// UpdateChannelTopic sets a channel's topic and returns the updated row. An
// empty topic clears it.
//
// The topic is the only mutable field in this slice: renaming a channel
// would move it under everyone's cursor mid-conversation and is not in the
// contract. A direct message has no topic to set (storage.ErrDMHasNoTopic),
// and an unknown channel is storage.ErrChannelNotFound.
//
// The returned Channel carries no caller-scoped fields, for the reason
// ChannelByID gives: an update statement is not told who ran it. A handler
// that answers with the whole channel should re-read it through
// ChannelForUser rather than serve this row's zeros. DMPeer is moot here in
// any case — a direct message has no topic, so this never returns one.
//
// PostgreSQL does the write and the read as one UPDATE ... RETURNING. Here
// they are separate statements in one transaction: the row this returns has
// to be the row this update wrote, and the second statement can only promise
// that if no other writer can land between them. Inside s.withTx none can —
// the transaction holds the database's write lock — so the pair is exactly as
// atomic as PostgreSQL's single statement.
func (s *Store) UpdateChannelTopic(ctx context.Context, id uuid.UUID, topic string) (storage.Channel, error) {
	var ch storage.Channel
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		kind, err := channelKind(ctx, tx, id)
		if err != nil {
			return err
		}
		if kind == storage.ChannelKindDM {
			return storage.ErrDMHasNoTopic
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE channels SET topic = ?, updated_at = ? WHERE id = ?`,
			topic, s.nowText(), id,
		); err != nil {
			return err
		}

		ch, err = scanChannel(tx.QueryRowContext(ctx,
			`SELECT `+unscopedChannelColumns+` FROM channels c WHERE c.id = ?`, id))
		return err
	})
	if err != nil {
		return storage.Channel{}, fmt.Errorf("update channel topic: %w", err)
	}
	return ch, nil
}

// ListChannelsForUser returns one page of the conversations a user belongs
// to, in stable (created_at, id) order — the sidebar's query.
//
// Membership is the whole visibility rule in Phase 1.2, for a public channel
// exactly as for a private one or a direct message: a channel directory and
// a join flow arrive later, and until they do, nobody sees a room they are
// not in (openapi.yaml listChannels/getChannel, migration 0003's header).
//
// The membership join is what enforces that, and it belongs in SQL. Reading
// every channel and filtering in Go would scale with the size of the
// instance rather than the size of the caller's sidebar, and it is the shape
// in which one forgotten branch silently hands out somebody else's rooms.
//
// Keyset pagination stays correct when channels are created between pages —
// every pre-existing row is returned exactly once. PostgreSQL compares the
// cursor as a row value, (c.created_at, c.id) > ($2, $3); SQLite has none, so
// the same test is written out over the two columns. Both work as plain
// column comparisons because the timestamp encoding is fixed width and
// lexicographic order is chronological.
//
// This is the query whose per-row subquery cost channelColumns describes: it
// is the one that returns a page rather than a row.
func (s *Store) ListChannelsForUser(ctx context.Context, userID uuid.UUID, params storage.ListChannelsParams) ([]storage.Channel, error) {
	var afterCreatedAt, afterID any
	if params.After != nil {
		afterCreatedAt = asTime(params.After.CreatedAt)
		afterID = params.After.ID
	}

	// The membership join's parameter sits between the column list's and the
	// caller joins', because that is where it sits in the statement text.
	args := append(channelSelectArgs(userID), userID)
	args = append(args, channelJoinArgs(userID)...)
	args = append(args, afterCreatedAt, afterCreatedAt, afterCreatedAt, afterID, params.Limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+channelColumns+`
		 FROM channels c
		 JOIN channel_members m ON m.channel_id = c.id AND m.user_id = ?
		 `+callerJoins+`
		 WHERE (? IS NULL
		        OR c.created_at > ?
		        OR (c.created_at = ? AND c.id > ?))
		 ORDER BY c.created_at, c.id
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list channels for user: %w", err)
	}
	// Close's error is not actionable and is not dropped information: any
	// failure during the iteration is what rows.Err below reports, and this
	// runs after the returned value is already built. pgx's Rows.Close
	// returns nothing at all for the same reason.
	defer func() { _ = rows.Close() }()

	channels := []storage.Channel{}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("list channels for user: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channels for user: %w", err)
	}
	return channels, nil
}

// OpenDirectMessage returns the 1:1 direct message between two users,
// creating it the first time. created reports which of the two happened, so
// the handler can answer 201 or 200.
//
// It is idempotent in both argument orders, and it stays idempotent when two
// people open the same pair at the same moment. The pair is canonicalized
// with min/max — by the database, which owns the ordering the
// channels_dm_pair_ordered check tests — and the insert defers to the partial
// unique index channels_dm_pair_key: ON CONFLICT DO NOTHING inserts nothing
// when the pair already has a channel, and the follow-up read then finds it.
// A read-then-insert would race here; two people opening the same DM at once
// is the ordinary case, not the exotic one.
//
// SQLite takes the partial index as a conflict target the same way PostgreSQL
// does, WHERE clause and all, so the statement is the same statement. What
// differs is only what "concurrent" means underneath: PostgreSQL's ON CONFLICT
// DO NOTHING waits for the other inserter and the follow-up read then sees the
// row on a fresh READ COMMITTED snapshot, while here the second opener never
// starts until the first has committed, because it is still waiting at BEGIN
// for the database's write lock. Both end with one channel and one creator.
//
// The min/max canonicalization is over TEXT rather than PostgreSQL's uuid
// type, and that is safe for exactly one reason: canonical lowercase hex is
// monotonic, so text ordering of these ids is byte ordering of the uuids
// (migration 0001's header states this once for the whole tree).
//
// Opening a direct message with oneself is storage.ErrDMWithSelf; a peer who
// does not exist is storage.ErrNotFound.
//
// e2ee applies only when this call is the one that creates the channel.
// Reopening an existing direct message returns it as it is, whatever the flag
// says: get-or-create is idempotent, and a flag cannot re-decide a
// conversation that already exists — which is the same rule as "fixed at
// creation, never toggled" seen from the get-or-create side.
func (s *Store) OpenDirectMessage(ctx context.Context, callerID, peerID uuid.UUID, e2ee bool) (storage.Channel, bool, error) {
	if callerID == peerID {
		return storage.Channel{}, false, fmt.Errorf("open direct message: %w", storage.ErrDMWithSelf)
	}

	var (
		ch      storage.Channel
		created bool
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		id := uuid.New()
		now := s.nowText()

		res, err := tx.ExecContext(ctx,
			`INSERT INTO channels (id, kind, dm_user_a, dm_user_b, e2ee, created_by, created_at, updated_at)
			 VALUES (?, 'dm', min(?, ?), max(?, ?), ?, ?, ?, ?)
			 ON CONFLICT (dm_user_a, dm_user_b) WHERE kind = 'dm' DO NOTHING`,
			id, callerID, peerID, callerID, peerID, boolValue(e2ee), callerID, now, now,
		)
		if err != nil {
			return mapChannelConflict(err)
		}
		inserted, err := rowsAffected(res)
		if err != nil {
			return err
		}

		// Nothing inserted means the pair already had a channel; read it.
		// PostgreSQL tells the two apart by whether RETURNING produced a row,
		// which it cannot do here because the id is generated in Go and the
		// insert has no RETURNING to read.
		if inserted == 0 {
			ch, err = scanChannel(tx.QueryRowContext(ctx,
				`SELECT `+channelColumns+` FROM channels c
				 `+callerJoins+`
				 WHERE c.kind = 'dm'
				   AND c.dm_user_a = min(?, ?)
				   AND c.dm_user_b = max(?, ?)`,
				append(channelCallerArgs(callerID), callerID, peerID, callerID, peerID)...,
			))
			return err
		}

		for _, member := range [...]uuid.UUID{callerID, peerID} {
			if _, execErr := tx.ExecContext(ctx,
				`INSERT INTO channel_members (channel_id, user_id, added_by, joined_at) VALUES (?, ?, ?, ?)`,
				id, member, callerID, now,
			); execErr != nil {
				return fmt.Errorf("add participant: %w", mapMembershipConflict(execErr))
			}
		}

		ch, err = channelForUser(ctx, tx, id, callerID)
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return storage.Channel{}, false, fmt.Errorf("open direct message: %w", err)
	}
	return ch, created, nil
}

// AddChannelMember puts a user in a channel.
//
// It is idempotent: adding somebody who is already a member changes nothing
// and reports no error. That matches the contract (inviting an existing
// member still answers 204) and is the honest model of what happens when two
// people invite the same person at the same moment — there is nothing wrong
// with the outcome, so there is nothing to report.
//
// A direct message's two participants are fixed
// (storage.ErrDMMembershipFixed). An unknown channel is
// storage.ErrChannelNotFound and an unknown user is storage.ErrNotFound.
func (s *Store) AddChannelMember(ctx context.Context, channelID, userID, addedBy uuid.UUID) error {
	kind, err := channelKind(ctx, s.db, channelID)
	if err != nil {
		return fmt.Errorf("add channel member: %w", err)
	}
	if kind == storage.ChannelKindDM {
		return fmt.Errorf("add channel member: %w", storage.ErrDMMembershipFixed)
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO channel_members (channel_id, user_id, added_by, joined_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, userID, addedBy, s.nowText(),
	); err != nil {
		return fmt.Errorf("add channel member: %w", mapMembershipConflict(err))
	}
	return nil
}

// RemoveChannelMember takes a user out of a channel — leaving it, or being
// removed from it.
//
// It is idempotent for the same reason AddChannelMember is: removing
// somebody who is not a member leaves exactly the state the caller asked
// for. A direct message's membership is fixed
// (storage.ErrDMMembershipFixed), and an unknown channel is
// storage.ErrChannelNotFound.
//
// Who may remove this particular member stays the authz layer's question.
// What the membership may become is this method's: removing the only
// remaining member is storage.ErrLastMember, refused for the caller leaving
// exactly as for the caller removing somebody else, because an empty channel
// is a property of the data rather than of who asked. It is enforced here
// because nothing above storage can hold a count and a delete together, and
// two members leaving at the same instant is precisely the case the rule
// exists for.
func (s *Store) RemoveChannelMember(ctx context.Context, channelID, userID uuid.UUID) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// storage.RemoveChannelMember reads this same row as
		//
		//	SELECT kind FROM channels WHERE id = $1 FOR NO KEY UPDATE
		//
		// and the lock clause is the entire design there. Two concurrent
		// removals delete two different channel_members rows, so nothing
		// brings the two statements into conflict; under READ COMMITTED both
		// read a member count of two, both decide they are not the last, and
		// the channel ends up empty. That is write skew, and only a shared
		// lock on something both removals must agree on prevents it — the
		// channels row. The strength matters as much as the lock: FOR UPDATE
		// conflicts with the KEY SHARE every AddChannelMember's foreign key
		// takes on this row, which would put removals and adds in each
		// other's way, and a remover that can be made to wait for an adder is
		// the edge a deadlock cycle needs. FOR NO KEY UPDATE does not
		// conflict with KEY SHARE, so there the two never queue at all.
		//
		// Here there is no lock clause because there is nothing left to
		// serialize. This transaction holds the database's write lock from
		// BEGIN (the DSN's _txlock=immediate; see withTx), so two removals
		// of one channel cannot overlap at all: the second has not started when
		// the first commits, and the count it then runs sees what the first
		// left behind. The refusal is the same refusal, reached without a
		// lock rather than despite one, and it needs no isolation level asked
		// for by name either — SQLite has exactly one.
		//
		// What is lost is only the PostgreSQL-specific SHAPE. There, a
		// removal deliberately never queues behind an adder, and
		// TestRemoveChannelMemberDoesNotBlockOnAddIntegration holds an
		// uncommitted add open to prove it. Here everything queues, briefly:
		// an add in flight holds the write lock, and a removal waits for it
		// the way it waits for any other writer. At household scale that wait
		// is a few milliseconds; it is the price of having no concurrency to
		// get wrong.
		//
		// One statement still does three jobs: the channel exists, here is
		// the kind the direct-message guard tests, and the read is inside the
		// transaction that will do the counting.
		kind, err := channelKind(ctx, tx, channelID)
		if err != nil {
			return err
		}
		if kind == storage.ChannelKindDM {
			return storage.ErrDMMembershipFixed
		}

		// "Is this user the only member" — one question, not two. A count of
		// one is not enough on its own: removing a non-member from a
		// one-member channel is still the idempotent no-op it has always
		// been, and refusing it would break that.
		var sole bool
		err = tx.QueryRowContext(ctx,
			`SELECT count(*) = 1 AND count(*) FILTER (WHERE user_id = ?) = 1
			 FROM channel_members WHERE channel_id = ?`,
			userID, channelID,
		).Scan(&sole)
		if err != nil {
			return fmt.Errorf("count members: %w", err)
		}
		if sole {
			return storage.ErrLastMember
		}

		_, err = tx.ExecContext(ctx,
			`DELETE FROM channel_members WHERE channel_id = ? AND user_id = ?`,
			channelID, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}

// ListChannelMembers returns one page of a channel's members in username
// order, the tie broken by user id.
//
// An unknown channel simply has no members; callers that need to tell an
// empty channel from a missing one ask ChannelByID.
//
// Username ordering and the cursor's comparison are case-insensitive because
// users.username carries the CITEXT collation (collation.go), which a column
// reference brings to any comparison it appears in — the same job
// PostgreSQL's citext type does, and the reason that driver casts its cursor
// parameter to citext rather than leaving it text.
func (s *Store) ListChannelMembers(ctx context.Context, channelID uuid.UUID, params storage.ListChannelMembersParams) ([]storage.User, error) {
	var afterUsername, afterID any
	if params.After != nil {
		afterUsername = params.After.Username
		afterID = params.After.UserID
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memberUserColumns+`
		 FROM channel_members m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.channel_id = ?
		   AND (? IS NULL
		        OR u.username > ?
		        OR (u.username = ? AND u.id > ?))
		 ORDER BY u.username, u.id
		 LIMIT ?`,
		channelID, afterUsername, afterUsername, afterUsername, afterID, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel members: %w", err)
	}
	// See ListChannelsForUser on why the close error is discarded here.
	defer func() { _ = rows.Close() }()

	members := []storage.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list channel members: %w", err)
		}
		members = append(members, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list channel members: %w", err)
	}
	return members, nil
}

// IsChannelMember reports whether a user belongs to a channel. It is the
// membership question the rest of the server leans on hardest — every
// channel-scoped request asks it — so it is a single EXISTS on the
// channel_members primary key and reads nothing else.
func (s *Store) IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	var member bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id = ? AND user_id = ?)`,
		channelID, userID,
	).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("is channel member: %w", err)
	}
	return member, nil
}

// channelKind reads a channel's kind, the precondition the direct-message
// guards test, through either the pool or a caller's transaction. A missing
// channel is storage.ErrChannelNotFound.
//
// Outside a transaction it needs no lock: kind is set at insert and never
// updated, so a stale read is impossible. Inside one it is the first
// statement of the write transaction, which is what RemoveChannelMember's
// note is about.
func channelKind(ctx context.Context, q querier, id uuid.UUID) (storage.ChannelKind, error) {
	var kind storage.ChannelKind
	err := q.QueryRowContext(ctx, `SELECT kind FROM channels WHERE id = ?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrChannelNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read channel kind: %w", err)
	}
	return kind, nil
}

// channelForUser reads one channel as userID sees it, through either the pool
// or a caller's transaction — the creating paths need the row from inside the
// transaction that made it, so the member count includes the members they
// just inserted.
func channelForUser(ctx context.Context, q querier, channelID, userID uuid.UUID) (storage.Channel, error) {
	return scanChannel(q.QueryRowContext(ctx,
		`SELECT `+channelColumns+`
		 FROM channels c
		 `+callerJoins+`
		 WHERE c.id = ?`,
		append(channelCallerArgs(userID), channelID)...))
}

// scanChannel scans one channelColumns (or unscopedChannelColumns) row.
// sql.ErrNoRows becomes storage.ErrChannelNotFound — the channel sentinel,
// not the package-wide ErrNotFound, which here means "no such user".
//
// The nullable columns land in sql.Null* and uuid.NullUUID first because the
// domain type models them as pointers and SQLite hands NULL back untyped.
func scanChannel(row rowScanner) (storage.Channel, error) {
	var (
		ch                            storage.Channel
		slug                          sql.NullString
		dmUserA, dmUserB, createdBy   uuid.NullUUID
		lastRead, peerID              uuid.NullUUID
		peerUsername, peerDisplayName sql.NullString
	)
	err := row.Scan(
		&ch.ID, &ch.Kind, &slug, &ch.Topic, &dmUserA, &dmUserB,
		&createdBy, &ch.E2EE, &ch.MemberCount,
		&ch.UnreadCount, &ch.MentionCount, &lastRead,
		&peerID, &peerUsername, &peerDisplayName,
		nullTimeScan{dst: &ch.LastMessageAt},
		timeScan{dst: &ch.CreatedAt}, timeScan{dst: &ch.UpdatedAt},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Channel{}, storage.ErrChannelNotFound
	}
	if err != nil {
		return storage.Channel{}, err
	}
	ch.Slug = stringPtr(slug)
	ch.DMUserA = channelUUIDPtr(dmUserA)
	ch.DMUserB = channelUUIDPtr(dmUserB)
	ch.CreatedBy = channelUUIDPtr(createdBy)
	ch.LastReadMessageID = channelUUIDPtr(lastRead)
	// The peer's three columns arrive together or not at all — username and
	// display_name are NOT NULL, so the only way any of them is null is the
	// LEFT JOIN matching nobody. All three are checked anyway rather than
	// dereferencing two of them on the strength of that argument.
	if peerID.Valid && peerUsername.Valid && peerDisplayName.Valid {
		ch.DMPeer = &storage.DMPeer{
			ID:          peerID.UUID,
			Username:    peerUsername.String,
			DisplayName: peerDisplayName.String,
		}
	}
	return ch, nil
}

// channelUUIDPtr decodes a nullable uuid column into the *uuid.UUID the
// domain type carries.
func channelUUIDPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	v := n.UUID
	return &v
}

// mapChannelConflict translates constraint violations on the channels table
// into sentinels. Every foreign key on that table points at users, so a
// violated one always means the same thing: one of the people named does not
// exist.
//
// PostgreSQL matches the constraint NAME channels_slug_key; SQLite names the
// columns of the violated index instead, and channels.slug is the only
// unique index on this table whose failure can reach a caller — the DM pair's
// is a conflict target that resolves to DO NOTHING.
func mapChannelConflict(err error) error {
	switch {
	case conflictsOn(err, "channels.slug"):
		return storage.ErrChannelSlugTaken
	case isForeignKeyViolation(err):
		return storage.ErrNotFound
	default:
		return err
	}
}

// mapMembershipConflict translates foreign-key violations on
// channel_members: the channel is gone, or the user is.
//
// The PostgreSQL driver tells those two apart by constraint name and answers
// ErrChannelNotFound for the first. SQLite's foreign-key error names nothing
// at all — "FOREIGN KEY constraint failed" is the whole message — so the
// distinction is not available here and every violation maps to ErrNotFound.
// That costs nothing at any call site: each of them has already established
// that the channel exists, either by reading its kind (AddChannelMember,
// which answers ErrChannelNotFound there) or by having just inserted it in
// this same transaction (CreateChannel, OpenDirectMessage). The only
// interleaving the two answers could differ on is a channel deleted between
// that check and this insert, and nothing in this server deletes a channel.
func mapMembershipConflict(err error) error {
	if isForeignKeyViolation(err) {
		return storage.ErrNotFound
	}
	return err
}
