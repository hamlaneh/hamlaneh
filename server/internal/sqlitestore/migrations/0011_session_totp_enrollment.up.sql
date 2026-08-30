-- SQLite counterpart of 0011_session_totp_enrollment.up.sql. The flag lives on
-- the session rather than being computed per request, which is what makes "at
-- the next sign-in, never mid-session" true rather than aspirational. DEFAULT
-- 0 is the only defensible backfill.
ALTER TABLE sessions
    ADD COLUMN totp_enrollment_required INTEGER NOT NULL DEFAULT 0
        CHECK (totp_enrollment_required IN (0, 1));
