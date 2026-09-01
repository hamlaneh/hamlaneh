-- Conference rooms: a link anybody holding it can join, including somebody
-- with no account on this instance.
--
-- That is the feature, and it is also the widest door in the product, which
-- is why it lands last -- after every gate it must respect is built and
-- tested. What confines it is that the link buys exactly one media room: no
-- session, no account, and no other endpoint honours it.
CREATE TABLE conferences (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- SET NULL rather than CASCADE, matching channels.created_by: a
    -- conference outliving the account that made it is correct, because
    -- somebody else may still be meeting in it. An administrator can still
    -- revoke it -- ownership is not the only way to reach it.
    created_by      uuid        REFERENCES users (id) ON DELETE SET NULL,

    title           text        NOT NULL DEFAULT ''
                                CHECK (char_length(title) <= 120),

    -- The digest, never the link. Same posture as invitations and
    -- provisioning tokens: a stolen database yields nothing presentable.
    link_token_hash bytea       NOT NULL UNIQUE
                                CHECK (octet_length(link_token_hash) = 32),

    -- Null means it does not expire, which is the default and is deliberate
    -- (ADR 005): the common case is a standing weekly link, and one that
    -- dies unannounced drives people to mint a fresh link per meeting and
    -- paste it into more places than the last.
    expires_at      timestamptz,

    revoked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Redeeming a link looks it up by digest and cares only about live ones.
CREATE INDEX conferences_live_idx ON conferences (link_token_hash)
    WHERE revoked_at IS NULL;

-- Listing is either "mine" or, for an administrator, "all", newest first.
CREATE INDEX conferences_owner_idx ON conferences (created_by, created_at DESC);
