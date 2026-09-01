/**
 * The invite token arrives in the URL *fragment*: the link an administrator
 * sends is `{base}/invite#token=...`, which is what the server mints into
 * `CreatedInvite.url` (`inviteRedemptionPath` and `inviteTokenFragmentKV` in
 * `internal/httpserver/invite_handlers.go`).
 *
 * A fragment is never sent to any server, so the token cannot reach an access
 * log, a Referer header, or a proxy's history — the same reasoning the reset
 * link follows, and it must not be "fixed" into a path segment or a query
 * parameter.
 *
 * This file used to read `/join/{token}` from the path and its comment said
 * that was what the server minted. It was not, and had never been: no `/join`
 * route is registered on the server either, so such a link 404s at the
 * document before any of this runs. Every real invitation landed on the
 * sign-in screen. Nothing failed loudly because the contract says only that
 * `CreatedInvite.url` is a string, so the two halves were free to disagree —
 * which is why the shape now lives in the contract's own description.
 */

/**
 * `undefined` while the fragment has not been read yet; the token (or null)
 * afterwards. Module state, not component state, because the read is
 * destructive: StrictMode's double-invoked render would otherwise find the
 * fragment already scrubbed and lose the token.
 */
let consumed: string | null | undefined;

/** The contract's own bounds (openapi.yaml → previewInvite.token). */
const TOKEN_SHAPE = /^[A-Za-z0-9_-]{20,128}$/u;

/** Where the server serves the redemption document. */
const REDEMPTION_PATH = "/invite";

function readAndScrub(): string | null {
  if (window.location.pathname !== REDEMPTION_PATH) {
    return null;
  }
  const hash = window.location.hash.startsWith("#")
    ? window.location.hash.slice(1)
    : window.location.hash;
  if (hash === "") {
    return null;
  }
  const token = new URLSearchParams(hash).get("token");
  // Anything outside the contract's shape is discarded here rather than
  // carried into a request path.
  if (token === null || !TOKEN_SHAPE.test(token)) {
    return null;
  }
  // Only the fragment goes. replaceState rather than pushState: the link's
  // own entry must not stay reachable with the Back button.
  window.history.replaceState(
    window.history.state,
    "",
    `${window.location.pathname}${window.location.search}`,
  );
  return token;
}

/**
 * The invite token from the link, or null when this is not an invitation.
 * Reads and scrubs on the first call; every later call gets the same answer.
 */
export function readInviteToken(): string | null {
  consumed ??= readAndScrub();
  return consumed;
}

/** Testing seam: forget the read so a case can stage its own URL. */
export function resetInviteTokenForTest(): void {
  consumed = undefined;
}
