-- SQLite counterpart of 0012_oidc_identities.up.sql. (issuer, subject) is the
-- whole login key; email is deliberately NOT part of it.
CREATE TABLE oidc_identities (
    user_id       TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    issuer        TEXT NOT NULL CHECK (length(issuer) <= 512),
    subject       TEXT NOT NULL CHECK (length(subject) <= 255),

    -- What the provider claimed at the moment of linking: a forensic record,
    -- never read to decide anything.
    email_at_link TEXT COLLATE CITEXT,

    created_at    TEXT NOT NULL,
    last_login_at TEXT,

    UNIQUE (issuer, subject)
);
