-- One live two-step challenge per user, enforced by the schema.
--
-- CreateTotpChallenge used to delete-then-insert; a delete that matches no
-- row locks nothing, so two concurrent password logins could each insert and
-- leave two live challenges — two parallel code-guessing budgets for one
-- account. With user_id UNIQUE, challenge creation is a single upsert and
-- the invariant cannot be broken by any future code path either.

-- Duplicates the race may already have left behind: keep only the newest
-- challenge per user (the one whose cookie the client most recently
-- received), exactly what the delete-then-insert intended.
DELETE FROM totp_challenges older
 USING totp_challenges newer
 WHERE older.user_id = newer.user_id
   AND (older.created_at, older.id) < (newer.created_at, newer.id);

ALTER TABLE totp_challenges
    ADD CONSTRAINT totp_challenges_user_id_key UNIQUE (user_id);

-- The unique constraint's index serves every user_id lookup; the plain
-- index from 0004 would be a redundant copy.
DROP INDEX totp_challenges_user_id_idx;
