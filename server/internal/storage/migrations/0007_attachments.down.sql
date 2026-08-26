-- Restore 0003's stricter content shape first: a live message must carry
-- text again. Any attachment-only messages would violate the restored
-- constraint, so the down migration cannot pretend they never existed —
-- it gives them a placeholder body rather than failing halfway and leaving
-- the schema dirty. Rolling back a feature does not get to corrupt the
-- messages people sent with it.
UPDATE messages
SET content = '(attachment removed)'
WHERE deleted_at IS NULL AND content = '';

ALTER TABLE messages DROP CONSTRAINT messages_content_shape;
ALTER TABLE messages ADD CONSTRAINT messages_content_shape CHECK (
    (deleted_at IS NULL AND char_length(content) BETWEEN 1 AND 4000)
    OR (deleted_at IS NOT NULL AND content = '')
);

DROP TABLE attachments;
