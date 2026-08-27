-- SCIM provisioning: the columns an identity provider's sync engine needs,
-- and the tokens it authenticates with.

-- Accounts a provider creates have no password, and accounts single sign-on
-- links may never grow one. NOT NULL was right while an administrator or an
-- invitation was the only way in; it is not once a directory is.
--
-- Readers see '' rather than NULL: the canonical projection COALESCEs it, so
-- the Go type stays a string and every existing reader is untouched. Empty
-- means "no password credential", and Login refuses it explicitly rather
-- than letting a malformed hash reach the verifier.
ALTER TABLE users
    ALTER COLUMN password_hash DROP NOT NULL;

-- The provider's own identifier for this person. It is the correlation
-- handle a sync engine filters on, and it is also what marks an account as
-- directory-managed -- which is the one condition under which single sign-on
-- may attach an identity by email, because then both sides of that match
-- come from an authority an administrator already granted.
--
-- Deliberately NOT assumed equal to the OIDC subject: Okta and Entra both
-- break that in their default configurations.
ALTER TABLE users
    ADD COLUMN scim_external_id text UNIQUE
        CHECK (char_length(scim_external_id) <= 255);

-- The provider's userName, stored verbatim so a round trip returns what was
-- sent. It is usually an email address, which cannot satisfy the local
-- username rules -- those stay as they are, and the local username is
-- derived from this rather than relaxed to accept it.
ALTER TABLE users
    ADD COLUMN scim_user_name citext UNIQUE
        CHECK (char_length(scim_user_name) <= 320);

-- Provisioning credentials. The invite shape exactly: the value is shown
-- once and only its digest rests here, so a stolen database yields nothing
-- that can be presented.
--
-- Several live tokens at once is deliberate. Rotating a credential held by
-- an external system needs an overlap -- mint, reconfigure the provider,
-- revoke -- and forcing one at a time would mean an outage on every rotation.
CREATE TABLE scim_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   bytea       NOT NULL UNIQUE
                             CHECK (octet_length(token_hash) = 32),
    note         text        NOT NULL DEFAULT ''
                             CHECK (char_length(note) <= 200),
    -- RESTRICT, not CASCADE: an administrator leaving must not silently take
    -- a working provisioning credential with them. Revoke it deliberately.
    created_by   uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Null until first use, which is how a configured token is told apart
    -- from one that was minted and forgotten.
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- Authentication looks a token up by digest and cares only about live ones.
CREATE INDEX scim_tokens_live_idx ON scim_tokens (token_hash)
    WHERE revoked_at IS NULL;
