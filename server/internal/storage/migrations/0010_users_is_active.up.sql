-- Deactivation, the offboarding switch behind PATCH /api/v1/admin/users/{id}.
--
-- 0009's header says "users.is_active already exists (0001)". It does not:
-- 0001 created the users table without it and no migration since added one,
-- so the column the whole deactivation path is specified against was never
-- written. This adds it, and nothing else.
--
-- DEFAULT true is the only defensible backfill: every account that exists
-- today can sign in, and a migration must not lock anybody out.
ALTER TABLE users
    ADD COLUMN is_active boolean NOT NULL DEFAULT true;

-- The org-settings screen counts accounts without a second factor, and the
-- last-admin rule counts admins who can still sign in. Both are "active
-- users", which is what this index answers.
CREATE INDEX users_active_admins_idx ON users (is_admin)
    WHERE is_active AND is_admin;
