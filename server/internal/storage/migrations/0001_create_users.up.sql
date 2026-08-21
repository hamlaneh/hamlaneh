-- Users are created by the installer (first admin) or from the admin
-- dashboard / invite links; public registration is off by default.
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    username      citext      NOT NULL UNIQUE
                              CHECK (char_length(username) BETWEEN 3 AND 32),
    email         citext      UNIQUE
                              CHECK (email IS NULL OR char_length(email) <= 320),
    display_name  text        NOT NULL DEFAULT ''
                              CHECK (char_length(display_name) <= 120),
    password_hash text        NOT NULL,
    locale        text        NOT NULL DEFAULT 'en'
                              CHECK (locale IN ('en', 'fa')),
    is_admin      boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
