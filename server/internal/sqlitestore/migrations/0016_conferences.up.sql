-- SQLite counterpart of 0016_conferences.up.sql (ADR 005).
--
-- Home mode ships without calls (ADR 012 decision 2): the instance document
-- reports calls false and the call and conference endpoints answer
-- calls_unavailable, so nothing here is ever written in that mode. The table
-- exists all the same, because the schema is the contract's shape and not a
-- deployment's — a driver whose schema forked by mode would make every
-- storage test driver-specific.
CREATE TABLE conferences (
    id              TEXT PRIMARY KEY,

    -- SET NULL rather than CASCADE, matching channels.created_by: a
    -- conference outliving the account that made it is correct, because
    -- somebody else may still be meeting in it.
    created_by      TEXT REFERENCES users (id) ON DELETE SET NULL,

    title           TEXT NOT NULL DEFAULT ''
                         CHECK (length(title) <= 120),

    -- The digest, never the link.
    link_token_hash BLOB NOT NULL UNIQUE
                         CHECK (length(link_token_hash) = 32),

    -- Null means it does not expire, which is the default and is deliberate.
    expires_at      TEXT,

    revoked_at      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX conferences_live_idx ON conferences (link_token_hash)
    WHERE revoked_at IS NULL;

CREATE INDEX conferences_owner_idx ON conferences (created_by, created_at DESC);
