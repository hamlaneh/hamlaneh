-- SQLite counterpart of 0002_sessions.up.sql. The session model is unchanged:
-- one row per refresh-token generation, reuse detection revokes the family.
ALTER TABLE users
    ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0
        CHECK (must_change_password IN (0, 1));

CREATE TABLE sessions (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id          TEXT NOT NULL,
    -- SHA-256 of the opaque tokens; raw tokens exist only in cookies.
    refresh_token_hash BLOB NOT NULL UNIQUE
                            CHECK (length(refresh_token_hash) = 32),
    access_token_hash  BLOB NOT NULL UNIQUE
                            CHECK (length(access_token_hash) = 32),
    refresh_expires_at TEXT NOT NULL,
    access_expires_at  TEXT NOT NULL,
    used_at            TEXT,
    revoked_at         TEXT,
    user_agent         TEXT NOT NULL DEFAULT ''
                            CHECK (length(user_agent) <= 512),
    ip                 TEXT,
    created_at         TEXT NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_family_id_idx ON sessions (family_id);
