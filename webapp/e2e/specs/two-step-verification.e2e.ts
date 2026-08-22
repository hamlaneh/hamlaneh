import { loginTotpCode, wrongTotpCode } from "../support/totp";
import { expect, test } from "../support/fixtures";

/**
 * The login challenge for an account with two-step verification on.
 *
 * The property under test is a security property, not a routing one: a
 * correct password on such an account must leave the caller with NO session.
 * The contract answers 202 and sets only a short-lived challenge cookie
 * precisely so that state can never be mistaken for being signed in, so the
 * test checks for the absence of the session cookie and for a second tab
 * landing on sign-in — not merely for which component is on screen.
 *
 * Enrolment is seeded through the API (see accounts.ts) rather than clicked
 * through the settings wizard: that is a different screen with its own
 * coverage, and driving it here would spend rate-limit budget and make this
 * assertion depend on three steps it is not about.
 *
 * Budget note: a challenge mint spends one unit of the per-IP login budget
 * (10 per 5 minutes, shared by every browser in the run), so this file mints
 * exactly one per locale.
 */
const SESSION_COOKIE = "hamlaneh_session";

test("two-step verification withholds the session until the code is accepted", async ({
  app,
  accounts,
  page,
  context,
  t,
}) => {
  const account = await accounts.createWithTotp();

  await app.gotoSignIn();
  await app.signIn(account.username, account.password);

  // 1. The password alone stops at the code screen.
  await expect(app.twoStepHeading).toBeVisible();
  await expect(app.chatSidebar).toHaveCount(0);

  // 2. And it really is not a session: no session cookie was set...
  const cookies = await context.cookies();
  expect(cookies.map((cookie) => cookie.name)).not.toContain(SESSION_COOKIE);

  // ...and anything else opened in the same browser is still signed out.
  const otherTab = await context.newPage();
  await otherTab.goto("/");
  await expect(otherTab.getByRole("heading", { name: t("login.title") })).toBeVisible();
  await otherTab.close();

  // 3. A wrong code is refused and leaves the challenge alive to retry.
  await app.enterCode(wrongTotpCode(account.otpauthUri));
  await app.verifyButton.click();
  await expect(page.getByText(t("totp.error.invalidCode"))).toBeVisible();
  await expect(app.chatSidebar).toHaveCount(0);

  // 4. The right code completes the sign-in.
  await app.enterCode(loginTotpCode(account.otpauthUri));
  await app.verifyButton.click();
  await expect(app.chatSidebar).toBeVisible();
});
