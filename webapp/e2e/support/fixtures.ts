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
import { test as base, expect, type BrowserContext } from "@playwright/test";

import { AccountFactory, signInApi, type ApiSession, type TestAccount } from "./accounts";
import { App } from "./app";
import { translator, type Translate } from "./i18n";
import type { TestOptions } from "./options";
import { readStackState } from "./stack";

/** The key src/i18n/index.ts reads before it initialises i18next. */
const LANGUAGE_STORAGE_KEY = "hamlaneh.language";

/** Seeds the language before any script runs — see the `context` fixture. */
async function seedLanguage(context: BrowserContext, locale: string): Promise<void> {
  await context.addInitScript(
    ([key, value]) => {
      window.localStorage.setItem(key ?? "", value ?? "");
    },
    [LANGUAGE_STORAGE_KEY, locale],
  );
}

/**
 * Signs a second person in, on their own browser context.
 *
 * A separate context, not a second tab: the whole point of the realtime tests
 * is that one person's send reaches somebody ELSE's screen, and two pages
 * sharing a cookie jar would be one person with two tabs. `path` is where they
 * land, so a spec can put them straight into the conversation under test.
 */
export type OpenApp = (account: TestAccount, path?: string) => Promise<App>;

interface Fixtures {
  t: Translate;
  accounts: AccountFactory;
  app: App;
  openApp: OpenApp;
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
   *
   * This settles only the screens before sign-in. Once a session exists the
   * interface follows the account's locale, which is why the account factory
   * is handed the same locale — see AccountFactory's constructor.
   */
  context: async ({ context, uiLocale }, provide) => {
    await seedLanguage(context, uiLocale);
    await provide(context);
  },

  openApp: async ({ browser, uiLocale, t }, provide) => {
    const opened: BrowserContext[] = [];
    await provide(async (account, path = "/") => {
      const context = await browser.newContext({
        baseURL: readStackState().baseURL,
        // The same reason the project sets it: Caddy issues from an internal
        // CA that exists only inside the stack's own volume, and global setup
        // has already asserted the certificate really is that CA's.
        ignoreHTTPSErrors: true,
      });
      opened.push(context);
      await seedLanguage(context, uiLocale);
      const app = new App(await context.newPage(), t);
      await app.gotoSignIn(path);
      await app.signIn(account.username, account.password);
      await app.chatSidebar.waitFor();
      return app;
    });
    for (const context of opened) {
      await context.close();
    }
  },

  t: async ({ uiLocale }, provide) => {
    await provide(translator(uiLocale));
  },

  accounts: async ({ adminSession, uiLocale }, provide) => {
    const factory = new AccountFactory(readStackState().baseURL, adminSession, uiLocale);
    await provide(factory);
    await factory.dispose();
  },

  app: async ({ page, t }, provide) => {
    await provide(new App(page, t));
  },
});

export { expect };
