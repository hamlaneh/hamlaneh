ALTER TABLE messages
    DROP CONSTRAINT messages_mls_both_or_neither,
    DROP COLUMN mls_ciphertext,
    DROP COLUMN mls_epoch;

ALTER TABLE channels
    DROP COLUMN e2ee;

DROP TABLE mls_welcomes;
DROP TABLE mls_commits;
DROP TABLE mls_groups;
DROP TABLE mls_key_packages;
DROP TABLE mls_devices;
