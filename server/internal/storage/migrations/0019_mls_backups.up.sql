-- 0019: the sealed verification backup (Phase 3, ADR 010).
--
-- One row per account, and the server can read exactly two things in it: the
-- counter and the timestamp. The envelope is AES-256-GCM under a key derived
-- from a recovery key that is generated on the device, shown once, and never
-- sent here — so this table is bytes this server cannot open and has no path
-- to opening. That is the point rather than a side effect: an account-recovery
-- feature the operator can use is an account-recovery feature the operator's
-- attacker can use.
--
-- The counter is mirrored out of the envelope's authenticated header purely so
-- a write can be refused without opening anything. It is NOT the anti-rollback
-- control: the client compares the SEALED counter against a floor it keeps
-- itself, which is the comparison a lying server cannot pass. What this column
-- buys is the ordinary case — two of the owner's own devices writing out of
-- order — and nothing more, which is why the check is a plain > and not a
-- ceremony.
CREATE TABLE mls_backups (
    user_id    uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    envelope   bytea NOT NULL,
    counter    bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
