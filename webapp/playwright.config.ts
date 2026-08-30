import process from "node:process";

import { defineConfig, devices } from "@playwright/test";

import type { TestOptions } from "./e2e/support/options";

/**
 * End-to-end suite for Hamlaneh.
 *
 * What it drives is the real stack: `deploy/docker-compose.yml` with the port
 * overlay in `webapp/e2e/docker-compose.e2e.yml`, which means Caddy
 * terminating TLS in front of the Go binary that embeds the web bundle, with
 * PostgreSQL behind it. Nothing is mocked. `webapp/e2e/support/stack.ts`
 * starts and stops it; `webapp/e2e/global-setup.ts` settles the first admin.
 *
 * Certificates: with HAMLANEH_DOMAIN=localhost, Caddy issues from an internal
 * CA that exists only inside the stack's own volume. `ignoreHTTPSErrors` is
 * therefore on — but global setup first asserts that the certificate on the
 * port really is that CA's, so the flag cannot hide a stack serving something
 * else entirely.
 *
 * Locale tiers (CLAUDE.md, testing policy):
 *   per-PR   — `npm run e2e`: the full suite in `en` plus the `fa` smoke subset
 *   nightly  — the same command with HAMLANEH_E2E_ALL_LOCALES=1 in the
 *              environment, which widens `fa` from the smoke subset to the
 *              full suite. The nightly workflow sets it; locally it is
 *              `HAMLANEH_E2E_ALL_LOCALES=1 npm run e2e` (sh) or
 *              `$env:HAMLANEH_E2E_ALL_LOCALES=1; npm run e2e` (PowerShell).
 *
 * Other environment switches:
 *   HAMLANEH_E2E_HTTPS_PORT   published port for the stack (default 8443)
 *   HAMLANEH_E2E_REUSE_STACK  keep a running stack between runs (local only)
 */
const httpsPort = process.env.HAMLANEH_E2E_HTTPS_PORT ?? "8443";
const allLocales = process.env.HAMLANEH_E2E_ALL_LOCALES === "1";
const onCI = process.env.CI !== undefined;

/**
 * Rate limiting is keyed on the client IP, and every browser in the run
 * shares one as far as the server is concerned. A spec that deliberately
 * exhausts that budget therefore cannot run beside specs that need to sign
 * in — so it lives in its own project, which `dependencies` puts last.
 */
const RATE_LIMIT_SPEC = "**/rate-limit.e2e.ts";
/**
 * Persian-only assertions; running them under the `en` project is meaningless
 * — and for the snapshot suite it is worse than meaningless, because a second
 * project would ask for a second set of committed baselines nobody wants.
 */
const PERSIAN_SPECS = ["**/fa-smoke.e2e.ts", "**/fa-rtl-snapshots.e2e.ts"];

/**
 * Specs that measure something with no language in it — a candidate type, a
 * credential in a response body — so running them twice measures the same
 * thing twice and calls it coverage.
 *
 * They live here rather than as a `test.skip` inside each spec so the routing
 * is in one readable place: a reader asking "what runs in Persian" gets an
 * answer from this file instead of from every spec's first three lines.
 */
const LOCALE_AGNOSTIC_SPECS = [
  "**/calls-relay-only.e2e.ts",
  "**/livekit-key-leak.e2e.ts",
  // Audio bytes, decoder energy and an MLS epoch. Nothing in what it measures
  // has a language in it, and it is one of the most expensive specs there is.
  "**/e2ee-call-rotation.e2e.ts",
];

/**
 * `.e2e.ts`, not `.spec.ts`: Vitest's default include is
 * `**\/*.{test,spec}.*`, so specs named the conventional way would be
 * collected by `npm test` as well and fail there — Playwright's `test` cannot
 * run under Vitest. The extension keeps the two suites from ever meeting.
 */
const SPECS = "**/*.e2e.ts";

export default defineConfig<TestOptions>({
  testDir: "./e2e/specs",
  testMatch: SPECS,
  fullyParallel: true,
  forbidOnly: onCI,
  // Deliberately none: a suite that hides intermittent failures behind a
  // retry stops being evidence. A flaky test gets fixed or deleted.
  retries: 0,
  workers: 4,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
  reporter: onCI
    ? [["github"], ["html", { open: "never" }], ["list"]]
    : [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: `https://localhost:${httpsPort}`,
    ignoreHTTPSErrors: true,
    // The three artefacts that make a red run actionable without a rerun.
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "en",
      use: { ...devices["Desktop Chrome"], uiLocale: "en" },
      testIgnore: [RATE_LIMIT_SPEC, ...PERSIAN_SPECS],
    },
    {
      name: "fa",
      use: { ...devices["Desktop Chrome"], uiLocale: "fa" },
      testIgnore: [RATE_LIMIT_SPEC, ...LOCALE_AGNOSTIC_SPECS],
      ...(allLocales ? {} : { grep: /@fa-smoke/u }),
    },
    {
      name: "rate-limit",
      use: { ...devices["Desktop Chrome"], uiLocale: "en" },
      testMatch: RATE_LIMIT_SPEC,
      dependencies: ["en", "fa"],
      fullyParallel: false,
    },
  ],
});
