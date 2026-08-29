-- 0017: MLS transport (Phase 3 slice 1, ADR 006).
--
-- Every bytea below is opaque to this server by design: it stores,
-- sequences and delivers MLS artifacts it cannot read, and no query in
-- this schema ever needs to look inside one. The structure that IS
-- server-owned — device identity, group-per-channel, the epoch counter,
-- welcome addressing — exists so that races are settled by keys and
-- compare-and-swap rather than by parsing cryptography.

-- A leaf is a device, not a user, from the first group ever created:
-- one signature key per client instance, because sharing a signature key
-- across devices means exporting private key material between browsers,
-- and retrofitting device-ness later is a state migration inside every
-- user's browser storage. The (user_id, signature_public_key) key is
-- registration's idempotency rule.
CREATE TABLE mls_devices (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    signature_public_key bytea NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, signature_public_key)
);

-- Single-use by protocol: a claim deletes the row in the same
-- transaction that returns it, so handing one package to two adders is
-- impossible rather than forbidden. Publishing is replace-all per
-- device — packages embed an expiry the server cannot read, so the
-- client replaces the pool on connect instead of the server guessing at
-- staleness.
CREATE TABLE mls_key_packages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id  uuid NOT NULL REFERENCES mls_devices (id) ON DELETE CASCADE,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mls_key_packages_device_idx ON mls_key_packages (device_id);

-- One group per channel, and the PRIMARY KEY on channel_id is the
-- create-race arbiter: two clients racing to create both insert, one
-- wins, the loser gets a unique violation the handler answers as 409 —
-- no lock, no read-then-write window. epoch is the sequencing claim the
-- commit CAS advances; it counts accepted commits and asserts nothing
-- cryptographic.
-- No creator or sender attribution columns, deliberately: no request in
-- the contract carries a device id the server could verify, and a column
-- filled from an unverifiable claim — or left NULL forever — is schema
-- asserting knowledge the server does not have. The session identifies
-- the user on every call; that is the attribution that is true.
CREATE TABLE mls_groups (
    channel_id uuid PRIMARY KEY REFERENCES channels (id) ON DELETE CASCADE,
    group_id   bytea NOT NULL UNIQUE,
    epoch      bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The durable commit log — unlike the WS replay buffer, because a device
-- offline for a week must still be able to advance from its epoch to the
-- current one. The (channel_id, epoch) PRIMARY KEY makes first-wins
-- structural: the CAS on mls_groups.epoch decides the winner, and this
-- key makes accepting two commits at one epoch impossible even if the
-- CAS were bypassed. Retention is unbounded in this slice; pruning
-- requires a device-eviction policy and is deliberately deferred with
-- the multi-device slice.
-- epoch here is the epoch the group REACHED by this commit (submitted
-- epoch plus one): the only numbering under which after_epoch=<mine>
-- returns exactly what a client has not applied, and a fresh group at 0
-- has an empty log.
CREATE TABLE mls_commits (
    channel_id uuid NOT NULL REFERENCES mls_groups (channel_id) ON DELETE CASCADE,
    epoch      bigint NOT NULL,
    message    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, epoch)
);

-- Welcomes are stored in the same transaction as the commit that adds
-- their recipients — a committed add whose Welcome was lost is a forked
-- group. Rows survive until the recipient acknowledges after joining
-- (fetch-then-crash must find the Welcome still there), and cascade away
-- with the device or the group.
CREATE TABLE mls_welcomes (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id          uuid NOT NULL REFERENCES mls_groups (channel_id) ON DELETE CASCADE,
    recipient_device_id uuid NOT NULL REFERENCES mls_devices (id) ON DELETE CASCADE,
    welcome             bytea NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mls_welcomes_recipient_idx ON mls_welcomes (recipient_device_id);

-- Fixed at creation, never toggled: flipping it on a live conversation
-- in either direction is the silent mode-switch the downgrade test
-- forbids. The write path enforces the boundary both ways — an e2ee
-- channel refuses plaintext, a plaintext channel refuses ciphertext.
ALTER TABLE channels
    ADD COLUMN e2ee boolean NOT NULL DEFAULT false;

-- The encrypted half of a message: present exactly together, and content
-- is '' whenever they are present, so the searchable column never holds
-- anything the server could read. Soft delete erases mls_ciphertext
-- exactly as it erases content — a deleted message keeps its place and
-- loses its words in both worlds.
ALTER TABLE messages
    ADD COLUMN mls_epoch bigint,
    ADD COLUMN mls_ciphertext bytea,
    ADD CONSTRAINT messages_mls_both_or_neither
        CHECK ((mls_epoch IS NULL) = (mls_ciphertext IS NULL));
