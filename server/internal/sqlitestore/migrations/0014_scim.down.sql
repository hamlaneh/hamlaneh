DROP TABLE IF EXISTS scim_tokens;
DROP INDEX IF EXISTS users_scim_user_name_key;
DROP INDEX IF EXISTS users_scim_external_id_key;
ALTER TABLE users DROP COLUMN scim_user_name;
ALTER TABLE users DROP COLUMN scim_external_id;
