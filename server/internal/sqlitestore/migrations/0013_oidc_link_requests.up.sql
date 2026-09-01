-- SQLite counterpart of 0013_oidc_link_requests.up.sql. The target account of
-- a link is server-side state precisely so it cannot be chosen by whoever
-- composes the request; the callback trusts the row, never the cookie.
CREATE TABLE oidc_link_requests (
    -- SHA-256 of the flow's state parameter.
    state_hash       BLOB PRIMARY KEY
                          CHECK (length(state_hash) = 32),
    user_id          TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of a second secret that lives ONLY in the transaction cookie.
    link_secret_hash BLOB NOT NULL
                          CHECK (length(link_secret_hash) = 32),
    -- Enforced by the consuming DELETE, not by a browser dropping a cookie.
    expires_at       TEXT NOT NULL
);
