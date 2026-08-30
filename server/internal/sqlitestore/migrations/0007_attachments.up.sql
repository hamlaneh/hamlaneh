-- SQLite counterpart of 0007_attachments.up.sql (ADR 003). The blob lives on
-- the filesystem under a path derived from id alone; the client's filename is
-- display data that never touches a path.
CREATE TABLE attachments (
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

    -- Last, because SQLite (unlike PostgreSQL) admits no column definition
    -- after the first table constraint.
    CONSTRAINT attachments_dimensions_shape CHECK ((width IS NULL) = (height IS NULL))
);

-- The message's cards, and the membership-scoped file search join.
CREATE INDEX attachments_message_id_idx ON attachments (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX attachments_channel_id_idx ON attachments (channel_id);

-- The orphan sweep's scan: unattached rows, oldest first.
CREATE INDEX attachments_orphan_idx ON attachments (created_at) WHERE message_id IS NULL;

-- No filename search index, for the same reason 0006 creates none: the
-- PostgreSQL tree accelerates the identical fold with pg_trgm, and here the
-- fold is applied in Go and matched with instr() over a scan. Semantics are
-- the same; the ceiling is the one 0006 names.

-- PostgreSQL also replaces messages_content_shape here, loosening the live
-- lower bound from 1 to 0 so an image with no caption is an ordinary message.
-- SQLite can neither drop nor add a constraint, and no SQLite database ever
-- held the stricter form, so 0003 in this tree declares the loosened shape
-- directly. Nothing to do.
