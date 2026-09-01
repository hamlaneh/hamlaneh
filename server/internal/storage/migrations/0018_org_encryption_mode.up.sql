-- 0018: the organisation encryption mode (Phase 3, ADR 011).
--
-- One column on the one-row settings table, and nothing else. In particular
-- NOTHING here touches channels or messages: that this migration cannot
-- reach an existing conversation is decision 2's entire point. The mode sets
-- what conversations are BORN as; channels.e2ee stays the birth certificate,
-- fixed at creation and never toggled (migration 0017), so no mode switch
-- has a code path it could ride to decrypt or expose history.
--
-- DEFAULT 'strict' covers both populations at once: a fresh install and an
-- instance migrated from Phase 2 both come up in this product's secure
-- posture with no setup question to answer. Conversations that predate it
-- are left exactly as they are — usable, labeled, and counted for the admin
-- rather than converted.
--
-- 'compliance' is in the CHECK from the first day even though the API
-- refuses to select it: the value's shape is final, so the column does not
-- churn when encryption at rest, retention and export land and unlock it.
ALTER TABLE org_settings
    ADD COLUMN encryption_mode text NOT NULL DEFAULT 'strict'
        CHECK (encryption_mode IN ('strict', 'compliance'));
