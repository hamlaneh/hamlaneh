-- SQLite counterpart of 0014_scim.up.sql: the columns a provider's sync
-- engine needs, and the tokens it authenticates with.
--
-- PostgreSQL also drops users.password_hash's NOT NULL here, because accounts
-- a provider creates have no password. SQLite cannot alter a column, and no
-- SQLite database ever held the NOT NULL form, so 0001 in this tree declares
-- the column nullable from the start. Readers still see '' rather than NULL:
-- the canonical projection COALESCEs it, exactly as on PostgreSQL.

-- The provider's own identifier for this person, and what marks an account as
-- directory-managed. Deliberately NOT assumed equal to the OIDC subject.
ALTER TABLE users
    ADD COLUMN scim_external_id TEXT
        CHECK (scim_external_id IS NULL OR length(scim_external_id) <= 255);

-- The provider's userName, stored verbatim so a round trip returns what was
-- sent. Usually an email address, which cannot satisfy the local username
-- rules — those stay as they are.
ALTER TABLE users
    ADD COLUMN scim_user_name TEXT COLLATE CITEXT
        CHECK (scim_user_name IS NULL OR length(scim_user_name) <= 320);

-- PostgreSQL spells the uniqueness of the two columns above as a column
-- constraint; SQLite's ALTER TABLE ADD COLUMN cannot carry UNIQUE, and a
-- unique index is the same object. The names match the PostgreSQL constraint
-- names so a conflict reads the same in both trees.
CREATE UNIQUE INDEX users_scim_external_id_key ON users (scim_external_id)
    WHERE scim_external_id IS NOT NULL;
CREATE UNIQUE INDEX users_scim_user_name_key ON users (scim_user_name)
    WHERE scim_user_name IS NOT NULL;

-- Provisioning credentials, the invite shape exactly: shown once, only the
-- digest rests here. Several live tokens at once is deliberate, because
-- rotating a credential an external system holds needs an overlap.
CREATE TABLE scim_tokens (
    id           TEXT PRIMARY KEY,
    token_hash   BLOB NOT NULL UNIQUE
                      CHECK (length(token_hash) = 32),
    note         TEXT NOT NULL DEFAULT ''
                      CHECK (length(note) <= 200),
    -- RESTRICT, not CASCADE: an administrator leaving must not silently take
    -- a working provisioning credential with them.
    created_by   TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at   TEXT NOT NULL,
    -- Null until first use, which is how a configured token is told apart
    -- from one that was minted and forgotten.
    last_used_at TEXT,
    revoked_at   TEXT
);

CREATE INDEX scim_tokens_live_idx ON scim_tokens (token_hash)
    WHERE revoked_at IS NULL;
