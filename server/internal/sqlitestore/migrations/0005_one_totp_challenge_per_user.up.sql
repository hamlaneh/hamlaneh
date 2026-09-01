-- SQLite counterpart of 0005_one_totp_challenge_per_user.up.sql: one live
-- two-step challenge per user, enforced by the schema so challenge creation
-- is a single upsert.
--
-- The PostgreSQL file first deletes duplicates the old delete-then-insert
-- race may have left behind. There are none to delete here: no SQLite
-- database predates this tree, and 0004 above is the first file that creates
-- the table.
--
-- PostgreSQL adds a named UNIQUE constraint; SQLite cannot add one, and a
-- unique index is the same object under a different keyword — the constraint
-- PostgreSQL adds is itself implemented as a unique index.
CREATE UNIQUE INDEX totp_challenges_user_id_key ON totp_challenges (user_id);

-- The unique index above serves every user_id lookup; the plain index from
-- 0004 would be a redundant copy.
DROP INDEX totp_challenges_user_id_idx;
