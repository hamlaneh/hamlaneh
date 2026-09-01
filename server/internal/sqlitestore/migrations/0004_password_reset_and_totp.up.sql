-- SQLite counterpart of 0004_password_reset_and_totp.up.sql. The token-storage
-- posture is unchanged: high-entropy secrets rest as SHA-256 digests, recovery
-- codes as argon2id hashes, and the TOTP secret raw because verification needs
-- the plaintext.

CREATE TABLE password_reset_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE
                    CHECK (length(token_hash) = 32),
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    created_at TEXT NOT NULL,
    CHECK (used_at IS NULL OR used_at >= created_at)
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

CREATE TABLE user_totp (
    user_id          TEXT    PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    -- Raw RFC 6238 secret, 160 bits.
    secret           BLOB    NOT NULL
                             CHECK (length(secret) = 20),
    verified_at      TEXT,
    activated_at     TEXT,
    setup_expires_at TEXT    NOT NULL,
    verify_attempts  INTEGER NOT NULL DEFAULT 0
                             CHECK (verify_attempts >= 0),
    -- Highest accepted time step, so an accepted code is never accepted twice
    -- even inside its thirty-second window (RFC 6238 section 5.2).
    last_used_step   INTEGER,
    created_at       TEXT    NOT NULL,

    CONSTRAINT user_totp_activated_requires_verified CHECK (
        activated_at IS NULL OR verified_at IS NOT NULL
    )
);

CREATE TABLE user_recovery_codes (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- argon2id, in the internal/password format — deliberately NOT SHA-256.
    code_hash  TEXT NOT NULL,
    used_at    TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX user_recovery_codes_user_id_idx ON user_recovery_codes (user_id);

CREATE TABLE totp_challenges (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  BLOB    NOT NULL UNIQUE
                        CHECK (length(token_hash) = 32),
    expires_at  TEXT    NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0
                        CHECK (attempts >= 0),
    consumed_at TEXT,
    created_at  TEXT    NOT NULL
);

CREATE INDEX totp_challenges_user_id_idx ON totp_challenges (user_id);
