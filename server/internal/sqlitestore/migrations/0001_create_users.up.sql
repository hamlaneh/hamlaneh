-- SQLite counterpart of storage/migrations/0001_create_users.up.sql.
--
-- READ THIS FIRST; it applies to every file in this tree.
--
-- This is a PARALLEL tree, numbered to match the PostgreSQL one so the two
-- read side by side. It is not a translation layer: each file is written for
-- SQLite, and the PostgreSQL files are never touched.
--
-- The type mapping, fixed once here:
--   uuid        -> TEXT, canonical lowercase. Hexadecimal is monotonic, so
--                  text ordering matches PostgreSQL's byte ordering of uuid,
--                  which the DM pair canonicalization depends on.
--   citext      -> TEXT COLLATE CITEXT, the Go collation the driver registers
--                  (collation.go). SQLite's built-in NOCASE folds ASCII only.
--   timestamptz -> TEXT in one fixed-width UTC layout (codec.go), so
--                  lexicographic order is chronological.
--   bytea       -> BLOB.
--   boolean     -> INTEGER holding 0 or 1.
--   jsonb, inet -> TEXT.
--   char_length / octet_length -> length(), which counts characters on TEXT
--                  and bytes on BLOB, matching both.
--
-- Two rules about constraints, because SQLite can neither ADD nor DROP one:
--
--   * Where a later PostgreSQL migration CHANGES a constraint created here,
--     this tree declares the final form in the file that creates the object,
--     and the later numbered file says so and carries no statements. There
--     are no existing SQLite databases to replay history for, so the
--     intermediate form was never in force anywhere.
--   * Where a later migration ADDS a constraint over columns it also adds, a
--     BEFORE INSERT/UPDATE trigger raising ABORT stands in for the CHECK
--     (0017 is the only case).
--
-- Nothing generates identifiers: there is no gen_random_uuid(), and the
-- driver supplies every id. now() defaults are likewise absent except where a
-- migration itself inserts a row — the driver binds every timestamp it writes
-- so the value the caller sees is the value stored.

CREATE TABLE users (
    id            TEXT    PRIMARY KEY,
    username      TEXT    COLLATE CITEXT NOT NULL UNIQUE
                          CHECK (length(username) BETWEEN 3 AND 32),
    email         TEXT    COLLATE CITEXT UNIQUE
                          CHECK (email IS NULL OR length(email) <= 320),
    display_name  TEXT    NOT NULL DEFAULT ''
                          CHECK (length(display_name) <= 120),
    -- Nullable from the start. PostgreSQL migration 0014 drops the NOT NULL
    -- here once a directory can provision accounts with no password
    -- credential; SQLite cannot alter a column, and the intermediate form was
    -- never in force on any SQLite database, so the final shape is declared
    -- once. 0014 in this tree records the fold.
    password_hash TEXT,
    locale        TEXT    NOT NULL DEFAULT 'en'
                          CHECK (locale IN ('en', 'fa')),
    is_admin      INTEGER NOT NULL DEFAULT 0
                          CHECK (is_admin IN (0, 1)),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);
