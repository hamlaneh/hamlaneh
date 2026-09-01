-- A message's files render in the order the SENDER listed them, and that
-- order is now stored instead of inferred.
--
-- It was inferred from created_at, which is a different order. The composer
-- uploads the picked files concurrently — deliberately, so a slow one never
-- holds up the others — so a small file chosen second routinely finishes
-- before a large one chosen first, and the cards came back in an order nobody
-- chose. When two uploads shared a microsecond it was not even stable: the id
-- broke the tie, and the id is random. Only the sender's order is a promise
-- worth making, and only a stored column can keep it.
--
-- NULL exactly when message_id is NULL, said by a CHECK rather than left to be
-- remembered: a file still in the composer has no position in a message it is
-- not part of yet. That is the same pairing shape attachments_dimensions_shape
-- already uses for width and height.
ALTER TABLE attachments
    ADD COLUMN message_position integer CHECK (message_position >= 0);

-- Every already-claimed row gets today's visible order — created_at, then id —
-- so none is left NULL. That is not tidiness: PostgreSQL sorts NULLs last and
-- SQLite sorts them first, so a NULL surviving here would make the two drivers
-- disagree about the position of a card in a history neither can reconstruct.
-- Backfilling removes the question rather than answering it twice.
UPDATE attachments a
SET message_position = ordered.message_position
FROM (
    SELECT id,
           (row_number() OVER (PARTITION BY message_id ORDER BY created_at, id) - 1)::integer
               AS message_position
    FROM attachments
    WHERE message_id IS NOT NULL
) ordered
WHERE a.id = ordered.id;

-- After the backfill, because PostgreSQL validates a new table CHECK against
-- every existing row and every claimed row was NULL until the statement above.
ALTER TABLE attachments
    ADD CONSTRAINT attachments_position_shape
    CHECK ((message_id IS NULL) = (message_position IS NULL));

-- This replaces 0007's attachments_message_id_idx rather than joining it: the
-- message's cards still look up on the leading message_id, so the old index
-- has no lookup left of its own, and the new one earns the extra column twice
-- over. It sorts the page's cards where they are read, and being UNIQUE it
-- makes the order TOTAL — no two files of one message can share a position —
-- which is what lets the read paths drop the id tiebreak instead of carrying
-- one on the chance of a collision that can no longer happen.
DROP INDEX attachments_message_id_idx;
CREATE UNIQUE INDEX attachments_message_order_idx
    ON attachments (message_id, message_position) WHERE message_id IS NOT NULL;
