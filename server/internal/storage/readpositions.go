package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// setReadPositionQuery stores one read position, or declines to, in a single
// statement.
//
// anchor is the message the caller names, looked up by primary key and
// required to be in this channel — the query's only visibility rule, and the
// reason a message id from another conversation is answered exactly like one
// that does not exist. A soft-deleted message anchors a position perfectly
// well: history keeps the row, the client draws a placeholder, and it is
// legitimately the newest thing that client has seen.
//
// The upsert copies the anchor's created_at into last_read_at, which is what
// the whole design turns on: the monotonic test is then a comparison of
// stored columns, and unread counting compares (created_at, id) tuples
// straight against messages_channel_created_idx with no join back to resolve
// the anchor's timestamp. The comparison is on the pair rather than the
// timestamp alone because that is the order messages are read in — messages
// sent in the same instant share a created_at, and a timestamp-only test
// would refuse the rest of that run forever.
//
// A position at or behind the stored one fails the DO UPDATE's WHERE, so
// nothing is written and no error is raised: a background tab replaying the
// position it remembers must not be able to mark a channel unread again.
// Concurrent writers need no explicit lock (see the package's lock order) —
// ON CONFLICT DO UPDATE waits for the other writer and then evaluates that
// comparison against the row it committed.
//
// The trailing SELECT is what separates "there was no such message here"
// from "the position was a regression"; both write nothing, and only the
// first is an error. The upsert runs either way: a data-modifying CTE always
// executes to completion whether or not the primary query reads its output.
const setReadPositionQuery = `WITH anchor AS (
	    SELECT msg.channel_id, msg.id, msg.created_at
	    FROM messages msg
	    WHERE msg.id = $3 AND msg.channel_id = $1
	), stored AS (
	    INSERT INTO channel_read_positions (channel_id, user_id, last_read_message_id, last_read_at)
	    SELECT anchor.channel_id, $2, anchor.id, anchor.created_at FROM anchor
	    ON CONFLICT (channel_id, user_id) DO UPDATE
	        SET last_read_message_id = EXCLUDED.last_read_message_id,
	            last_read_at         = EXCLUDED.last_read_at,
	            updated_at           = now()
	        WHERE (EXCLUDED.last_read_at, EXCLUDED.last_read_message_id)
	            > (channel_read_positions.last_read_at, channel_read_positions.last_read_message_id)
	    RETURNING 1
	)
	SELECT EXISTS (SELECT 1 FROM anchor)`

// SetReadPosition moves a user's read position in a channel to the message
// they name — where the "New messages" divider goes, and the point every
// unread count starts from.
//
// It is monotonic per (user, channel): a position at or behind the stored one
// is accepted and changes nothing rather than being an error, so a stale tab
// cannot undo a read. The endpoint answers 204 either way, which is why this
// returns nil for both.
//
// Own-device sync only. The position is written under userID and read back
// only for that same user; nothing in this package exposes one person's read
// position to anybody else, and nothing should start to — the contract
// carries no cross-user read receipts anywhere. Pass the authenticated
// caller's id, never one taken from a request body.
//
// A messageID that does not name a message of this channel is ErrNotFound,
// and so is a userID with no account. The two look identical from outside on
// purpose: a message in a conversation the caller cannot see is answered
// exactly like a message that never existed, so the 404 cannot be used to
// probe for messages elsewhere. Whether the caller may read this channel at
// all is the authz layer's question, not this one's.
func (s *Store) SetReadPosition(ctx context.Context, channelID, userID, messageID uuid.UUID) error {
	var inChannel bool
	err := s.pool.QueryRow(ctx, setReadPositionQuery, channelID, userID, messageID).Scan(&inChannel)
	if err != nil {
		// The only foreign key that can fail here is the user's: the channel
		// and the message come from the anchor row the query just read.
		return fmt.Errorf("set read position: %w", mapMissingReference(err))
	}
	if !inChannel {
		return fmt.Errorf("set read position: %w", ErrNotFound)
	}
	return nil
}
