import type { Locator, Page } from "@playwright/test";

import { createChannelApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import { loginTotpCode } from "../support/totp";

/**
 * The delivered `chat-mobile` artboard's viewport, and the screens that have
 * no mobile artboard at all, at the same width.
 *
 * Two different failures are checked, because neither catches the other.
 *
 * `expectFits` compares the document element's scroll width to its client
 * width. A page wider than the phone holding it is the characteristic failure
 * of a desktop layout meeting a narrow viewport, and it is silent: every
 * string still reads, every control still responds, and half the screen is
 * simply somewhere else.
 *
 * `expectWithinViewport` measures one element's box. It exists because the
 * chat shell and the settings panel are `overflow: hidden` containers, which
 * means content driven off their edge is CLIPPED rather than added to the
 * document's scroll width — invisible to the first check, and the exact shape
 * of "the composer is off the side of the phone". Both run in Persian as well
 * as English: the layout mirrors, and a hard-coded physical offset does not.
 */
const PHONE = { width: 375, height: 812 };

test.use({ viewport: PHONE });

async function expectFits(page: Page, where: string): Promise<void> {
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(
    scrollWidth,
    `${where}: the document is ${String(scrollWidth)}px wide inside a ${String(clientWidth)}px viewport`,
  ).toBeLessThanOrEqual(clientWidth);
}

async function expectWithinViewport(locator: Locator, where: string): Promise<void> {
  const box = await locator.boundingBox();
  expect(box, `${where}: no box — the element is not rendered`).not.toBeNull();
  if (box === null) {
    return;
  }
  expect(box.x, `${where}: starts ${String(box.x)}px from the left edge`).toBeGreaterThanOrEqual(0);
  expect(
    box.x + box.width,
    `${where}: ends ${String(box.x + box.width)}px from the left edge of a ${String(PHONE.width)}px viewport`,
  ).toBeLessThanOrEqual(PHONE.width);
}

test("@fa-smoke the chat shell fits a 375px phone", async ({ app, accounts, page, t }) => {
  const account = await accounts.createReady("e2emob");
  // Seeded over the API so the shell has a conversation to open: the composer
  // renders only for an active channel, and creating one through the sidebar
  // would mean driving the drawer before the drawer has been asserted.
  const session = await accounts.open(account.username, account.password);
  await createChannelApi(session, uniqueSlug("mobile"));

  await app.gotoSignIn();
  await expectFits(page, "sign-in");

  await app.signIn(account.username, account.password);

  // The header control the mobile artboard adds, and only it: its presence is
  // how we know the <=899 tier is the one in force.
  //
  // 30s, not the 10s default: this is the first fact asserted after signIn,
  // so it absorbs the whole login round-trip, shell render and MLS bootstrap
  // on a contended runner — the same allowance every other spec's first
  // post-sign-in wait already makes (chat-messaging, chat-unread, key-swap).
  // Everything after it keeps the default.
  const drawerToggle = page.getByRole("button", { name: t("chat.header.openChannels") });
  await expect(drawerToggle).toBeVisible({ timeout: 30_000 });
  await expect(app.channelHeading).toBeVisible();
  await expectFits(page, "chat shell, drawer closed");
  await expectWithinViewport(drawerToggle, "drawer toggle");

  // Reachable, not merely present: a composer pushed under the scrim, off the
  // edge or behind the keyboard bar still passes `toBeVisible`.
  await expectWithinViewport(app.composerForm, "composer");
  await app.composerField.click();
  await expect(app.composerField).toBeFocused();
  await app.sendMessage("mobile");
  await expect(app.messageBodies).toHaveText(["mobile"]);
  await expectFits(page, "chat shell, message sent");

  // The drawer is a fixed 300px overlay, so it is the one piece of this
  // layout that can hang off the edge — and in Persian it hangs off the other
  // edge, which is exactly what a physical `left` would get wrong.
  await drawerToggle.click();
  await expect(app.chatSidebar).toBeVisible();
  await expectWithinViewport(app.chatSidebar, "open drawer");
  await expectFits(page, "chat shell, drawer open");

  // Scoped to the drawer on purpose: the scrim carries the same accessible
  // name (ChatShell) as the drawer's own X (Sidebar), and both are visible at
  // this width — an unscoped locator matches two elements and is refused.
  // This clicks the drawn control rather than the backdrop.
  await app.chatSidebar.getByRole("button", { name: t("chat.sidebar.close") }).click();
  await expect(app.chatSidebar).toBeHidden();
});

test("@fa-smoke the settings panel fits a 375px phone", async ({ app, accounts, page, t }) => {
  const account = await accounts.createReady("e2emob");

  await app.gotoSignIn();
  await app.signIn(account.username, account.password);

  // The gear lives in the sidebar footer, and at this width the sidebar is a
  // drawer — so the drawer is how you reach Settings on a phone at all.
  await page.getByRole("button", { name: t("chat.header.openChannels") }).click();
  await expect(app.chatSidebar).toBeVisible();

  const dialog = await app.openSettings();
  await expect(dialog.getByRole("tablist", { name: t("settings.title") })).toBeVisible();
  await expectWithinViewport(dialog, "settings panel");
  await expectFits(page, "settings panel");

  // Security carries the panel's widest content: the password form and the
  // instance policy beside it, drawn as two columns.
  await dialog.getByRole("tab", { name: t("settings.nav.security") }).click();
  await expect(dialog.getByRole("tabpanel")).toBeVisible();
  await expectWithinViewport(dialog.getByRole("tabpanel"), "settings panel, security");
  await expectFits(page, "settings panel, security");
});

/**
 * The admin dashboard has its own `@media (max-width: 899px)` tier, and no
 * mobile artboard: the 260px rail becomes a full-width strip above the
 * content, its nav list turns into a row that scrolls sideways inside itself,
 * and the seven-column users table scrolls inside its card. All three are the
 * "scrolls inside itself rather than widening the page" pattern, which is
 * precisely what `expectFits` is for.
 *
 * Reached by a hard navigation on purpose. /admin is a client-side route the
 * server has to know about too, and a bookmark or a reload lands there without
 * the shell ever having been mounted — that path 404'd until this slice.
 *
 * The first run of this test failed at ~620px because `admin.css` was not
 * imported by `index.css` at all, so `.hm-admin-scroll` never got its
 * `overflow-x: auto` and the table dragged the whole document sideways. That
 * import is what makes this pass, which is worth knowing if it ever goes red
 * again: check the stylesheet is in the bundle before touching the numbers.
 */
test("@fa-smoke the admin dashboard fits a 375px phone", async ({ app, accounts, page, t }) => {
  const account = await accounts.createReady("e2emobadm", { isAdmin: true });

  await app.gotoSignIn("/admin");
  await app.signIn(account.username, account.password);

  // First fact after signIn — the 30s allowance the chat-shell test explains.
  await expect(page.getByRole("heading", { name: t("admin.users.title") })).toBeVisible({
    timeout: 30_000,
  });
  const rail = page.getByRole("navigation", { name: t("admin.nav.label") });
  const content = page.getByRole("main");
  await expect(rail).toBeVisible();

  // Stacked, not side by side: the rail ending above the content is how we
  // know the <=899 tier is the one in force, and not a 260px column squeezed
  // into a 375px phone.
  const railBox = await rail.boundingBox();
  const contentBox = await content.boundingBox();
  expect(railBox, "no box for the admin rail").not.toBeNull();
  expect(contentBox, "no box for the admin content").not.toBeNull();
  if (railBox !== null && contentBox !== null) {
    expect(
      railBox.y + railBox.height,
      `the admin rail ends ${String(railBox.y + railBox.height)}px down, content starts at ${String(contentBox.y)}px`,
    ).toBeLessThanOrEqual(Math.ceil(contentBox.y));
  }

  await expectWithinViewport(rail, "admin rail");
  await expectFits(page, "admin dashboard");

  // The table is the widest thing on the screen by a distance. It has to
  // scroll inside its own card; a table that widens the document takes the
  // header, the filters and the rail with it.
  await expect(page.getByRole("table", { name: t("admin.users.title") })).toBeVisible();
  await expectFits(page, "admin dashboard, users table");

  // The nav is a sideways-scrolling row here, and scrolling it must move the
  // row and not the page.
  await rail.getByRole("link", { name: t("admin.nav.audit") }).scrollIntoViewIfNeeded();
  await expectFits(page, "admin dashboard, nav scrolled");
});

/**
 * `en` only, deliberately. What this covers is a WIDTH — six 60px cells and
 * their gaps come to 400px against a 375px phone — and that arithmetic is the
 * same in both directions, so running it in Persian would buy nothing and
 * spend a second unit of the per-IP challenge budget the suite shares (see
 * two-step-verification.e2e.ts).
 */
test("the two-step code row fits a 375px phone", async ({ app, accounts, page, t }) => {
  const account = await accounts.createWithTotp("e2emob2fa");

  await app.gotoSignIn();
  await app.signIn(account.username, account.password);

  // First fact after signIn — the 30s allowance the chat-shell test explains.
  await expect(app.twoStepHeading).toBeVisible({ timeout: 30_000 });
  // The first and last of the six: between them they span the whole row, so
  // a row that no longer fits shows up on one end or the other.
  await expectWithinViewport(
    page.getByLabel(t("password.otpCell", { index: 1, total: 6 })),
    "first code cell",
  );
  await expectWithinViewport(
    page.getByLabel(t("password.otpCell", { index: 6, total: 6 })),
    "last code cell",
  );
  await expectFits(page, "two-step verification");

  // Every cell has to stay typeable after it has been allowed to shrink.
  await app.enterCode(loginTotpCode(account.otpauthUri));
  await app.verifyButton.click();
  await expect(page.getByRole("button", { name: t("chat.header.openChannels") })).toBeVisible();
});
