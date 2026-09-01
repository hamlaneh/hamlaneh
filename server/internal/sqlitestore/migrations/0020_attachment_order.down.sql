-- Another rebuild, for the mirror of the reason the up migration is one:
-- SQLite refuses DROP COLUMN on a column a CHECK constraint names, and
-- attachments_position_shape names message_position. So the table goes back to
-- exactly 0007's shape, minus the column and its constraint.
CREATE TABLE attachments_old (
    id            TEXT    PRIMARY KEY,
    channel_id    TEXT    NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    uploader_id   TEXT    NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    message_id    TEXT    REFERENCES messages (id) ON DELETE SET NULL,

    filename      TEXT    NOT NULL CHECK (length(filename) BETWEEN 1 AND 255),
    content_type  TEXT    NOT NULL CHECK (length(content_type) BETWEEN 1 AND 255),
    size_bytes    INTEGER NOT NULL CHECK (size_bytes >= 0),
    width         INTEGER CHECK (width  > 0),
    height        INTEGER CHECK (height > 0),
    has_thumbnail INTEGER NOT NULL DEFAULT 0
                          CHECK (has_thumbnail IN (0, 1)),

    created_at    TEXT    NOT NULL,

    CONSTRAINT attachments_dimensions_shape CHECK ((width IS NULL) = (height IS NULL))
);

-- The sender's order is what a rollback loses; the cards fall back to upload
-- order, which is the behaviour 0020 replaced. No other data goes with it.
INSERT INTO attachments_old
    (id, channel_id, uploader_id, message_id, filename, content_type,
     size_bytes, width, height, has_thumbnail, created_at)
SELECT id, channel_id, uploader_id, message_id, filename, content_type,
       size_bytes, width, height, has_thumbnail, created_at
FROM attachments;

DROP TABLE attachments;
ALTER TABLE attachments_old RENAME TO attachments;

CREATE INDEX attachments_message_id_idx ON attachments (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX attachments_channel_id_idx ON attachments (channel_id);
CREATE INDEX attachments_orphan_idx ON attachments (created_at) WHERE message_id IS NULL;
