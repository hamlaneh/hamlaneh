/**
 * The places single sign-on touches the address bar rather than the API.
 *
 * Starting the flow, and coming back from it, are *navigations*: the browser
 * has to follow the server's 302 to another origin and carry the SameSite=Lax
 * transaction cookie back with the callback. A `fetch` would follow the
 * redirects invisibly and land the provider's HTML in a response body, so
 * nothing here goes through the API client.
 */

/**
 * Where the sign-in button points. A plain link — the browser navigating is
 * the mechanism, not an implementation detail of one.
 */
export const OIDC_START_PATH = "/api/v1/auth/oidc/start";

/**
 * The two parameters the callback can land on the application root with
 * (openapi.yaml, `completeOidcSignIn`: "every one of them lands on the
 * application root. What differs is one query parameter, which the client
 * reads once and strips from the address bar"). Signed in carries neither.
 */
const CHALLENGE_PARAM = "sso";
const ERROR_PARAM = "sso_error";

/**
 * The three failure codes the contract names, and the only three this app
 * renders. It calls them a closed set and says "a client meeting an
 * unrecognised value treats it as sso_failed" — which is also what keeps
 * provider-supplied text off the screen, since text the provider chose can
 * never match one of these.
 */
const SSO_ERROR_CODES = ["sso_account_exists", "sso_account_unknown", "sso_failed"] as const;

export type SsoErrorCode = (typeof SSO_ERROR_CODES)[number];

/**
 * The challenge methods this client can complete. `?sso=totp` names the
 * *method* rather than saying "a challenge is owed", so the parameter survives
 * WebAuthn arriving beside totp the way `TwoFactorChallenge.methods` does.
 *
 * When a second member lands here, this union widens and every `switch` over
 * it stops compiling until the new screen is wired — which is the whole point
 * of reading a method instead of a boolean.
 */
const CHALLENGE_METHODS = ["totp"] as const;

export type ChallengeMethod = (typeof CHALLENGE_METHODS)[number];

/** What the callback said, or null when this page load is not a callback landing. */
export type SsoLanding =
  | { outcome: "challenge"; method: ChallengeMethod }
  | { outcome: "error"; code: SsoErrorCode };

function isSsoErrorCode(value: string): value is SsoErrorCode {
  return (SSO_ERROR_CODES as readonly string[]).includes(value);
}

function isChallengeMethod(value: string): value is ChallengeMethod {
  return (CHALLENGE_METHODS as readonly string[]).includes(value);
}

/**
 * `undefined` until the query has been read; the landing (or null) afterwards.
 * Module state, not component state, because the read is destructive —
 * StrictMode's double-invoked render would otherwise find the parameters
 * already scrubbed and lose the answer. Same reasoning as `resetToken.ts`.
 */
let consumed: SsoLanding | null | undefined;

function readAndScrub(): SsoLanding | null {
  const params = new URLSearchParams(window.location.search);
  const challenge = params.get(CHALLENGE_PARAM);
  const failure = params.get(ERROR_PARAM);
  if (challenge === null && failure === null) {
    return null;
  }
  params.delete(CHALLENGE_PARAM);
  params.delete(ERROR_PARAM);
  const query = params.toString();
  // Only these parameters go; anything else in the query, and the fragment,
  // are not this module's to remove. replaceState rather than pushState: the
  // callback's own entry must not stay reachable with the Back button, or a
  // refresh re-shows an outcome that already happened and a copied link
  // carries it to whoever it is pasted to.
  window.history.replaceState(
    window.history.state,
    "",
    `${window.location.pathname}${query === "" ? "" : `?${query}`}${window.location.hash}`,
  );
  if (challenge !== null) {
    // An unrecognised method is handled the way an unrecognised error code is:
    // fall back inside the closed set rather than leaving it. A second factor
    // really is owed — the challenge cookie is set — so the honest degradation
    // is the one challenge screen this client has, which offers `onChallengeLost`
    // if the code cannot in fact be completed. It never leaves the set, and it
    // never renders the value.
    return { outcome: "challenge", method: isChallengeMethod(challenge) ? challenge : "totp" };
  }
  return {
    outcome: "error",
    code: failure !== null && isSsoErrorCode(failure) ? failure : "sso_failed",
  };
}

/**
 * What the identity provider callback came back with, or null. Reads (and
 * scrubs) the query on the first call and answers from memory afterwards, so
 * every caller on the same page load sees the same answer.
 */
export function consumeSsoLanding(): SsoLanding | null {
  if (consumed === undefined) {
    consumed = readAndScrub();
  }
  return consumed;
}

/** Test seam: forgets the landing so the next call reads the URL again. */
export function forgetSsoLanding(): void {
  consumed = undefined;
}

/**
 * Follows the provider redirect that `POST /api/v1/users/me/oidc` handed back.
 *
 * An object with a method rather than a bare function because `window.location`
 * is unforgeable — jsdom refuses to redefine `assign`, so standing in for this
 * property is the only way a test can watch the app leave the page.
 */
export const providerRedirect = {
  follow(url: string): void {
    window.location.assign(url);
  },
};
