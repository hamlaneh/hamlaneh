/**
 * Per-project options the Playwright config sets and the fixtures consume.
 *
 * It lives in its own file, with no imports, because playwright.config.ts
 * type-imports it and that config is compiled by the webapp's own tsconfig
 * (which knows nothing about Node types or the e2e project).
 */
export interface TestOptions {
  /**
   * Which language the browser starts in. The app reads it from
   * localStorage("hamlaneh.language") before its first render, so the
   * fixture seeds that rather than clicking the switcher — a test must not
   * depend on a UI affordance to reach the state it is testing.
   */
  uiLocale: "en" | "fa";
}
