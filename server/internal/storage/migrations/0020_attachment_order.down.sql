-- Back to 0007's index on message_id alone.
DROP INDEX attachments_message_order_idx;
CREATE INDEX attachments_message_id_idx ON attachments (message_id) WHERE message_id IS NOT NULL;

-- Dropping the column takes attachments_position_shape with it: PostgreSQL
-- drops a CHECK whose expression names a dropped column. Rolling back loses
-- the sender's order and the cards fall back to upload order, which is the
-- behaviour this migration replaced — no data beyond the ordering is lost.
ALTER TABLE attachments DROP COLUMN message_position;
