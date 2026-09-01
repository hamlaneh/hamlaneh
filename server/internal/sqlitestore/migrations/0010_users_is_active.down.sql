DROP INDEX IF EXISTS users_active_admins_idx;
ALTER TABLE users DROP COLUMN is_active;
