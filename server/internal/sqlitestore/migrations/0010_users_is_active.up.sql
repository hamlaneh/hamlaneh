-- SQLite counterpart of 0010_users_is_active.up.sql: the offboarding switch.
-- DEFAULT 1 (true) is the only defensible backfill — a migration must not
-- lock anybody out.
ALTER TABLE users
    ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1
        CHECK (is_active IN (0, 1));

-- The org-settings screen counts accounts without a second factor, and the
-- last-admin rule counts admins who can still sign in.
CREATE INDEX users_active_admins_idx ON users (is_admin)
    WHERE is_active = 1 AND is_admin = 1;
