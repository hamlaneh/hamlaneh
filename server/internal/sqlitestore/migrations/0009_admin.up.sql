-- SQLite counterpart of 0009_admin.up.sql: invitations, instance settings,
-- and the hash-chained audit log.

CREATE TABLE invites (
    id          TEXT PRIMARY KEY,
    token_hash  BLOB NOT NULL UNIQUE,
    created_by  TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    note        TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    accepted_at TEXT,
    accepted_by TEXT REFERENCES users (id) ON DELETE SET NULL,
    revoked_at  TEXT,

    CONSTRAINT invites_accepted_shape CHECK ((accepted_at IS NULL) = (accepted_by IS NULL)),
    -- An invite cannot be both spent and revoked.
    CONSTRAINT invites_outcome_shape CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);

-- What can still be redeemed, soonest expiry first.
CREATE INDEX invites_open_idx ON invites (expires_at, id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Instance settings: exactly one row, and the CHECK is what makes "exactly
-- one" a fact rather than a convention. PostgreSQL uses a boolean primary key
-- pinned to true; SQLite has no boolean, so the same idea is an integer key
-- pinned to 1.
CREATE TABLE org_settings (
    id                     INTEGER PRIMARY KEY CHECK (id = 1),
    org_name               TEXT    NOT NULL DEFAULT 'Hamlaneh'
                                   CHECK (length(org_name) BETWEEN 1 AND 64),
    default_locale         TEXT    NOT NULL DEFAULT 'en'
                                   CHECK (default_locale IN ('en', 'fa')),
    -- invite, not open: registration is closed unless somebody deliberately
    -- opens it.
    registration_mode      TEXT    NOT NULL DEFAULT 'invite'
                                   CHECK (registration_mode IN ('invite', 'open')),
    require_totp           INTEGER NOT NULL DEFAULT 0
                                   CHECK (require_totp IN (0, 1)),
    session_lifetime_hours INTEGER NOT NULL DEFAULT 720
                                   CHECK (session_lifetime_hours BETWEEN 1 AND 8760),
    -- The one default clock reading in this tree: the row below is inserted
    -- by the migration itself, with no Go caller to bind a timestamp. The
    -- expression renders exactly the layout codec.go defines, six fractional
    -- digits included, because a value of a different width would not compare
    -- correctly against the ones the driver writes.
    updated_at             TEXT    NOT NULL
                                   DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z','now'))
);

INSERT INTO org_settings (id) VALUES (1);

-- The audit log: append-only and hash-chained. Tamper-EVIDENT, not
-- tamper-proof — see the PostgreSQL file for the honest claim.
--
-- seq is the chain's total order. PostgreSQL uses GENERATED ALWAYS AS
-- IDENTITY; SQLite's INTEGER PRIMARY KEY AUTOINCREMENT carries the two
-- properties the chain actually needs — monotonic, and never reused even
-- after a delete.
CREATE TABLE audit_entries (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL UNIQUE,
    action       TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    -- Null for something the system did rather than a person. RESTRICT
    -- rather than SET NULL: an actor must not become anonymous.
    actor_id     TEXT REFERENCES users (id) ON DELETE RESTRICT,
    target_id    TEXT,
    -- What the target was called at the time, unreferenced so the log still
    -- reads correctly after a rename or a deletion.
    target_label TEXT,
    detail       TEXT,
    ip           TEXT,
    occurred_at  TEXT NOT NULL,
    -- The chain: sha256 over this entry's fields plus prev_hash, keyed by the
    -- server's HMAC key. The first entry's prev_hash is 32 zero bytes.
    prev_hash    BLOB NOT NULL CHECK (length(prev_hash) = 32),
    entry_hash   BLOB NOT NULL UNIQUE CHECK (length(entry_hash) = 32)
);

-- The filter bar's two filters, and the newest-first paging under them.
CREATE INDEX audit_entries_occurred_idx ON audit_entries (occurred_at DESC, seq DESC);
CREATE INDEX audit_entries_action_idx   ON audit_entries (action, occurred_at DESC);
CREATE INDEX audit_entries_actor_idx    ON audit_entries (actor_id, occurred_at DESC)
    WHERE actor_id IS NOT NULL;
