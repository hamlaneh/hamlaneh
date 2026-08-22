import { expect, test } from "../support/fixtures";

/**
 * Settings → Security → Active sessions.
 *
 * Signing another device out is a real revocation, not a row disappearing:
 * the second session here is a live API session created against the same
 * account, so the test can check afterwards that the server refuses it. A
 * suite that only watched the list would pass against a screen that revokes
 * nothing.
 *
 * The second session is given an iPhone user agent so its row has a name the
 * test can point at, instead of depending on the order the list comes back in.
 */
const IPHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";

test.describe("settings and sessions", () => {
  test("the settings panel opens on Security and lists the current session", async ({
    app,
    accounts,
    t,
  }) => {
    const account = await accounts.createReady();

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);
    await expect(app.chatSidebar).toBeVisible();

    const dialog = await app.openSettings();
    await expect(dialog.getByRole("tab", { name: t("settings.nav.security") })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    const sessions = await app.openSessions();
    await expect(sessions.getByRole("listitem")).toHaveCount(1);
    await expect(sessions.getByText(t("settings.sessions.thisDevice"))).toBeVisible();
    await expect(sessions.getByText(t("settings.sessions.current"))).toBeVisible();

    // With nothing else signed in the bulk control is absent, not disabled.
    await expect(
      dialog.getByRole("button", { name: t("settings.sessions.signOutOthers") }),
    ).toHaveCount(0);
  });

  test("signing out another device revokes it on the server", async ({ app, accounts, t }) => {
    const account = await accounts.createReady();
    const otherDevice = await accounts.open(account.username, account.password, {
      userAgent: IPHONE_UA,
    });

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);
    await expect(app.chatSidebar).toBeVisible();

    const dialog = await app.openSettings();
    await expect(
      dialog.getByText(t("settings.security.sessionsCount", { count: 2 })),
    ).toBeVisible();

    const sessions = await app.openSessions();
    await expect(sessions.getByRole("listitem")).toHaveCount(2);

    const iphoneRow = sessions
      .getByRole("listitem")
      .filter({ hasText: t("settings.sessions.device.iphone") });
    await expect(iphoneRow).toHaveCount(1);
    await iphoneRow.getByRole("button", { name: t("settings.sessions.signOut") }).click();

    await expect(sessions.getByRole("listitem")).toHaveCount(1);
    await expect(iphoneRow).toHaveCount(0);
    // This device stays signed in — the panel is still up over the chat.
    await expect(sessions.getByText(t("settings.sessions.current"))).toBeVisible();

    // The revocation reached the server: the other session's own requests fail.
    const probe = await otherDevice.context.get("/api/v1/users/me");
    expect(probe.status()).toBe(401);
  });
});
