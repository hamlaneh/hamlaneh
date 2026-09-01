-- SQLite counterpart of 0017_mls_transport.up.sql (Phase 3 slice 1, ADR 006).
--
-- Every BLOB below is opaque to this server by design, in both drivers alike:
-- it stores, sequences and delivers MLS artifacts it cannot read, and no
-- query needs to look inside one.

-- A leaf is a device, not a user. The (user_id, signature_public_key) key is
-- registration's idempotency rule.
CREATE TABLE mls_devices (
    id                   TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    signature_public_key BLOB NOT NULL,
    created_at           TEXT NOT NULL,
    UNIQUE (user_id, signature_public_key)
);

-- Single-use by protocol: a claim deletes the row in the same transaction
-- that returns it, so handing one package to two adders is impossible rather
-- than forbidden.
CREATE TABLE mls_key_packages (
    id         TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL REFERENCES mls_devices (id) ON DELETE CASCADE,
    data       BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX mls_key_packages_device_idx ON mls_key_packages (device_id);

-- One group per channel, and the PRIMARY KEY on channel_id is the create-race
-- arbiter: two clients racing to create both insert, one wins, the loser gets
-- a unique violation the handler answers as 409 — no lock, no read-then-write
-- window, on either driver.
CREATE TABLE mls_groups (
    channel_id TEXT PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    group_id   BLOB NOT NULL UNIQUE,
    epoch      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- The durable commit log. The (channel_id, epoch) PRIMARY KEY makes
-- first-wins structural even if the CAS on mls_groups.epoch were bypassed.
-- epoch here is the epoch the group REACHED by this commit.
CREATE TABLE mls_commits (
    channel_id TEXT NOT NULL REFERENCES mls_groups (channel_id) ON DELETE CASCADE,
    epoch      INTEGER NOT NULL,
    message    BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (channel_id, epoch)
);

-- Welcomes are stored in the same transaction as the commit that adds their
-- recipients — a committed add whose Welcome was lost is a forked group.
CREATE TABLE mls_welcomes (
    id                  TEXT PRIMARY KEY,
    channel_id          TEXT NOT NULL REFERENCES mls_groups (channel_id) ON DELETE CASCADE,
    recipient_device_id TEXT NOT NULL REFERENCES mls_devices (id) ON DELETE CASCADE,
    welcome             BLOB NOT NULL,
    created_at          TEXT NOT NULL
);

CREATE INDEX mls_welcomes_recipient_idx ON mls_welcomes (recipient_device_id);

-- Fixed at creation, never toggled: flipping it on a live conversation in
-- either direction is the silent mode-switch the downgrade test forbids.
ALTER TABLE channels
    ADD COLUMN e2ee INTEGER NOT NULL DEFAULT 0
        CHECK (e2ee IN (0, 1));

-- The encrypted half of a message: present exactly together, and content is
-- '' whenever they are present, so the searchable column never holds anything
-- the server could read.
ALTER TABLE messages ADD COLUMN mls_epoch INTEGER;
ALTER TABLE messages ADD COLUMN mls_ciphertext BLOB;

-- PostgreSQL states the both-or-neither rule as a CHECK constraint added over
-- the two columns above. SQLite can add neither a constraint nor a CHECK to
-- an existing table, and folding the rule into 0003 would mean moving Phase 3
-- columns into the Phase 1 migration and breaking the side-by-side reading.
--
-- So it is a pair of triggers instead, which is what SQLite offers for an
-- invariant a CHECK cannot carry: the same rule, enforced on the same two
-- paths a CHECK covers, aborting with the constraint's own name so a caller
-- sees the same failure. It is classified as a constraint violation by
-- errors.go, which treats the trigger result code alongside the CHECK one.
CREATE TRIGGER messages_mls_both_or_neither_insert
BEFORE INSERT ON messages
WHEN (NEW.mls_epoch IS NULL) <> (NEW.mls_ciphertext IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'CHECK constraint failed: messages_mls_both_or_neither');
END;

CREATE TRIGGER messages_mls_both_or_neither_update
BEFORE UPDATE ON messages
WHEN (NEW.mls_epoch IS NULL) <> (NEW.mls_ciphertext IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'CHECK constraint failed: messages_mls_both_or_neither');
END;
