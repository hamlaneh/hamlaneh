import { expect, test } from "../support/fixtures";

/**
 * The forced password replacement.
 *
 * Every account an administrator creates — and the very first admin the
 * installer bootstraps — arrives with must_change_password set, so this is
 * the screen that stands between a brand-new account and the application.
 * It is a gate, and a gate is worth testing in both directions: it must not
 * be walkable around, and it must keep the one documented way out for
 * someone who signed in to the wrong account.
 */
test.describe("forced password change", () => {
  test("a new account must replace its password before reaching the app", async ({
    app,
    accounts,
    page,
    context,
  }) => {
    const account = await accounts.createPending();
    const newPassword = `changed-${Date.now().toString(36)}-e2e`;

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);

    await expect(app.changePasswordHeading).toBeVisible();
    await expect(app.chatSidebar).toHaveCount(0);

    // The gate is the server's flag, not a render choice: reloading lands here.
    await page.reload();
    await expect(app.changePasswordHeading).toBeVisible();

    await app.completeForcedPasswordChange(account.password, newPassword);
    await expect(app.chatSidebar).toBeVisible();

    // The change is real and permanent: with the session thrown away, the NEW
    // password signs in and no forced-change screen appears again.
    await context.clearCookies();
    await app.gotoSignIn();
    await app.signIn(account.username, newPassword);
    await expect(app.chatSidebar).toBeVisible();
    await expect(app.changePasswordHeading).toHaveCount(0);
  });

  test("signing out is the way past the screen without changing the password", async ({
    app,
    accounts,
  }) => {
    const account = await accounts.createPending();

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);
    await expect(app.changePasswordHeading).toBeVisible();

    await app.forcedChangeSignOutButton.click();

    await expect(app.signInHeading).toBeVisible();
    await expect(app.identifierField).toHaveValue("");
  });
});
