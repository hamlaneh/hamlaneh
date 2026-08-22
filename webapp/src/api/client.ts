import createClient from "openapi-fetch";
import type { Middleware } from "openapi-fetch";

import type { paths } from "./schema";

const CSRF_COOKIE = "hamlaneh_csrf";
const CSRF_HEADER = "X-Hamlaneh-CSRF";
const MUTATING_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

function readCookie(name: string): string | null {
  const prefix = `${name}=`;
  for (const part of document.cookie.split("; ")) {
    if (part.startsWith(prefix)) {
      return decodeURIComponent(part.slice(prefix.length));
    }
  }
  return null;
}

/**
 * Double-submit CSRF defense (spec: components.securitySchemes): mirror the
 * readable hamlaneh_csrf cookie into the X-Hamlaneh-CSRF header on every
 * state-changing request. The server compares header and cookie.
 */
const csrfMiddleware: Middleware = {
  onRequest({ request }) {
    if (!MUTATING_METHODS.has(request.method)) {
      return undefined;
    }
    const token = readCookie(CSRF_COOKIE);
    if (token === null) {
      return undefined;
    }
    request.headers.set(CSRF_HEADER, token);
    return request;
  },
};

/**
 * The largest `Retry-After` this client will believe. Every 429 the contract
 * describes is a window of minutes, so anything past an hour is not a wait a
 * screen can honestly count down — it is a value to distrust, not to render.
 */
const MAX_RETRY_AFTER_SECONDS = 60 * 60;

/**
 * Whole seconds until the caller's budget frees up, from a 429's `Retry-After`
 * — or null when the response does not carry one this client will act on.
 *
 * The spec's `RateLimited` response documents the header as an integer count of
 * seconds (present on the login, two-step and account-security 429s), so that
 * is the only form read here: the HTTP-date alternative RFC 9110 also allows is
 * not what the server sends. Absent, blank, zero, negative, fractional,
 * non-numeric and out-of-range all mean one thing to a caller — this response
 * cannot say when the door reopens, so say something vaguer rather than render
 * a guess dressed as a fact.
 */
export function retryAfterSeconds(response: Response): number | null {
  const header = response.headers.get("Retry-After")?.trim() ?? "";
  if (!/^\d+$/u.test(header)) {
    return null;
  }
  const seconds = Number(header);
  if (seconds <= 0 || seconds > MAX_RETRY_AFTER_SECONDS) {
    return null;
  }
  return seconds;
}

/**
 * Auth endpoints that must never trigger the automatic refresh+retry below:
 * a 401 from login IS the answer, a 401 from refresh means the session is
 * unrecoverable (retrying would loop), and logout drops the session anyway.
 */
const NO_REFRESH_PATHS = new Set([
  "/api/v1/auth/login",
  "/api/v1/auth/refresh",
  "/api/v1/auth/logout",
]);

/**
 * Single-flighted refresh: concurrent 401s must share ONE in-flight
 * POST /api/v1/auth/refresh. This is not just an optimization — the server
 * rotates refresh tokens and treats a re-presented (already-rotated) token as
 * theft, revoking the whole token family. Parallel refreshes would trip that.
 */
let refreshInFlight: Promise<boolean> | null = null;

function refreshSession(): Promise<boolean> {
  refreshInFlight ??= api
    .POST("/api/v1/auth/refresh")
    .then(({ response }) => response.status === 204)
    .catch(() => false) // network failure: treat as not refreshed
    .finally(() => {
      // Allow a future refresh (e.g. after the next 15-minute expiry);
      // awaiters of THIS refresh still share the resolved promise.
      refreshInFlight = null;
    });
  return refreshInFlight;
}

/**
 * Clones for the one-shot retry, keyed by openapi-fetch's per-request id.
 * Cloned in onRequest because by onResponse a request body is already
 * consumed and can no longer be cloned.
 */
const retryClones = new Map<string, Request>();

/**
 * Transparent session refresh: access sessions live ~15 minutes, the refresh
 * cookie ~30 days. On a 401 from any non-auth endpoint, refresh once and
 * retry the original request once. The retry goes straight to fetch (not back
 * through the client), so a second 401 propagates — no loops.
 */
const authRefreshMiddleware: Middleware = {
  onRequest({ id, request }) {
    if (!NO_REFRESH_PATHS.has(new URL(request.url).pathname)) {
      retryClones.set(id, request.clone());
    }
    return undefined;
  },
  async onResponse({ id, response }) {
    const retry = retryClones.get(id);
    retryClones.delete(id);
    if (retry === undefined || response.status !== 401) {
      // Not a 401, or an auth endpoint excluded above.
      return undefined;
    }
    const refreshed = await refreshSession();
    if (!refreshed) {
      return undefined; // refresh failed: the original 401 propagates
    }
    // The refresh rotated the cookies; re-mirror the current CSRF token.
    const token = readCookie(CSRF_COOKIE);
    if (token !== null && MUTATING_METHODS.has(retry.method)) {
      retry.headers.set(CSRF_HEADER, token);
    }
    return globalThis.fetch(retry);
  },
  onError({ id }) {
    retryClones.delete(id);
  },
};

/**
 * Typed API client for the contract in docs/api/openapi.yaml.
 * `src/api/schema.d.ts` is generated from it via `npm run api:gen` — never
 * edit the schema by hand.
 *
 * - baseUrl: window.location.origin is identical to the spec's same-origin
 *   "/" in the browser, and unlike "/" it is also a valid absolute base for
 *   Node's fetch under jsdom tests.
 * - credentials "include": sessions travel only in HttpOnly cookies.
 * - fetch: resolved per request, not captured at createClient() time, so
 *   MSW's node interceptor (installed in a test's beforeAll) is respected.
 */
export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
  fetch: (request) => globalThis.fetch(request),
});

// Order matters: csrfMiddleware first, so authRefreshMiddleware's clones
// already carry the CSRF header (re-set after a refresh anyway).
api.use(csrfMiddleware);
api.use(authRefreshMiddleware);
