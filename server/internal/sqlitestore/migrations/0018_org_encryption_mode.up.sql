-- SQLite counterpart of 0018_org_encryption_mode.up.sql (Phase 3, ADR 011).
--
-- One column on the one-row settings table, and nothing else. In particular
-- NOTHING here touches channels or messages: that this migration cannot reach
-- an existing conversation is decision 2's entire point.
--
-- DEFAULT 'strict' covers both populations at once. 'compliance' is in the
-- CHECK from the first day even though the API refuses to select it.
ALTER TABLE org_settings
    ADD COLUMN encryption_mode TEXT NOT NULL DEFAULT 'strict'
        CHECK (encryption_mode IN ('strict', 'compliance'));
