/**
 * The reset token arrives in the URL *fragment*, not the query string: the
 * emailed link is `{base}/reset#token=...`.
 *
 * That is deliberate on the server's side — a fragment is never sent to any
 * server, so the token cannot leak into access logs, Referer headers or a
 * proxy's history. It must not be "fixed" into a query parameter.
 *
 * The client's job is the other half: read it once and strip it from the
 * address bar immediately, so it does not sit in browser history, in a shared
 * screenshot, or in whatever the next `history.pushState` copies forward.
 */

/**
 * `undefined` while the fragment has not been read yet; the token (or null)
 * afterwards. Module state, not component state, because the read is
 * destructive: StrictMode's double-invoked render would otherwise find the
 * fragment already scrubbed and lose the token.
 */
let consumed: string | null | undefined;

function readAndScrub(): string | null {
  const hash = window.location.hash.startsWith("#")
    ? window.location.hash.slice(1)
    : window.location.hash;
  if (hash === "") {
    return null;
  }
  const token = new URLSearchParams(hash).get("token");
  if (token === null || token === "") {
    return null;
  }
  // Same document, same path and query — only the fragment goes. replaceState
  // rather than pushState: the link's own entry must not stay reachable with
  // the Back button.
  window.history.replaceState(
    window.history.state,
    "",
    `${window.location.pathname}${window.location.search}`,
  );
  return token;
}

/**
 * The reset token from the emailed link, or null. Reads (and scrubs) the
 * fragment on the first call and answers from memory afterwards.
 */
export function consumeResetToken(): string | null {
  if (consumed === undefined) {
    consumed = readAndScrub();
  }
  return consumed;
}

/** Test seam: forgets the consumed token so the next call reads the URL again. */
export function forgetResetToken(): void {
  consumed = undefined;
}
