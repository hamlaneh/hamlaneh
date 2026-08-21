-- Conversation model per ADR 001: the instance is the organization, so there
-- is no org_id and no team layer anywhere below. Channels are flat — 'public',
-- 'private' or 'dm' — and in Phase 1.2 visibility is membership for every
-- kind; `kind` is stored so a channel directory and join flow can arrive
-- later without a schema change. Direct messages are 1:1, canonicalized so
-- the database, not the application, guarantees one DM per user pair.
--
-- Foreign-key policy, applied deliberately throughout:
--   * content and conversation identity use ON DELETE RESTRICT — a message's
--     author and a DM's two participants must never vanish out from under the
--     history. Offboarding a person deactivates the account (Phase 1.4); it
--     does not delete their words or the other party's copy of them.
--   * per-user state (membership, read positions, mentions) uses ON DELETE
--     CASCADE — it is not content and has no meaning without its user.
--   * attribution that is merely nice to have (who created a channel, who
--     added a member, who deleted a message) uses ON DELETE SET NULL.
--
-- Full-text search: message search ships in this phase, but this migration
-- deliberately adds NO tsvector column and NO FTS/GIN index. The choice of
-- text-search configuration is language-dependent (the product is bilingual
-- en/fa and Postgres has no stock Persian configuration), it is irreversible
-- in practice once an index is built on it, and it belongs with the search
-- implementation that has to live with it. The index arrives in its own
-- migration alongside that code.

CREATE TABLE channels (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text        NOT NULL
                             CHECK (kind IN ('public', 'private', 'dm')),
    -- The name rendered after '#'. citext matches users.username so casing can
    -- never fork one channel into two; the pattern keeps slugs URL-safe.
    slug         citext      CHECK (slug IS NULL
                                    OR (char_length(slug) BETWEEN 2 AND 64
                                        AND slug ~ '^[a-z0-9][a-z0-9_-]*$')),
    topic        text        NOT NULL DEFAULT ''
                             CHECK (char_length(topic) <= 250),
    -- The two participants of a direct message, canonicalized as a < b so the
    -- unique index below admits exactly one DM per unordered pair.
    dm_user_a    uuid        REFERENCES users (id) ON DELETE RESTRICT,
    dm_user_b    uuid        REFERENCES users (id) ON DELETE RESTRICT,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

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
    channel_id uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    joined_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, user_id)
);

-- "Every channel I am in" — the sidebar's query, and the membership join that
-- scopes search. The primary key already covers the other direction.
CREATE INDEX channel_members_user_id_idx ON channel_members (user_id);

CREATE TABLE messages (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id    uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    author_id     uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Client-generated per message and reused verbatim on every retry, so a
    -- message queued while the socket was down lands exactly once.
    client_msg_id uuid        NOT NULL,
    -- Markdown source, never HTML. Deletion is soft: the row keeps its place
    -- with its content erased, because the design draws a placeholder there.
    content       text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    edited_at     timestamptz,
    deleted_at    timestamptz,
    -- Who deleted it. An admin may delete another member's message, and that
    -- power needs a name attached until the Phase 1.4 audit log exists.
    deleted_by    uuid        REFERENCES users (id) ON DELETE SET NULL,

    CONSTRAINT messages_content_shape CHECK (
        (deleted_at IS NULL AND char_length(content) BETWEEN 1 AND 4000)
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
    message_id        uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    mentioned_user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    PRIMARY KEY (message_id, mentioned_user_id)
);

-- Rows are written server-side when a message is sent or edited; the sidebar's
-- filled "@" badge counts them. Parsing mentions on read would mean re-parsing
-- every unread message on every sidebar load.
CREATE INDEX message_mentions_user_id_idx ON message_mentions (mentioned_user_id);

CREATE TABLE channel_read_positions (
    channel_id           uuid        NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    user_id              uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    last_read_message_id uuid        NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    -- The read message's created_at, copied here so unread counting compares
    -- (created_at, id) tuples straight against messages_channel_created_idx
    -- instead of joining back to resolve the anchor's timestamp. It is also
    -- what makes the monotonic check a plain column comparison: a position
    -- older than the stored one is accepted and ignored, so a stale tab can
    -- never mark a channel unread again.
    last_read_at         timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, user_id)
);
