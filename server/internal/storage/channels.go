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

// Sentinel errors for channels and membership. ErrNotFound keeps its
// package-wide meaning here — "no such user" — because a missing channel has
// a sentinel of its own; the two are deliberately disjoint so a handler that
// passes both a channel id and a user id can tell which one was wrong.
var (
	// ErrChannelNotFound reports that no channel has that id.
	ErrChannelNotFound = errors.New("storage: channel not found")
	// ErrChannelSlugTaken reports a channel-name conflict. Slugs are unique
	// across public and private channels alike.
	ErrChannelSlugTaken = errors.New("storage: channel slug already taken")
	// ErrDMMembershipFixed reports an attempt to add or remove a member of a
	// direct message, whose two participants are fixed at creation.
	ErrDMMembershipFixed = errors.New("storage: a direct message's membership is fixed")
	// ErrDMHasNoTopic reports an attempt to set the topic of a direct
	// message; the schema keeps that column empty for them.
	ErrDMHasNoTopic = errors.New("storage: a direct message has no topic")
	// ErrDMWithSelf reports an attempt to open a direct message with
	// oneself.
	ErrDMWithSelf = errors.New("storage: a direct message needs two different users")
)

// ChannelKind is the flat channel taxonomy of ADR 001 — the instance is the
// organization, so there is no org or team layer above these.
type ChannelKind string

// The three kinds a channel row may carry.
const (
	ChannelKindPublic  ChannelKind = "public"
	ChannelKindPrivate ChannelKind = "private"
	ChannelKindDM      ChannelKind = "dm"
)

// Channel is a row of the channels table, plus the derived fields every
// channel response carries.
//
// MemberCount is derived, not stored: each query counts channel_members for
// the row it returns. It lives here because the sidebar renders a whole page
// of channels at once and asking per channel would be a query per row. The
// same is true of everything below it, which is why the counts are computed
// in the channel query rather than by a second call.
//
// UnreadCount, MentionCount and LastReadMessageID are the *caller's* view and
// mean nothing without one. UnreadCount counts the messages after the
// caller's read position, less the caller's own and the deleted ones;
// MentionCount is the subset of exactly those that mention the caller — the
// filled "@" badge, which can therefore never exceed the plain unread count.
// LastReadMessageID is nil until the caller has read the channel at all.
// Only the paths that know who is asking fill them: ListChannelsForUser,
// ChannelForUser, CreateChannel and OpenDirectMessage. ChannelByID and
// UpdateChannelTopic leave them at 0 and nil because there is no caller in
// scope — that zero means "nobody asked", not "nothing unread", and anything
// serving a channel to a user must go through a caller-scoped read instead.
//
// LastMessageAt needs no caller — the newest message in a channel is the same
// fact whoever asks — so every path fills it. It is nil in an empty channel,
// and it counts a deleted message: history keeps the row's place, so it is
// still the last thing that happened there.
//
// Slug is nil exactly for a direct message, which the client labels with the
// other participant's name instead; DMUserA and DMUserB are set exactly for
// a direct message, canonicalized by the database as DMUserA < DMUserB so
// one pair can only ever have one channel. CreatedBy is nil once that
// account is gone (ON DELETE SET NULL).
type Channel struct {
	ID                uuid.UUID
	Kind              ChannelKind
	Slug              *string
	Topic             string
	DMUserA           *uuid.UUID
	DMUserB           *uuid.UUID
	CreatedBy         *uuid.UUID
	MemberCount       int
	UnreadCount       int
	MentionCount      int
	LastReadMessageID *uuid.UUID
	LastMessageAt     *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewChannel carries the fields for creating a named channel. Validation is
// the handler's job; the database constraints are the backstop.
type NewChannel struct {
	// Kind must be public or private — a direct message is opened through
	// OpenDirectMessage, which is the only path that can canonicalize its
	// pair.
	Kind      ChannelKind
	Slug      string
	Topic     string
	CreatedBy uuid.UUID
}

// ChannelCursor is a keyset-pagination position: the (created_at, id) of the
// last row of the previous page.
type ChannelCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListChannelsParams control one page of ListChannelsForUser. After nil
// means the first page. Limit must be positive; callers own clamping it to
// the API contract.
type ListChannelsParams struct {
	After *ChannelCursor
	Limit int
}

// ChannelMemberCursor is a keyset-pagination position in a member list: the
// (username, user id) of the last row of the previous page.
type ChannelMemberCursor struct {
	Username string
	UserID   uuid.UUID
}

// ListChannelMembersParams control one page of ListChannelMembers. After nil
// means the first page. Limit must be positive.
type ListChannelMembersParams struct {
	After *ChannelMemberCursor
	Limit int
}

// The canonical column lists every channel query selects, in the order
// scanChannel expects. Both require the channels table to be aliased c —
// INSERT and UPDATE can do that too (INSERT INTO channels AS c).
//
// The head and tail exist so the two lists cannot drift apart: they differ in
// exactly the three caller-scoped columns and nowhere else.
const (
	channelColumnsHead = `c.id, c.kind, c.slug, c.topic, c.dm_user_a, c.dm_user_b, c.created_by,
	        (SELECT count(*) FROM channel_members mc WHERE mc.channel_id = c.id)::int, `

	channelColumnsTail = `,
	        (SELECT max(msg.created_at) FROM messages msg WHERE msg.channel_id = c.id),
	        c.created_at, c.updated_at`

	// channelColumns is what a query selects when it knows who is asking. It
	// requires the caller's user id bound as $1 and the joins in callerJoins.
	channelColumns = channelColumnsHead +
		`counts.unread, counts.mentions, rp.last_read_message_id` +
		channelColumnsTail

	// unscopedChannelColumns is what a query selects when there is no caller:
	// literal zeros and a null, never a computed value. See ChannelByID on
	// why they are not an answer to anything.
	unscopedChannelColumns = channelColumnsHead + `0, 0, NULL::uuid` + channelColumnsTail
)

// callerJoins resolve everything about a channel that depends on who is
// asking. Every query selecting channelColumns must carry them after
// `FROM channels c`, with the caller's user id bound as $1.
//
// The read position is a primary-key lookup, left-joined because a channel
// the caller has never opened has no row there. The counts are one pass over
// the messages after that position: a range scan on
// messages_channel_created_idx that *starts* at the read position instead of
// at the channel's first message, so a caller who is caught up reads nothing
// at all and a caller who is behind pays for what they are behind by. The
// bound is coalesced to (-infinity, the smallest uuid) rather than guarded by
// an OR, because an OR would turn that range scan into a filter over the
// channel's whole history.
//
// Both counts come out of that single pass. The mentions are a LEFT JOIN on
// the message_mentions primary key, which can neither drop a row nor
// duplicate one, so mention_count is a count of the non-null side over
// exactly the rows unread_count counted — a subset by construction, not by
// two WHERE clauses somebody has to keep identical. Joining rather than
// testing EXISTS per row also lets the planner read the caller's mentions
// once through message_mentions_user_id_idx instead of probing the primary
// key once per unread message, which measures ~40x fewer buffers on a
// channel with a thousand unread.
const callerJoins = `LEFT JOIN channel_read_positions rp
	            ON rp.channel_id = c.id AND rp.user_id = $1
	     LEFT JOIN LATERAL (
	         SELECT count(*)::int AS unread,
	                count(mn.message_id)::int AS mentions
	         FROM messages msg
	         LEFT JOIN message_mentions mn
	                ON mn.message_id = msg.id AND mn.mentioned_user_id = $1
	         WHERE msg.channel_id = c.id
	           AND (msg.created_at, msg.id) > (
	                   coalesce(rp.last_read_at, '-infinity'::timestamptz),
	                   coalesce(rp.last_read_message_id, '00000000-0000-0000-0000-000000000000'::uuid))
	           AND msg.deleted_at IS NULL
	           AND msg.author_id <> $1
	     ) counts ON true`

// memberUserColumns is userColumns qualified for the joins in this file; it
// must stay in the order scanUser expects.
const memberUserColumns = `u.id, u.username, u.email, u.display_name, u.password_hash,
	        u.locale, u.is_admin, u.must_change_password, u.created_at, u.updated_at`

// CreateChannel inserts a public or private channel and makes its creator
// the first member, in one transaction: a channel nobody belongs to is
// unreachable, and the empty-channel screen promises the creator is already
// in it.
//
// A duplicate slug maps to ErrChannelSlugTaken, a creator who does not exist
// to ErrNotFound. Direct messages are refused outright — only
// OpenDirectMessage can produce a well-formed pair.
func (s *Store) CreateChannel(ctx context.Context, nc NewChannel) (Channel, error) {
	if nc.Kind != ChannelKindPublic && nc.Kind != ChannelKindPrivate {
		return Channel{}, fmt.Errorf("create channel: kind %q must be public or private", nc.Kind)
	}

	var ch Channel
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx,
			`INSERT INTO channels (kind, slug, topic, created_by)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id`,
			string(nc.Kind), nc.Slug, nc.Topic, nc.CreatedBy,
		).Scan(&id)
		if err != nil {
			return mapChannelConflict(err)
		}

		if _, execErr := tx.Exec(ctx,
			`INSERT INTO channel_members (channel_id, user_id, added_by) VALUES ($1, $2, $2)`,
			id, nc.CreatedBy,
		); execErr != nil {
			return fmt.Errorf("add creator as member: %w", mapMembershipConflict(execErr))
		}

		// Re-read rather than RETURNING from the insert: the member count is
		// only right once the creator's row exists.
		ch, err = channelForUser(ctx, tx, id, nc.CreatedBy)
		return err
	})
	if err != nil {
		return Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return ch, nil
}

// ChannelByID returns the channel with the given id, or ErrChannelNotFound.
//
// It applies no visibility check at all. Callers serving a request must gate
// on membership — IsChannelMember, or the authz package — before handing any
// of it to a user; ListChannelsForUser is the scoped counterpart for lists.
//
// There is no caller here, so UnreadCount and MentionCount come back 0 and
// LastReadMessageID nil — not because the channel is read, but because
// "unread" is a question about a person and this function was not told one.
// Anything that serves a channel to a user must read it through
// ChannelForUser, or it will render a zero nobody computed. LastMessageAt is
// filled: it needs no caller.
func (s *Store) ChannelByID(ctx context.Context, id uuid.UUID) (Channel, error) {
	ch, err := scanChannel(s.pool.QueryRow(ctx,
		`SELECT `+unscopedChannelColumns+` FROM channels c WHERE c.id = $1`, id))
	if err != nil {
		return Channel{}, fmt.Errorf("channel by id: %w", err)
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
func (s *Store) ChannelForUser(ctx context.Context, channelID, userID uuid.UUID) (Channel, error) {
	ch, err := channelForUser(ctx, s.pool, channelID, userID)
	if err != nil {
		return Channel{}, fmt.Errorf("channel for user: %w", err)
	}
	return ch, nil
}

// UpdateChannelTopic sets a channel's topic and returns the updated row. An
// empty topic clears it.
//
// The topic is the only mutable field in this slice: renaming a channel
// would move it under everyone's cursor mid-conversation and is not in the
// contract. A direct message has no topic to set (ErrDMHasNoTopic), and an
// unknown channel is ErrChannelNotFound.
//
// The returned Channel carries no caller-scoped counts, for the reason
// ChannelByID gives: an update statement is not told who ran it. A handler
// that answers with the whole channel should re-read it through
// ChannelForUser rather than serve this row's zeros.
func (s *Store) UpdateChannelTopic(ctx context.Context, id uuid.UUID, topic string) (Channel, error) {
	kind, err := s.channelKind(ctx, id)
	if err != nil {
		return Channel{}, fmt.Errorf("update channel topic: %w", err)
	}
	if kind == ChannelKindDM {
		return Channel{}, fmt.Errorf("update channel topic: %w", ErrDMHasNoTopic)
	}

	ch, err := scanChannel(s.pool.QueryRow(ctx,
		`UPDATE channels AS c SET topic = $2, updated_at = now()
		 WHERE c.id = $1
		 RETURNING `+unscopedChannelColumns,
		id, topic,
	))
	if err != nil {
		return Channel{}, fmt.Errorf("update channel topic: %w", err)
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
// every pre-existing row is returned exactly once.
func (s *Store) ListChannelsForUser(ctx context.Context, userID uuid.UUID, params ListChannelsParams) ([]Channel, error) {
	var afterCreatedAt *time.Time
	var afterID *uuid.UUID
	if params.After != nil {
		afterCreatedAt = &params.After.CreatedAt
		afterID = &params.After.ID
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+channelColumns+`
		 FROM channels c
		 JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
		 `+callerJoins+`
		 WHERE ($2::timestamptz IS NULL OR (c.created_at, c.id) > ($2, $3))
		 ORDER BY c.created_at, c.id
		 LIMIT $4`,
		userID, afterCreatedAt, afterID, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list channels for user: %w", err)
	}
	defer rows.Close()

	channels := []Channel{}
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
// with least/greatest — by the database, which owns the uuid ordering the
// channels_dm_pair_ordered check tests — and the insert defers to the
// partial unique index: ON CONFLICT DO NOTHING waits for a concurrent
// inserter to finish, and the follow-up read (a new statement, so a new READ
// COMMITTED snapshot) then sees the row it committed. A read-then-insert
// would race here; two people opening the same DM at once is the ordinary
// case, not the exotic one.
//
// Opening a direct message with oneself is ErrDMWithSelf; a peer who does
// not exist is ErrNotFound.
func (s *Store) OpenDirectMessage(ctx context.Context, callerID, peerID uuid.UUID) (Channel, bool, error) {
	if callerID == peerID {
		return Channel{}, false, fmt.Errorf("open direct message: %w", ErrDMWithSelf)
	}

	var (
		ch      Channel
		created bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx,
			`INSERT INTO channels (kind, dm_user_a, dm_user_b, created_by)
			 VALUES ('dm', least($1::uuid, $2::uuid), greatest($1::uuid, $2::uuid), $1)
			 ON CONFLICT (dm_user_a, dm_user_b) WHERE kind = 'dm' DO NOTHING
			 RETURNING id`,
			callerID, peerID,
		).Scan(&id)

		// No row back means the pair already had a channel; read it.
		if errors.Is(err, pgx.ErrNoRows) {
			ch, err = scanChannel(tx.QueryRow(ctx,
				`SELECT `+channelColumns+` FROM channels c
				 `+callerJoins+`
				 WHERE c.kind = 'dm'
				   AND c.dm_user_a = least($1::uuid, $2::uuid)
				   AND c.dm_user_b = greatest($1::uuid, $2::uuid)`,
				callerID, peerID,
			))
			return err
		}
		if err != nil {
			return mapChannelConflict(err)
		}

		for _, member := range [...]uuid.UUID{callerID, peerID} {
			if _, execErr := tx.Exec(ctx,
				`INSERT INTO channel_members (channel_id, user_id, added_by) VALUES ($1, $2, $3)`,
				id, member, callerID,
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
		return Channel{}, false, fmt.Errorf("open direct message: %w", err)
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
// A direct message's two participants are fixed (ErrDMMembershipFixed). An
// unknown channel is ErrChannelNotFound and an unknown user is ErrNotFound.
func (s *Store) AddChannelMember(ctx context.Context, channelID, userID, addedBy uuid.UUID) error {
	kind, err := s.channelKind(ctx, channelID)
	if err != nil {
		return fmt.Errorf("add channel member: %w", err)
	}
	if kind == ChannelKindDM {
		return fmt.Errorf("add channel member: %w", ErrDMMembershipFixed)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO channel_members (channel_id, user_id, added_by) VALUES ($1, $2, $3)
		 ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, userID, addedBy,
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
// for. A direct message's membership is fixed (ErrDMMembershipFixed), and an
// unknown channel is ErrChannelNotFound.
//
// Whether the caller is allowed to remove this particular member, and
// whether the channel may be left with nobody in it, are policy questions
// for the authz layer; this method enforces the shape of the data, not the
// rules about who may change it.
func (s *Store) RemoveChannelMember(ctx context.Context, channelID, userID uuid.UUID) error {
	kind, err := s.channelKind(ctx, channelID)
	if err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	if kind == ChannelKindDM {
		return fmt.Errorf("remove channel member: %w", ErrDMMembershipFixed)
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM channel_members WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}

// ListChannelMembers returns one page of a channel's members in username
// order, the tie broken by user id.
//
// An unknown channel simply has no members; callers that need to tell an
// empty channel from a missing one ask ChannelByID.
func (s *Store) ListChannelMembers(ctx context.Context, channelID uuid.UUID, params ListChannelMembersParams) ([]User, error) {
	var afterUsername *string
	var afterID *uuid.UUID
	if params.After != nil {
		afterUsername = &params.After.Username
		afterID = &params.After.UserID
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+memberUserColumns+`
		 FROM channel_members m
		 JOIN users u ON u.id = m.user_id
		 WHERE m.channel_id = $1
		   AND ($2::citext IS NULL OR (u.username, u.id) > ($2::citext, $3))
		 ORDER BY u.username, u.id
		 LIMIT $4`,
		channelID, afterUsername, afterID, params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel members: %w", err)
	}
	defer rows.Close()

	members := []User{}
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
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2)`,
		channelID, userID,
	).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("is channel member: %w", err)
	}
	return member, nil
}

// channelKind reads a channel's kind, the precondition the direct-message
// guards test. It needs no lock and no transaction: kind is set at insert
// and never updated, so a stale read is impossible.
func (s *Store) channelKind(ctx context.Context, id uuid.UUID) (ChannelKind, error) {
	var kind ChannelKind
	err := s.pool.QueryRow(ctx, `SELECT kind FROM channels WHERE id = $1`, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrChannelNotFound
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
func channelForUser(ctx context.Context, q rowQuerier, channelID, userID uuid.UUID) (Channel, error) {
	return scanChannel(q.QueryRow(ctx,
		`SELECT `+channelColumns+`
		 FROM channels c
		 `+callerJoins+`
		 WHERE c.id = $2`,
		userID, channelID))
}

// scanChannel scans one channelColumns (or unscopedChannelColumns) row.
// pgx.ErrNoRows becomes ErrChannelNotFound.
func scanChannel(row pgx.Row) (Channel, error) {
	var ch Channel
	err := row.Scan(
		&ch.ID, &ch.Kind, &ch.Slug, &ch.Topic, &ch.DMUserA, &ch.DMUserB,
		&ch.CreatedBy, &ch.MemberCount,
		&ch.UnreadCount, &ch.MentionCount, &ch.LastReadMessageID, &ch.LastMessageAt,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	}
	if err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// mapChannelConflict translates constraint violations on the channels table
// into sentinels. Every foreign key on that table points at users, so a
// violated one always means the same thing: one of the people named does not
// exist.
func mapChannelConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "channels_slug_key" {
		return ErrChannelSlugTaken
	}
	if pgErr.Code == pgerrcode.ForeignKeyViolation {
		return ErrNotFound
	}
	return err
}

// mapMembershipConflict translates foreign-key violations on
// channel_members: the channel is gone, or the user is.
func mapMembershipConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.ForeignKeyViolation {
		return err
	}
	if pgErr.ConstraintName == "channel_members_channel_id_fkey" {
		return ErrChannelNotFound
	}
	return ErrNotFound
}
