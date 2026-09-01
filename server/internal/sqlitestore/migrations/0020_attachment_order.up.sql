-- SQLite counterpart of 0020_attachment_order.up.sql: a message's files render
-- in the order the SENDER listed them, stored rather than inferred from
-- created_at. The PostgreSQL file carries the reasoning.
--
-- It is a table rebuild, not an ALTER TABLE ADD COLUMN, and the reason is
-- specific rather than the usual SQLite shrug. The constraint pairs two
-- columns, so it has to be a table constraint, and SQLite can add neither a
-- table constraint to an existing table nor a cross-column CHECK on an added
-- column while any existing row would break it — every already-claimed row
-- breaks it until the backfill runs, and ADD COLUMN validates before there is
-- any chance to run one. A rebuild does the backfill and the constraint in the
-- same statement, so there is no moment in between for either to be wrong.
--
-- The drop and rename cannot disturb another table's foreign keys, because no
-- table references attachments. (PRAGMA foreign_keys is on for this connection
-- and a no-op inside the transaction golang-migrate runs a migration in, so
-- "nothing points here" is the property doing the work, not a pragma.)
CREATE TABLE attachments_new (
    id            TEXT    PRIMARY KEY,
    channel_id    TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    uploader_id   TEXT    NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    message_id    TEXT    REFERENCES messages (id) ON DELETE SET NULL,

    filename      TEXT    NOT NULL CHECK (length(filename) BETWEEN 1 AND 255),
    content_type  TEXT    NOT NULL CHECK (length(content_type) BETWEEN 1 AND 255),
    size_bytes    INTEGER NOT NULL CHECK (size_bytes >= 0),
    -- Images only; both set together at ingest, after the sniff.
    width         INTEGER CHECK (width  > 0),
    height        INTEGER CHECK (height > 0),
    has_thumbnail INTEGER NOT NULL DEFAULT 0
                          CHECK (has_thumbnail IN (0, 1)),

    created_at    TEXT    NOT NULL,

    -- The sender's order. Last of the columns, matching where PostgreSQL's
    -- ADD COLUMN puts it.
    message_position INTEGER CHECK (message_position >= 0),

    -- Last, because SQLite (unlike PostgreSQL) admits no column definition
    -- after the first table constraint.
    CONSTRAINT attachments_dimensions_shape CHECK ((width IS NULL) = (height IS NULL)),
    CONSTRAINT attachments_position_shape CHECK ((message_id IS NULL) = (message_position IS NULL))
);

-- The copy is the backfill: already-claimed rows get today's visible order —
-- created_at, then id — and unclaimed ones keep the NULL the constraint pairs
-- with their NULL message_id. Leaving a claimed row NULL would make the two
-- drivers disagree about where its card sits, since PostgreSQL sorts NULLs
-- last and SQLite sorts them first.
INSERT INTO attachments_new
    (id, channel_id, uploader_id, message_id, filename, content_type,
     size_bytes, width, height, has_thumbnail, created_at, message_position)
SELECT id, channel_id, uploader_id, message_id, filename, content_type,
       size_bytes, width, height, has_thumbnail, created_at,
       CASE WHEN message_id IS NULL THEN NULL
            ELSE row_number() OVER (PARTITION BY message_id ORDER BY created_at, id) - 1
       END
FROM attachments;

DROP TABLE attachments;
ALTER TABLE attachments_new RENAME TO attachments;

-- All three indexes go with the dropped table and are rebuilt here. The first
-- replaces 0007's attachments_message_id_idx: message_id still leads, so it
-- answers the same lookup, and being UNIQUE it makes the new order total — no
-- two files of one message can share a position — which is what lets the read
-- paths drop the id tiebreak. The other two are 0007's, unchanged.
CREATE UNIQUE INDEX attachments_message_order_idx
    ON attachments (message_id, message_position) WHERE message_id IS NOT NULL;
CREATE INDEX attachments_channel_id_idx ON attachments (channel_id);
CREATE INDEX attachments_orphan_idx ON attachments (created_at) WHERE message_id IS NULL;
