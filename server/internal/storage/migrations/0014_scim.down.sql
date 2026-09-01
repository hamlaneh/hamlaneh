DROP TABLE scim_tokens;

ALTER TABLE users
    DROP COLUMN scim_user_name,
    DROP COLUMN scim_external_id;

-- Refuses to run while a password-less account exists, which is correct:
-- restoring NOT NULL would otherwise have to invent a hash or delete people.
ALTER TABLE users
    ALTER COLUMN password_hash SET NOT NULL;
