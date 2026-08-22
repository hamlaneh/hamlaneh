CREATE INDEX totp_challenges_user_id_idx ON totp_challenges (user_id);
ALTER TABLE totp_challenges DROP CONSTRAINT totp_challenges_user_id_key;
