-- SQLite counterpart of 0008_link_previews.up.sql (ADR 003). At most one
-- preview per message, so message_id is the primary key; CASCADE because a
-- preview is regenerable from the message's own text.
CREATE TABLE link_previews (
    message_id    TEXT PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,

    url           TEXT NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    title         TEXT CHECK (length(title) BETWEEN 1 AND 200),
    description   TEXT CHECK (length(description) BETWEEN 1 AND 500),
    -- Names a derivative in the blob store, deliberately not a foreign key.
    image_blob_id TEXT,

    fetched_at    TEXT NOT NULL
);
