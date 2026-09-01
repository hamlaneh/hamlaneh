-- SQLite counterpart of 0019_mls_backups.up.sql (Phase 3, ADR 010).
--
-- One row per account, and the server can read exactly two things in it: the
-- counter and the timestamp. The envelope is sealed under a key derived from
-- a recovery key generated on the device and never sent here.
--
-- The counter is mirrored out of the envelope's authenticated header purely so
-- a write can be refused without opening anything. It is NOT the anti-rollback
-- control, which is client-side by necessity.
CREATE TABLE mls_backups (
    user_id    TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    envelope   BLOB NOT NULL,
    counter    INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
