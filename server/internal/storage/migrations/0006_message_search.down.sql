DROP INDEX messages_content_search_idx;
-- pg_trgm is deliberately kept, for the reason 0001 keeps citext: dropping
-- an extension is a cluster-level decision, and another object may depend on
-- it by the time a rollback runs.
