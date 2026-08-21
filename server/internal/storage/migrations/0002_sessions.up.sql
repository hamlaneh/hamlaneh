-- Session model: one row per refresh-token generation. A login creates a new
-- family; each refresh marks the presented row used and inserts the next row
-- in the same family. Presenting an already-used refresh token means theft or
-- replay — the whole family is revoked (reuse detection). Logout and remote
-- revocation set revoked_at on every row of the family. A "device" in the UI
-- is a family.
ALTER TABLE users
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

CREATE TABLE sessions (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id          uuid        NOT NULL,
    -- SHA-256 of the opaque tokens; raw tokens exist only in cookies.
    refresh_token_hash bytea       NOT NULL UNIQUE
                                   CHECK (octet_length(refresh_token_hash) = 32),
    access_token_hash  bytea       NOT NULL UNIQUE
                                   CHECK (octet_length(access_token_hash) = 32),
    refresh_expires_at timestamptz NOT NULL,
    access_expires_at  timestamptz NOT NULL,
    used_at            timestamptz,
    revoked_at         timestamptz,
    user_agent         text        NOT NULL DEFAULT ''
                                   CHECK (char_length(user_agent) <= 512),
    ip                 inet,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_family_id_idx ON sessions (family_id);
