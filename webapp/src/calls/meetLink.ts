/**
 * The conference token arrives in the URL *path*: the link a member sends is
 * `{base}/meet/{token}`, which is what the server mints into
 * `CreatedConference.url`.
 *
 * It is not scrubbed from the address bar, and here that is more than the
 * invite link's reasoning (the server has already seen a path segment, so
 * removing it locally buys nothing). A conference link is a *standing* link —
 * ADR 005 keeps it alive precisely so it can live in a recurring calendar
 * entry — so reloading the page it opens has to work, and scrubbing would
 * turn a refresh into a dead end.
 *
 * The bounds are the invite token's, because both are `session.NewToken`:
 * 32 bytes of base64url, 43 characters. Anything outside that shape is
 * discarded here rather than carried into a request path.
 */
const MEET_PATH = /^\/meet\/([A-Za-z0-9_-]{20,128})$/u;

/**
 * The conference token from the current URL, or null when this is not a
 * conference link.
 */
export function readMeetToken(): string | null {
  return MEET_PATH.exec(window.location.pathname)?.[1] ?? null;
}
