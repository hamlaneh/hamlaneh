import createClient from "openapi-fetch";

import type { paths } from "./schema";

/**
 * Typed API client for the contract in docs/api/openapi.yaml.
 * `src/api/schema.d.ts` is generated from it via `npm run api:gen` — never
 * edit the schema by hand.
 *
 * - baseUrl "/": same-origin deployment behind Caddy (spec `servers`).
 * - credentials "include": sessions travel only in HttpOnly cookies.
 *
 * The X-Hamlaneh-CSRF header for state-changing requests is wired in the
 * Phase 1.1 auth slice.
 */
export const api = createClient<paths>({
  baseUrl: "/",
  credentials: "include",
});
