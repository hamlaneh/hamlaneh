import { expect, test } from "../support/fixtures";

/**
 * Login rate limiting, from the browser's side.
 *
 * WHY THIS FILE IS ALONE IN ITS OWN PROJECT, RUNNING LAST
 *
 * The server's login limiter is keyed on the client IP and allows ten
 * recorded failures per five minutes. Every browser in the run reaches the
 * server through the same Caddy hop, so they all share one key: once this
 * test exhausts the budget, EVERY sign-in anywhere in the run is refused
 * with 429 until the window slides. That is not a flaw in the limiter, it is
 * the limiter working — so the suite is arranged around it. The project in
 * playwright.config.ts declares `dependencies: ["en", "fa"]`, which is what
 * guarantees nothing else is still signing in when this starts.
 *
 * The same arithmetic bounds the rest of the suite: everything else together
 * must stay well under ten deliberate failures inside any five minutes. Keep
 * new specs honest about that.
 */
test.describe.configure({ mode: "serial" });

/** Comfortably past the ten-per-five-minutes budget, bounded so it cannot spin. */
const MAX_ATTEMPTS = 14;

/**
 * The banner names the server's own wait, so its text carries a number that
 * differs between runs — and in Persian it is rendered in Persian digits.
 * Matching on the clause the countdown shares with its no-header fallback
 * keeps the locator true in either locale without encoding either alphabet:
 * whatever the two sentences have in common is what the state always says.
 */
function sharedPrefix(a: string, b: string): string {
  let i = 0;
  while (i < a.length && i < b.length && a[i] === b[i]) {
    i += 1;
  }
  return a.slice(0, i);
}

/** Any digit the count can be rendered as — Latin, Persian or Arabic-Indic. */
const ANY_DIGIT = /[0-9۰-۹٠-٩]/;

test("repeated wrong passwords end in the rate-limited state", async ({ app, accounts, t }) => {
  const account = await accounts.createReady("e2erl");

  await app.gotoSignIn();

  const rateLimitedText = sharedPrefix(
    t("login.error.rateLimited"),
    t("login.error.rateLimitedMinutes", { count: 1 }),
  );
  const rateLimited = app.statusBanner.filter({ hasText: rateLimitedText });
  let attempts = 0;

  while (attempts < MAX_ATTEMPTS && (await rateLimited.count()) === 0) {
    attempts += 1;
    await app.signIn(account.username, `wrong-attempt-${String(attempts)}`);
    // Each attempt settles on one of the two banners before the next starts.
    await expect(app.errorBanner.or(app.statusBanner)).toBeVisible();
  }

  await expect(rateLimited).toBeVisible();
  // The server sends Retry-After on this 429, so the notice must state the
  // real wait. Asserting a digit is present is what stops the screen quietly
  // regressing to the guess it used to show ("try again in a few minutes")
  // while every other assertion here still passed.
  await expect(rateLimited).toHaveText(ANY_DIGIT);

  // The state is not decorative: submission is blocked while it stands.
  await expect(app.signInButton).toBeDisabled();

  // And it is not a dead end — the design requires an edit-and-retry path
  // from every failure, so editing a field lifts the notice and re-enables
  // the button. (The password box was emptied by the failure, so typing into
  // it is a real change; refilling the identifier with the same value would
  // not be one, and React would not report it.)
  await app.passwordField.fill("typing-again-after-the-notice");
  await expect(rateLimited).toHaveCount(0);
  await expect(app.signInButton).toBeEnabled();

  // The correct password is still refused while the server's window stands,
  // which is what makes this a server-side control rather than a UI mood.
  await app.signIn(account.username, account.password);
  await expect(app.statusBanner.filter({ hasText: rateLimitedText })).toBeVisible();
  await expect(app.chatSidebar).toHaveCount(0);
});
