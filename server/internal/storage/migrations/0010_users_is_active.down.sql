DROP INDEX users_active_admins_idx;

ALTER TABLE users
    DROP COLUMN is_active;
