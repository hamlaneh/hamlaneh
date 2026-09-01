-- Phase 1.4: invitations, instance settings, and the audit log.
--
-- Deactivation needs no column of its own: users.is_active already exists
-- (0001) and is what the sign-in path checks.

-- Invitations. Only the hash is stored, exactly as password_reset_tokens
-- does it and for the same reason: a stolen database must yield no usable
-- invitation, and nothing can redisplay a link somebody closed the dialog
-- on. accepted_at and revoked_at are separate columns rather than one
-- status, because the audit question is "what happened to it", not "is it
-- open" — the partial index below answers the second cheaply anyway.
CREATE TABLE invites (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  bytea       NOT NULL UNIQUE,
    created_by  uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    note        text        NOT NULL DEFAULT '' CHECK (char_length(note) <= 200),
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    accepted_at timestamptz,
    accepted_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    revoked_at  timestamptz,

    CONSTRAINT invites_accepted_shape CHECK ((accepted_at IS NULL) = (accepted_by IS NULL)),
    -- An invite cannot be both spent and revoked; whichever happened first
    -- is the one that happened.
    CONSTRAINT invites_outcome_shape CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);

-- The table's own question: what can still be redeemed, soonest expiry
-- first. Partial, because spent and revoked invites are history the log
-- keeps and the list never shows.
CREATE INDEX invites_open_idx ON invites (expires_at, id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Instance settings: exactly one row, and the CHECK is what makes "exactly
-- one" a fact rather than a convention every reader has to trust.
CREATE TABLE org_settings (
    id                     boolean     PRIMARY KEY DEFAULT true CHECK (id),
    org_name               text        NOT NULL DEFAULT 'Hamlaneh'
                                       CHECK (char_length(org_name) BETWEEN 1 AND 64),
    default_locale         text        NOT NULL DEFAULT 'en'
                                       CHECK (default_locale IN ('en', 'fa')),
    -- invite, not open: registration is closed unless somebody deliberately
    -- opens it, which is the secure-by-default rule this product carries
    -- everywhere else too.
    registration_mode      text        NOT NULL DEFAULT 'invite'
                                       CHECK (registration_mode IN ('invite', 'open')),
    require_totp           boolean     NOT NULL DEFAULT false,
    session_lifetime_hours integer     NOT NULL DEFAULT 720
                                       CHECK (session_lifetime_hours BETWEEN 1 AND 8760),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

INSERT INTO org_settings (id) VALUES (true);

-- The audit log: append-only and hash-chained.
--
-- Each row carries the hash of the row before it, so editing or deleting
-- one breaks verification from that point forward. That is tamper-EVIDENT,
-- not tamper-proof: somebody with database access can still rewrite the
-- whole chain, and the honest claim is that they cannot do it invisibly
-- without also holding the HMAC key the server keeps outside the database.
--
-- seq is the chain's order, and it is a bigint identity rather than a
-- timestamp because two entries can share a microsecond and a chain needs a
-- total order. There is deliberately no UPDATE or DELETE path in the
-- application; the constraint below is what stops a careless one being
-- written later.
CREATE TABLE audit_entries (
    seq          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id           uuid        NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    action       text        NOT NULL CHECK (char_length(action) BETWEEN 1 AND 64),
    -- Null for something the system did rather than a person. RESTRICT
    -- rather than SET NULL: an actor must not become anonymous because
    -- their account was removed — that is precisely the record worth having.
    actor_id     uuid        REFERENCES users (id) ON DELETE RESTRICT,
    target_id    uuid,
    -- What the target was called at the time. Kept as text, unreferenced,
    -- so the log still reads correctly after a rename or a deletion.
    target_label text,
    detail       jsonb,
    ip           inet,
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    -- The chain: sha256 over this entry's fields plus prev_hash, keyed by
    -- the server's HMAC key. The first entry's prev_hash is 32 zero bytes.
    prev_hash    bytea       NOT NULL CHECK (octet_length(prev_hash) = 32),
    entry_hash   bytea       NOT NULL UNIQUE CHECK (octet_length(entry_hash) = 32)
);

-- The filter bar's two filters, and the newest-first paging under them.
CREATE INDEX audit_entries_occurred_idx ON audit_entries (occurred_at DESC, seq DESC);
CREATE INDEX audit_entries_action_idx   ON audit_entries (action, occurred_at DESC);
CREATE INDEX audit_entries_actor_idx    ON audit_entries (actor_id, occurred_at DESC)
    WHERE actor_id IS NOT NULL;
