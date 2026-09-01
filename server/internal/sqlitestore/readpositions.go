package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/hamlaneh/hamlaneh/server/internal/storage"
)

// readPositionAnchorQuery resolves the message the caller names.
//
// It is looked up by primary key and required to be in this channel — the
// only visibility rule in this file, and the reason a message id from another
// conversation is answered exactly like one that does not exist. A
// soft-deleted message anchors a position perfectly well: history keeps the
// row, the client draws a placeholder, and it is legitimately the newest
// thing that client has seen.
//
// It returns created_at as the stored TEXT and the upsert binds that text
// straight back, rather than decoding to a time.Time and re-encoding. That is
// what "copies the anchor's created_at" means literally, and it removes the
// only way the two columns could ever disagree.
const readPositionAnchorQuery = `SELECT msg.created_at
	FROM messages msg
	WHERE msg.id = ? AND msg.channel_id = ?`

// setReadPositionQuery stores one read position, or declines to.
//
// It copies the anchor's created_at into last_read_at, which is what the
// whole design turns on: the monotonic test is then a comparison of stored
// columns, and unread counting compares (created_at, id) tuples straight
// against messages_channel_created_idx with no join back to resolve the
// anchor's timestamp. The comparison is on the pair rather than the timestamp
// alone because that is the order messages are read in — messages sent in the
// same instant share a created_at, and a timestamp-only test would refuse the
// rest of that run forever. PostgreSQL writes that pair comparison as one row
// value; SQLite has no row values, so it is written out over the two columns.
//
// A position at or behind the stored one fails the DO UPDATE's WHERE, so
// nothing is written and no error is raised: a background tab replaying the
// position it remembers must not be able to mark a channel unread again.
//
// The PostgreSQL driver notes that concurrent writers need no explicit lock
// because ON CONFLICT DO UPDATE waits for the other writer and then evaluates
// that comparison against the row it committed. Here there is no concurrent
// writer to wait for: SetReadPosition runs inside a write transaction that
// holds the database's write lock, so a second one has not begun. The
// comparison sees the committed row either way, and the monotonic promise is
// the same promise.
const setReadPositionQuery = `INSERT INTO channel_read_positions
	    (channel_id, user_id, last_read_message_id, last_read_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT (channel_id, user_id) DO UPDATE
	    SET last_read_message_id = excluded.last_read_message_id,
	        last_read_at         = excluded.last_read_at,
	        updated_at           = excluded.updated_at
	    WHERE (excluded.last_read_at > channel_read_positions.last_read_at
	           OR (excluded.last_read_at = channel_read_positions.last_read_at
	               AND excluded.last_read_message_id > channel_read_positions.last_read_message_id))`

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
// A messageID that does not name a message of this channel is
// storage.ErrNotFound, and so is a userID with no account. The two look
// identical from outside on purpose: a message in a conversation the caller
// cannot see is answered exactly like a message that never existed, so the
// 404 cannot be used to probe for messages elsewhere. Whether the caller may
// read this channel at all is the authz layer's question, not this one's.
//
// PostgreSQL does the whole thing in one statement: the anchor is a CTE, the
// upsert is a data-modifying CTE beside it, and the trailing SELECT over the
// anchor is what separates "there was no such message here" from "the
// position was a regression" — both write nothing, and only the first is an
// error. SQLite has no data-modifying CTEs, so the two halves are two
// statements in one transaction. The distinction that trailing SELECT existed
// to draw is drawn here by reading the anchor first and failing on no rows,
// which is the same question asked earlier rather than a different one; and
// the transaction is what keeps the anchor's timestamp and the row it is
// written into from being separated by another writer.
func (s *Store) SetReadPosition(ctx context.Context, channelID, userID, messageID uuid.UUID) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var anchorAt string
		err := tx.QueryRowContext(ctx, readPositionAnchorQuery, messageID, channelID).Scan(&anchorAt)
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read anchor message: %w", err)
		}

		if _, err := tx.ExecContext(ctx, setReadPositionQuery,
			channelID, userID, messageID, anchorAt, s.nowText(),
		); err != nil {
			// The only foreign key that can fail here is the user's: the
			// channel and the message come from the anchor row just read.
			// SQLite's foreign-key error names no constraint, so this maps
			// every one of them the same way — which is what the PostgreSQL
			// driver's mapMissingReference does too, for the same reason.
			if isForeignKeyViolation(err) {
				return storage.ErrNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("set read position: %w", err)
	}
	return nil
}
