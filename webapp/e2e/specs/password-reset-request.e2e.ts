import { expect, test } from "../support/fixtures";

/**
 * "Forgot password?" — the request half of the reset flow.
 *
 * The whole point of the screen is that it tells you nothing. POST
 * /auth/reset-request always answers 202 and the confirmation names no
 * identity, so the screen cannot be used to discover which addresses have
 * accounts. That is asserted here the only way it can honestly be asserted:
 * by running it for an address that exists and one that does not, and
 * comparing what the page says.
 *
 * The stack runs with an SMTP host configured (a reserved .invalid name), so
 * password_reset_available is true and the link is offered. Delivery happens
 * off the response path by design, so no mail server is involved and nothing
 * about the confirmation depends on one.
 */
test.describe("password reset request", () => {
  test("the confirmation is identical for a known and an unknown address", async ({
    app,
    accounts,
    t,
    page,
  }) => {
    const account = await accounts.createReady();

    await app.gotoSignIn();
    await app.forgotPasswordLink.click();

    await expect(page.getByRole("heading", { name: t("resetRequest.title") })).toBeVisible();
    await app.resetEmailField.fill(account.email);
    await app.resetSubmitButton.click();

    const confirmation = t("resetRequest.confirmation");
    await expect(app.statusBanner).toHaveText(confirmation);
    const knownAddressScreen = await page.locator("form").innerText();

    // Same screen, an address with no account behind it.
    await page.getByRole("button", { name: t("resetRequest.backToSignIn") }).click();
    await expect(app.signInHeading).toBeVisible();
    await app.forgotPasswordLink.click();
    await app.resetEmailField.fill(`nobody-${Date.now().toString(36)}@e2e.invalid`);
    await app.resetSubmitButton.click();

    await expect(app.statusBanner).toHaveText(confirmation);
    expect(await page.locator("form").innerText()).toBe(knownAddressScreen);
  });

  test("the request screen leads back to sign-in", async ({ app, t, page }) => {
    await app.gotoSignIn();
    await app.forgotPasswordLink.click();

    await expect(page.getByRole("heading", { name: t("resetRequest.title") })).toBeVisible();
    await page.getByRole("button", { name: t("resetRequest.backToSignIn") }).click();

    await expect(app.signInHeading).toBeVisible();
  });
});
