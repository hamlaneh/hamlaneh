-- Previews are derived data: dropping the table loses nothing anybody wrote,
-- and re-applying 0008 lets enrichment rebuild every card from the messages
-- that are still there. The stored derivative images outlive this and are
-- collected by the blob sweep, exactly as orphaned attachment blobs are.
DROP TABLE link_previews;
