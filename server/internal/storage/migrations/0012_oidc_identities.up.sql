-- Single sign-on identities: which provider subject is which local account.
--
-- One row per user (PRIMARY KEY on user_id) and one user per identity (the
-- UNIQUE below). Both directions matter: an account with two identities would
-- have two doors nobody audits together, and an identity pointing at two
-- accounts is an unanswerable question at sign-in.
--
-- (issuer, subject) is the whole login key. Email is deliberately NOT part of
-- it: emails are mutable at the provider and `sub` is contractually stable,
-- so looking an account up by email on each sign-in is the account-takeover
-- bug this schema is shaped to make impossible.
CREATE TABLE oidc_identities (
    user_id       uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    issuer        text NOT NULL CHECK (char_length(issuer) <= 512),
    subject       text NOT NULL CHECK (char_length(subject) <= 255),

    -- What the provider claimed at the moment of linking. A forensic record
    -- for the audit trail, never read to decide anything: if it were, it
    -- would be the email lookup this table exists to avoid.
    email_at_link citext,

    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,

    UNIQUE (issuer, subject)
);
