import { computedDirection } from "../support/app";
import { expect, test } from "../support/fixtures";

/**
 * The Persian smoke subset the testing policy asks for on every PR: sign-in,
 * the chat shell, and one right-to-left-critical screen.
 *
 * The bar here is deliberately higher than "Persian text appears". Persian
 * strings render perfectly well inside a layout that never mirrored, and
 * that is the failure that actually ships. So each test checks direction
 * where it is decided — `dir` on the document, the computed `direction` of a
 * real element — and the settings screen additionally checks that the layout
 * MOVED: its navigation rail must sit to the right of the content it drives,
 * which is what CSS logical properties buy and what one stray `margin-left`
 * silently undoes.
 *
 * This file only ever runs in the `fa` project (playwright.config.ts).
 */
test.describe("Persian smoke", () => {
  test("@fa-smoke sign-in renders right-to-left and signs in", async ({
    app,
    accounts,
    page,
    t,
  }) => {
    const account = await accounts.createReady("e2efa");

    await app.gotoSignIn();

    const root = page.locator("html");
    await expect(root).toHaveAttribute("lang", "fa");
    await expect(root).toHaveAttribute("dir", "rtl");
    await expect(app.signInHeading).toHaveText(t("login.title"));
    await expect(app.signInButton).toHaveText(t("login.submit"));

    await app.signIn(account.username, account.password);
    await expect(app.chatSidebar).toBeVisible();
  });

  test("@fa-smoke the chat shell renders in Persian, right-to-left", async ({
    app,
    accounts,
    page,
    t,
  }) => {
    const account = await accounts.createReady("e2efa");

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);

    const sidebar = app.chatSidebar;
    await expect(sidebar).toBeVisible();
    await expect(sidebar.getByRole("heading", { name: t("chat.sidebar.channels") })).toBeVisible();
    await expect(
      sidebar.getByRole("heading", { name: t("chat.sidebar.directMessages") }),
    ).toBeVisible();

    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    expect(await computedDirection(sidebar)).toBe("rtl");

    // The sidebar is the inline-start rail, so in Persian it is on the right.
    const viewport = page.viewportSize();
    const box = await sidebar.boundingBox();
    expect(viewport).not.toBeNull();
    expect(box).not.toBeNull();
    if (viewport !== null && box !== null) {
      expect(box.x + box.width / 2).toBeGreaterThan(viewport.width / 2);
    }
  });

  test("@fa-smoke the settings panel mirrors, not just translates", async ({
    app,
    accounts,
    page,
    t,
  }) => {
    const account = await accounts.createReady("e2efa");

    await app.gotoSignIn();
    await app.signIn(account.username, account.password);
    await expect(app.chatSidebar).toBeVisible();

    const dialog = await app.openSettings();
    await expect(dialog.getByRole("heading", { name: t("settings.title") })).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
    expect(await computedDirection(dialog)).toBe("rtl");

    // The nav rail leads the content, so mirroring puts it to the RIGHT of
    // the panel it drives. A hard-coded physical margin would fail here while
    // every string still read correctly in Persian.
    const navBox = await dialog.getByRole("tablist", { name: t("settings.title") }).boundingBox();
    const panelBox = await dialog.getByRole("tabpanel").boundingBox();
    expect(navBox).not.toBeNull();
    expect(panelBox).not.toBeNull();
    if (navBox !== null && panelBox !== null) {
      expect(navBox.x).toBeGreaterThan(panelBox.x);
    }
  });
});
