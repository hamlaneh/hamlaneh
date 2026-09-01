-- Phase 1.3: link previews, per ADR 003.
--
-- A preview is a nicety the server derived from a message, not something
-- anybody wrote, so it lives beside the message rather than in it: the
-- messages row keeps meaning exactly what its author typed, and enrichment
-- can arrive, be replaced or be dropped without ever touching content or
-- edited_at. At most one preview per message — the first http(s) URL in the
-- content — which is why message_id is the primary key rather than a plain
-- foreign key.
--
-- ON DELETE CASCADE, unlike attachments' SET NULL: an attachment is a file
-- somebody uploaded and losing it silently would be losing their data, while
-- a preview is regenerable from the message's own text. If a hard delete
-- ever arrives, the safe failure here is the card going away with the
-- message it described.
--
-- image_blob_id names a derivative on the filesystem, written by the same
-- blob store as attachment thumbnails and served from the files origin. It
-- is deliberately not a foreign key: the blob store is not a table, and the
-- honest failure for a missing blob is a card with no image, which is what
-- a NULL here already means. Cleaning up the blob when this row goes is the
-- sweep's job, not a constraint's.
--
-- The caps mirror the ones the fetcher enforces, so a row that somehow
-- bypassed the Go side still cannot carry a page's worth of text into every
-- history response.
CREATE TABLE link_previews (
    message_id    uuid        PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,

    url           text        NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    title         text        CHECK (char_length(title) BETWEEN 1 AND 200),
    description   text        CHECK (char_length(description) BETWEEN 1 AND 500),
    image_blob_id uuid,

    fetched_at    timestamptz NOT NULL DEFAULT now()
);

-- No index beyond the primary key: every read of this table is a lookup by
-- message id — one message, or one page's worth via = ANY — and the primary
-- key already serves both.
