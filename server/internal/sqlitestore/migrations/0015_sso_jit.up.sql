-- SQLite counterpart of 0015_sso_jit.up.sql. DEFAULT 0 is the point rather
-- than a cautious default: while it is off the account-creating branch does
-- not run at all, which is what makes "single sign-on cannot walk around
-- registration being closed" true by construction.
ALTER TABLE org_settings
    ADD COLUMN sso_jit_provisioning INTEGER NOT NULL DEFAULT 0
        CHECK (sso_jit_provisioning IN (0, 1));
