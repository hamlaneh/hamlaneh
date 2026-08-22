import { expect, test } from "../support/fixtures";

/**
 * The sign-in screen against the real server.
 *
 * Both halves matter. The accepted password has to end in a session that
 * survives a reload — a screen that merely swaps components would pass a
 * weaker assertion — and the refused one has to produce the message the
 * contract's single 401 body is designed for: identical for an unknown
 * account and a wrong password, so the screen cannot be used to find out
 * which accounts exist.
 */
test.describe("sign-in", () => {
  test("a correct password signs in and the session survives a reload", async ({
    app,
    accounts,
    page,
  }) => {
    const account = await accounts.createReady();

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);

    await expect(app.chatSidebar).toBeVisible();
    await expect(page.getByText(account.displayName)).toBeVisible();

    // The session lives in cookies the server set, not in React state.
    await page.reload();
    await expect(app.chatSidebar).toBeVisible();
  });

  test("a wrong password is refused with the generic credentials error", async ({
    app,
    t,
    accounts,
  }) => {
    const account = await accounts.createReady();

    await app.gotoSignIn();
    await app.signIn(account.username, "not-the-right-password");

    await expect(app.errorBanner).toHaveText(t("login.error.invalidCredentials"));
    await expect(app.signInHeading).toBeVisible();
    await expect(app.chatSidebar).toHaveCount(0);
    // The identifier survives a failure; the password never does.
    await expect(app.identifierField).toHaveValue(account.username);
    await expect(app.passwordField).toHaveValue("");
  });

  test("an unknown account is refused with exactly the same message", async ({ app, t }) => {
    await app.gotoSignIn();
    await app.signIn("nobody-e2e-unknown", "not-the-right-password");

    await expect(app.errorBanner).toHaveText(t("login.error.invalidCredentials"));
    await expect(app.chatSidebar).toHaveCount(0);
  });
});
