import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { defineConfig, devices } from "@playwright/test";

/**
 * The layout tier: a real browser, no stack.
 *
 * WHY IT EXISTS SEPARATELY FROM `webapp/playwright.config.ts`. The main suite
 * drives the real deployment — Caddy, the Go binary, PostgreSQL — which is the
 * right thing for anything about a server. It also means the whole suite needs
 * Docker, and the two specs that would have caught a surface lying across the
 * call control are the media ones, which skip on any host that is not Linux.
 * The bug they were meant to catch reached its third recurrence for exactly
 * that reason: nothing that runs on a developer's machine lays anything out,
 * and jsdom never will.
 *
 * So this tier asks a narrower question — does the shell put its boxes where
 * they belong — and pays for it with Vite's dev server and the contract mocks
 * in `src/mocks/`, which is a couple of seconds and runs anywhere Chromium
 * does. Nothing here may assert about the server: the backend is a mock, and a
 * claim about it made here would be a claim about the mock.
 *
 * `npm run e2e:layout`, and the webapp CI job.
 */
const here = path.dirname(fileURLToPath(import.meta.url));
const webapp = path.resolve(here, "../..");

/** Not 5173: a developer's own `npm run dev` must not collide with a test run. */
const port = process.env.HAMLANEH_LAYOUT_PORT ?? "5199";
const onCI = process.env.CI !== undefined;

export default defineConfig({
  testDir: here,
  testMatch: "**/*.layout.ts",
  fullyParallel: true,
  forbidOnly: onCI,
  // None, for the same reason the main suite has none: a suite that hides an
  // intermittent failure behind a retry stops being evidence.
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: onCI ? [["github"], ["list"]] : [["list"]],
  use: {
    ...devices["Desktop Chrome"],
    baseURL: `http://localhost:${port}`,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: {
    // `vite` directly rather than `npm run dev`, so the port and the mock flag
    // are this config's business and not a package script's.
    command: `npx vite --port ${port} --strictPort`,
    cwd: webapp,
    env: { VITE_API_MOCK: "1" },
    url: `http://localhost:${port}`,
    reuseExistingServer: !onCI,
    timeout: 120_000,
  },
});
