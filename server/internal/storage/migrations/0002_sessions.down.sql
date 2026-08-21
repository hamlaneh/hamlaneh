DROP TABLE sessions;

ALTER TABLE users
    DROP COLUMN must_change_password;
