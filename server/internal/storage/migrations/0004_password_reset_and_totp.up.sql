-- Phase 1.1b credential surface: password reset, TOTP two-step verification,
-- recovery codes, and the half-authenticated login challenge.
--
-- Token storage follows the sessions pattern wherever the secret is high
-- entropy: 256-bit reset tokens and login challenges are stored only as
-- SHA-256 digests, so a database leak yields nothing usable.
--
-- Recovery codes are the deliberate exception. At roughly 40 bits each, a bare
-- digest is brute-forceable offline in minutes, so they are argon2id hashes in
-- the same format as users.password_hash.
--
-- The TOTP secret cannot be hashed at all — verification needs the plaintext —
-- so it is stored raw. Encrypting it at rest needs a key-management decision
-- (where the key lives across restarts and backups) and would arrive as its own
-- migration; recorded in ROADMAP as a Phase 5 item rather than half-done here.
--
-- The sessions table needs no change: user_agent and ip are already recorded
-- per generation, so listing devices, revoking one family, and revoking all
-- others are queries over columns that exist. What the settings screen wants
-- beyond them is runtime, not schema — approximate location comes from a
-- geolocation lookup, and the device label is parsed client-side from the raw
-- user agent.

CREATE TABLE password_reset_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the opaque emailed token; the raw value exists only in the
    -- link that reaches the mailbox.
    token_hash bytea       NOT NULL UNIQUE
                           CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (used_at IS NULL OR used_at >= created_at)
);

-- "Every outstanding token of this user": issuing a new one invalidates the
-- previous, and completing a reset consumes them all.
CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

CREATE TABLE user_totp (
    user_id          uuid        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    -- Raw RFC 6238 secret, 160 bits. The base32 manual key is rendered in code.
    secret           bytea       NOT NULL
                                 CHECK (octet_length(secret) = 20),
    -- Lifecycle: the row is a pending setup until activated_at is set. A
    -- pending setup expires, or is replaced by starting again; activation
    -- requires a prior verification, which the constraint below enforces so
    -- no code path can switch two-step verification on for an account that
    -- never proved its authenticator.
    verified_at      timestamptz,
    activated_at     timestamptz,
    setup_expires_at timestamptz NOT NULL,
    -- Wrong codes burned during setup step 2; the setup is revoked at the cap,
    -- because an uncapped verifier is a brute-force oracle.
    verify_attempts  integer     NOT NULL DEFAULT 0
                                 CHECK (verify_attempts >= 0),
    -- Highest accepted time step, so an accepted code is never accepted twice
    -- even inside its thirty-second window (RFC 6238 section 5.2).
    last_used_step   bigint,
    created_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT user_totp_activated_requires_verified CHECK (
        activated_at IS NULL OR verified_at IS NOT NULL
    )
);

CREATE TABLE user_recovery_codes (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- argon2id, in the internal/password format — deliberately NOT SHA-256.
    code_hash  text        NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- "The caller's unused codes": sign-in walks them, the Security card counts
-- them, regeneration replaces the whole set.
CREATE INDEX user_recovery_codes_user_id_idx ON user_recovery_codes (user_id);

-- The half-authenticated state between the password step and the code step.
-- A challenge is minted only after the password verified, carries no authority
-- anywhere except the one endpoint that completes the sign-in, and dies by
-- TTL, by consumption, or at the wrong-code cap. One live challenge per user:
-- minting a new one consumes the previous.
CREATE TABLE totp_challenges (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the opaque token carried by the challenge cookie.
    token_hash  bytea       NOT NULL UNIQUE
                            CHECK (octet_length(token_hash) = 32),
    expires_at  timestamptz NOT NULL,
    attempts    integer     NOT NULL DEFAULT 0
                            CHECK (attempts >= 0),
    consumed_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX totp_challenges_user_id_idx ON totp_challenges (user_id);
