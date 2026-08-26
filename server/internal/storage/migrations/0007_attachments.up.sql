-- Phase 1.3: file attachments, per ADR 003.
--
-- A file belongs to a channel from birth — uploaded through a channel-scoped
-- endpoint, readable exactly by that channel's members — and to at most one
-- message. message_id is NULL between upload and send; the orphan sweep
-- deletes rows (and their blobs) that are still unattached after 24 hours,
-- so an abandoned composer costs a day of disk, not forever.
--
-- The blob itself lives on the filesystem under a path derived from id
-- alone. The client's filename is stored here as display data and never
-- touches a path, which is what makes traversal structurally impossible
-- rather than filtered.
--
-- Foreign keys follow 0003's policy: the channel owns the file's visibility,
-- so channel deletion cascades; the uploader is attribution, kept RESTRICT
-- like message authorship — offboarding deactivates, it does not erase.
-- message_id is SET NULL rather than CASCADE deliberately: messages are only
-- ever soft-deleted today, so this path never fires, but if a hard delete
-- ever arrives the safe failure is an orphan the sweep collects, not a blob
-- silently gone while a row still points at it.
CREATE TABLE attachments (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id    uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    uploader_id   uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    message_id    uuid        REFERENCES messages (id) ON DELETE SET NULL,

    filename      text        NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    content_type  text        NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 255),
    size_bytes    bigint      NOT NULL CHECK (size_bytes >= 0),
    -- Images only; both set together at ingest, after the sniff. A row with
    -- dimensions is one whose bytes proved to be the image they claimed.
    width         integer     CHECK (width  > 0),
    height        integer     CHECK (height > 0),
    has_thumbnail boolean     NOT NULL DEFAULT false,
    CONSTRAINT attachments_dimensions_shape CHECK ((width IS NULL) = (height IS NULL)),

    created_at    timestamptz NOT NULL DEFAULT now()
);

-- The message's cards, and the membership-scoped file search join.
CREATE INDEX attachments_message_id_idx ON attachments (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX attachments_channel_id_idx ON attachments (channel_id);

-- The orphan sweep's scan: unattached rows, oldest first.
CREATE INDEX attachments_orphan_idx ON attachments (created_at) WHERE message_id IS NULL;

-- Filename search, exactly the 0006 shape: trigrams over the same fold, so
-- an Arabic keyboard and a Persian one search alike and the limitation is
-- the same honest one (substrings, not stems). pg_trgm exists since 0006.
CREATE INDEX attachments_filename_search_idx
    ON attachments
    USING gin (
        lower(
            translate(filename, U&'\064A\0643\0629\200C', U&'\06CC\06A9\0647')
        ) gin_trgm_ops
    );

-- An image with no caption is an ordinary message, so live content may now
-- be empty; the "text or files, never neither" rule needs the attachments
-- row and is enforced in the send transaction, which is also where
-- attachment ids are claimed. The deleted half is unchanged: erased means
-- erased.
ALTER TABLE messages DROP CONSTRAINT messages_content_shape;
ALTER TABLE messages ADD CONSTRAINT messages_content_shape CHECK (
    (deleted_at IS NULL AND char_length(content) BETWEEN 0 AND 4000)
    OR (deleted_at IS NOT NULL AND content = '')
);
