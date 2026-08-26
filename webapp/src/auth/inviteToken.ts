/**
 * The invite token arrives in the URL *path*: the link an admin sends is
 * `{base}/join/{token}`, which is what the server mints into
 * `CreatedInvite.url`.
 *
 * Unlike the reset token this one is not scrubbed from the address bar. A
 * reset token rides in the fragment precisely so it never reaches a server;
 * an invite token is a path segment the server has already seen by the time
 * the page renders, so removing it locally would buy nothing and would break
 * a reload of the redemption screen mid-signup.
 */

/** The contract's own bounds (openapi.yaml -> previewInvite.token). */
const JOIN_PATH = /^\/join\/([A-Za-z0-9_-]{20,128})$/u;

/**
 * The invite token from the current URL, or null when this is not a join
 * link. Anything that does not match the contract's shape is discarded here
 * rather than carried into a request path.
 */
export function readInviteToken(): string | null {
  return JOIN_PATH.exec(window.location.pathname)?.[1] ?? null;
}
