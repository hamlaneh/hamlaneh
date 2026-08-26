-- Pending "link this identity to my account" requests: the server-side
-- state that decides, at the OIDC callback, whether an authorization round
-- trip links an identity or signs one in.
--
-- Why this exists rather than a field in the transaction cookie: the account
-- that receives the identity is the ONE input an attacker must not choose,
-- and a cookie is chosen by whoever composes the request. HttpOnly, Secure
-- and SameSite constrain a browser; they do nothing to a hand-built Cookie
-- header from a script running in the attacker's own client. Carrying the
-- target account in the cookie let any member drive a real sign-in flow as
-- themselves and then name the admin's id in a forged cookie, linking their
-- identity to the admin's account. Moving the intent here makes the target
-- un-forgeable: a row exists only because POST /users/me/oidc — an
-- authenticated, session-gated endpoint — inserted it for the caller's own
-- id, and the callback trusts the row, never the cookie.
--
-- Keyed by the SHA-256 of the OIDC state (the same 32-byte digest the
-- transaction cookie is compared on), so a leaked database yields no live
-- credential — the raw state never rests here, exactly as token_hash and the
-- reset/invite tokens are stored hashed elsewhere.
CREATE TABLE oidc_link_requests (
    -- SHA-256 of the flow's state parameter. One pending link per state, and
    -- the callback consumes it by this key.
    state_hash       bytea       PRIMARY KEY
                                 CHECK (octet_length(state_hash) = 32),
    -- The account the identity links to, fixed by the authenticated caller
    -- at insert and never read from anywhere the caller does not control.
    user_id          uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of a second secret that lives ONLY in the transaction cookie
    -- and is never put in a URL, sent to the provider, or stored raw. The
    -- state alone is not enough to complete a link: state is a CSRF nonce
    -- that appears in the victim's address bar, history, proxy logs and at
    -- the provider, so an attacker who OBSERVES an outstanding state could
    -- forge a cookie carrying it and link their identity to the victim. The
    -- consuming DELETE matches on this too, so completing a link needs the
    -- secret that only the browser which started the flow ever held — and an
    -- attacker who can read that cookie jar already holds the session cookie.
    link_secret_hash bytea       NOT NULL
                                 CHECK (octet_length(link_secret_hash) = 32),
    -- Enforced by the consuming DELETE, not by a browser dropping a cookie.
    expires_at       timestamptz NOT NULL
);
