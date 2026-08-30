-- SQLite counterpart of 0003_channels_messages.up.sql. The conversation model,
-- the foreign-key policy (RESTRICT for content and conversation identity,
-- CASCADE for per-user state, SET NULL for attribution) and the DM
-- canonicalization are identical; only the spelling changes.

CREATE TABLE channels (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL
                    CHECK (kind IN ('public', 'private', 'dm')),
    -- The name rendered after '#'. CITEXT matches users.username so casing
    -- can never fork one channel into two. PostgreSQL states the shape with a
    -- regex; SQLite's GLOB is the anchored, case-sensitive equivalent, and
    -- lower() is applied first because PostgreSQL's ~ on a citext column is
    -- case-insensitive.
    slug       TEXT COLLATE CITEXT
                    CHECK (slug IS NULL
                           OR (length(slug) BETWEEN 2 AND 64
                               AND lower(slug) GLOB '[a-z0-9][a-z0-9_-]*')),
    topic      TEXT NOT NULL DEFAULT ''
                    CHECK (length(topic) <= 250),
    -- The two participants of a direct message, canonicalized as a < b so the
    -- unique index below admits exactly one DM per unordered pair.
    dm_user_a  TEXT REFERENCES users (id) ON DELETE RESTRICT,
    dm_user_b  TEXT REFERENCES users (id) ON DELETE RESTRICT,
    created_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    -- A DM has no slug and no topic; every other kind has a slug.
    CONSTRAINT channels_slug_shape CHECK (
        (kind = 'dm' AND slug IS NULL AND topic = '')
        OR (kind <> 'dm' AND slug IS NOT NULL)
    ),
    -- Both participants exist precisely when the channel is a DM.
    CONSTRAINT channels_dm_pair_shape CHECK (
        (kind = 'dm') = (dm_user_a IS NOT NULL AND dm_user_b IS NOT NULL)
    ),
    -- Canonical ordering; also rules out a DM with oneself.
    CONSTRAINT channels_dm_pair_ordered CHECK (
        dm_user_a IS NULL OR dm_user_a < dm_user_b
    )
);

-- Channel names are unique among named channels; DMs (slug NULL) are exempt.
CREATE UNIQUE INDEX channels_slug_key ON channels (slug) WHERE kind <> 'dm';

-- One direct message per user pair — enforced here rather than promised by
-- the application, because a race between two people opening the same DM is
-- the ordinary case, not the exotic one.
CREATE UNIQUE INDEX channels_dm_pair_key
    ON channels (dm_user_a, dm_user_b) WHERE kind = 'dm';

CREATE TABLE channel_members (
    channel_id TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_by   TEXT REFERENCES users (id) ON DELETE SET NULL,
    joined_at  TEXT NOT NULL,

    PRIMARY KEY (channel_id, user_id)
);

-- "Every channel I am in" — the sidebar's query, and the membership join that
-- scopes search. The primary key already covers the other direction.
CREATE INDEX channel_members_user_id_idx ON channel_members (user_id);

CREATE TABLE messages (
    id            TEXT NOT NULL PRIMARY KEY,
    channel_id    TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    author_id     TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Client-generated per message and reused verbatim on every retry, so a
    -- message queued while the socket was down lands exactly once.
    client_msg_id TEXT NOT NULL,
    -- Markdown source, never HTML. Deletion is soft: the row keeps its place
    -- with its content erased, because the design draws a placeholder there.
    content       TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    edited_at     TEXT,
    deleted_at    TEXT,
    deleted_by    TEXT REFERENCES users (id) ON DELETE SET NULL,

    -- The final form of the rule, which PostgreSQL reaches in two steps:
    -- 0007 loosens the live lower bound from 1 to 0 once an image with no
    -- caption is an ordinary message. SQLite cannot alter a constraint and no
    -- SQLite database ever held the stricter form, so it is declared once
    -- here and 0007 records the fold.
    CONSTRAINT messages_content_shape CHECK (
        (deleted_at IS NULL AND length(content) BETWEEN 0 AND 4000)
        OR (deleted_at IS NOT NULL AND content = '')
    ),
    CONSTRAINT messages_deleted_by_shape CHECK (
        deleted_by IS NULL OR deleted_at IS NOT NULL
    ),
    -- The idempotency guarantee: one message per (channel, author, key).
    CONSTRAINT messages_client_msg_id_key UNIQUE (channel_id, author_id, client_msg_id)
);

-- History pages ascending by (created_at, id) in both directions and around a
-- permalink; the same index answers "newest message in this channel" for the
-- sidebar and the unread counts.
CREATE INDEX messages_channel_created_idx ON messages (channel_id, created_at, id);

CREATE TABLE message_mentions (
    message_id        TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    mentioned_user_id TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    PRIMARY KEY (message_id, mentioned_user_id)
);

CREATE INDEX message_mentions_user_id_idx ON message_mentions (mentioned_user_id);

CREATE TABLE channel_read_positions (
    channel_id           TEXT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id              TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    last_read_message_id TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    -- The read message's created_at, copied here so unread counting compares
    -- (created_at, id) tuples straight against messages_channel_created_idx.
    last_read_at         TEXT NOT NULL,
    updated_at           TEXT NOT NULL,

    PRIMARY KEY (channel_id, user_id)
);
