/**
 * The base `test` every spec imports.
 *
 * It supplies four things and nothing else:
 *   - `t`        — translations for the project's locale, so a spec asserts
 *                  on keys and runs unchanged in English and Persian;
 *   - `accounts` — a factory for per-test accounts (see accounts.ts);
 *   - `app`      — the screen interactions the specs share;
 *   - a browser context whose language is fixed before the app's first render.
 *
 * Playwright's fixture callbacks take their "hand the value to the test"
 * function as the second argument, conventionally named `use`. It is named
 * `provide` here: the repo's ESLint config applies react-hooks to every .ts
 * file, and a bare `use(...)` call trips rules-of-hooks as if it were React's
 * `use`. Renaming the parameter is cheaper than exempting a directory.
 */
import { test as base, expect } from "@playwright/test";

import { AccountFactory, signInApi, type ApiSession } from "./accounts";
import { App } from "./app";
import { translator, type Translate } from "./i18n";
import type { TestOptions } from "./options";
import { readStackState } from "./stack";

/** The key src/i18n/index.ts reads before it initialises i18next. */
const LANGUAGE_STORAGE_KEY = "hamlaneh.language";

interface Fixtures {
  t: Translate;
  accounts: AccountFactory;
  app: App;
}

interface WorkerFixtures {
  /** The bootstrapped first admin, shared by every test in the worker. */
  adminSession: ApiSession;
}

export const test = base.extend<Fixtures & TestOptions, WorkerFixtures>({
  uiLocale: ["en", { option: true }],

  adminSession: [
    // Playwright requires the first argument to be a destructuring pattern
    // even when the fixture depends on nothing, which this one does not.
    // eslint-disable-next-line no-empty-pattern
    async ({}, provide) => {
      const stack = readStackState();
      const session = await signInApi(stack.baseURL, stack.admin.username, stack.admin.password);
      await provide(session);
      await session.dispose();
    },
    { scope: "worker" },
  ],

  /**
   * The language is seeded into localStorage before any script runs, rather
   * than clicked in the switcher: a Persian test must start in Persian, not
   * pass through an English screen first, and it must not depend on the
   * switcher working in order to test something else.
   */
  context: async ({ context, uiLocale }, provide) => {
    await context.addInitScript(
      ([key, value]) => {
        window.localStorage.setItem(key ?? "", value ?? "");
      },
      [LANGUAGE_STORAGE_KEY, uiLocale],
    );
    await provide(context);
  },

  t: async ({ uiLocale }, provide) => {
    await provide(translator(uiLocale));
  },

  accounts: async ({ adminSession }, provide) => {
    const factory = new AccountFactory(readStackState().baseURL, adminSession);
    await provide(factory);
    await factory.dispose();
  },

  app: async ({ page, t }, provide) => {
    await provide(new App(page, t));
  },
});

export { expect };
