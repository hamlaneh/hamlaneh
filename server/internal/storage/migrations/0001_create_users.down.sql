DROP TABLE users;
-- citext is deliberately kept: other tables may depend on it by the time a
-- rollback runs, and dropping an extension is a cluster-level decision.
