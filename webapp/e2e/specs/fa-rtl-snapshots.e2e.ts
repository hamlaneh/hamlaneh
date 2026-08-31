import { readFileSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";

import type { Page } from "@playwright/test";

import type { AccountFactory, TestAccount } from "../support/accounts";
import type { App } from "../support/app";
import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import type { Translate } from "../support/i18n";

/**
 * The Persian right-to-left baseline: `<html dir="rtl" lang="fa">` on the five
 * core screens, and a committed screenshot of each (ROADMAP §1.5).
 *
 * # Two jobs, two tests
 *
 * The attribute assertion and the image diff are deliberately separate. A
 * document that stopped declaring `dir="rtl"` is a precise failure with a
 * one-line cause, and burying it inside "37,412 pixels differ" would throw
 * that away. The attribute test also runs everywhere, while the screenshots
 * run only where the committed baselines were rendered — see below.
 *
 * # Where the pixels come from
 *
 * Chromium rasterises text through DirectWrite on Windows and FreeType on
 * Linux. The glyphs land on different pixels, on a page that is almost
 * entirely text, so a Windows baseline and a Linux one differ by several
 * percent of the image — and a `maxDiffPixelRatio` wide enough to absorb that
 * is wide enough to absorb a button changing colour. There is no honest
 * threshold, so there is no cross-platform baseline.
 *
 * The baselines committed here are therefore Linux-rendered, matching CI
 * (ubuntu-latest, `npx playwright install --with-deps chromium`), and the
 * comparison is skipped anywhere else with the reason stated rather than
 * passed with a threshold that proves nothing.
 *
 * To regenerate them from a machine that is not Linux, point the run at a
 * Linux browser instead of the local one with HAMLANEH_E2E_LINUX_BROWSER;
 * support/linux-browser.mjs is one, and carries the command that starts it.
 *
 * # What is pinned, and what could not be
 *
 * Everything that varies between two identical runs is pinned in the DATA
 * where that was possible — fixed display names, fixed channel slugs, fixed
 * message text, a fixed viewport, UTC, an explicit colour scheme. What was
 * left is neutralised by support/screenshot.css, which names each case: the
 * avatar tint (hashed from a server-generated id), the message clock (stamped
 * by the server) and the caret blink. Nothing is masked: a mask hides
 * regressions inside it as well as the value it was aimed at.
 *
 * The fixed slugs are unique instance-wide, so a second run against a REUSED
 * stack (HAMLANEH_E2E_REUSE_STACK=1) fails at channel creation. That is the
 * price of a slug a committed baseline can name; a fresh stack, which is what
 * CI and a plain `npm run e2e` both give, has no such problem.
 *
 * # The committed baselines are STALE and have not been regenerated
 *
 * ADR 011 made every conversation end-to-end encrypted, so the two screens
 * that open one — `channel-list` and `message-view` — now draw an encryption
 * notice that was not there when their PNGs were taken. The images below will
 * therefore differ from the product until somebody re-records them, and that
 * difference is correct rather than a regression: the screens really did
 * change, and a baseline that still matched would mean the notice was missing.
 *
 * They were deliberately NOT regenerated here. The baselines are Linux
 * renderings (see above) and the branch was worked on a Windows host, where
 * the comparison skips and `--update-snapshots` would write Windows pixels
 * under a Linux name — a baseline that is wrong everywhere it actually runs.
 * Re-record them on Linux, or through HAMLANEH_E2E_LINUX_BROWSER, and read the
 * diff before accepting it: an encryption notice appearing is the expected
 * change, anything else in the same diff is not.
 */

/** A Linux Playwright server to render against; see the header. */
const LINUX_BROWSER = process.env.HAMLANEH_E2E_LINUX_BROWSER;

/**
 * `snapshotSuffix` names the platform of the test RUNNER, and the baselines
 * are named for the platform that produced the PIXELS. Those are the same
 * thing on CI and different when a Windows runner drives the container above.
 */
const BASELINE_PLATFORM = "linux";
const rendersOnLinux = process.platform === BASELINE_PLATFORM || LINUX_BROWSER !== undefined;

/** What could not be pinned in the data, and why — see support/screenshot.css. */
const NEUTRALISING_STYLES = readFileSync(
  fileURLToPath(new URL("../support/screenshot.css", import.meta.url)),
  "utf8",
);

/** Inside the ≥1280 tier and not on its edge, so no rounding sits on a boundary. */
const VIEWPORT = { width: 1440, height: 900 };

/**
 * Fixed names and slugs. A generated one carries random hex, and hex is drawn
 * on screen — pinning the data is what keeps a baseline honest, where masking
 * the region it lands in would hide whatever else moved there.
 *
 * They are Persian because the screens under test are: a Persian display name
 * in a right-to-left row and a Persian sentence with a Latin run inside it are
 * exactly where bidirectional layout breaks, and a suite that only ever drew
 * ASCII would photograph none of it. Everything AROUND them — keys, comments,
 * identifiers — stays English, per the language policy.
 */
const AUTHOR_NAME = "زهرا نوری";
const PEER_NAME = "Dana Lee";
/** Slugs are unique instance-wide, so one fixed slug per test that needs one. */
const LIST_SLUGS = ["rtl-general", "rtl-design", "rtl-releases"] as const;
const MESSAGE_SLUG = "rtl-conversation";
const MESSAGES = [
  "سلام، نسخه‌ی تازه روی استیجینگ بالا آمد.",
  "دیدمش — build 2481 است، درست؟",
  "بله، همان.",
] as const;

test.use({
  viewport: VIEWPORT,
  // Both remove a real source of run-to-run drift: a message stamp formatted
  // in the runner's own zone, and a page drawn in whatever the host's theme is.
  timezoneId: "UTC",
  colorScheme: "light",
  ...(LINUX_BROWSER === undefined ? {} : { connectOptions: { wsEndpoint: LINUX_BROWSER } }),
});

/** `<html dir lang>` — the assertion that is worth its own failure message. */
async function expectRightToLeftDocument(page: Page, where: string): Promise<void> {
  const root = page.locator("html");
  await expect(root, `${where}: the document is not marked Persian`).toHaveAttribute("lang", "fa");
  await expect(root, `${where}: the document is not marked right-to-left`).toHaveAttribute(
    "dir",
    "rtl",
  );
}

/** The committed comparison. Everything unstable is handled before we get here. */
async function expectBaseline(page: Page, name: string): Promise<void> {
  test.info().snapshotSuffix = BASELINE_PLATFORM;
  // Adopted as a constructed stylesheet, and neither of the two obvious ways
  // round. The application serves `style-src 'self'`: toHaveScreenshot's own
  // `stylePath` injects a <style> the page then blocks WITHOUT SAYING SO — the
  // screenshot simply comes out unneutralised — and `addStyleTag` at least
  // throws. A sheet built in script is not inline style, so this survives the
  // policy the product ships, which is the policy to photograph it under.
  await page.evaluate((css) => {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }, NEUTRALISING_STYLES);
  // Self-hosted Inter and Vazirmatn: a screenshot taken mid-swap is a
  // screenshot of the fallback stack.
  await page.evaluate(async () => {
    await document.fonts.ready;
  });
  await expect(page).toHaveScreenshot(`${name}.png`, {
    fullPage: true,
    animations: "disabled",
  });
}

/**
 * Signs in and waits for the one visible fact that says the realtime socket is
 * up. Without it the footer can still read "Offline" when the shutter falls.
 */
async function signInAndSettle(app: App, account: TestAccount, t: Translate, path = "/"): Promise<void> {
  await app.gotoSignIn(path);
  await app.signIn(account.username, account.password);
  await expect(app.identityButton).toContainText(t("chat.presence.online"));
}

/** The three channels the sidebar lists; returns the one to open into. */
async function seedChannelList(
  accounts: AccountFactory,
  account: TestAccount,
): Promise<string> {
  const session = await accounts.open(account.username, account.password);
  const ids: string[] = [];
  for (const slug of LIST_SLUGS) {
    ids.push(await createChannelApi(session, slug));
  }
  return ids[0] ?? "";
}

test.describe("Persian right-to-left baseline", () => {
  test("@fa-smoke every core screen declares rtl and fa", async ({ app, accounts, page, t }) => {
    test.setTimeout(120_000);

    const account = await accounts.createReady("e2ertldir", { isAdmin: true });
    const session = await accounts.open(account.username, account.password);
    const slug = uniqueSlug("rtldir");
    await createChannelApi(session, slug);

    await app.gotoSignIn();
    await expectRightToLeftDocument(page, "sign-in");

    await app.signIn(account.username, account.password);
    await expect(app.chatSidebar).toBeVisible();
    await expectRightToLeftDocument(page, "channel list");

    // The message is typed here rather than seeded over the API: the channel
    // is encrypted (ADR 011), so the only thing that can produce one is the
    // MLS client in this very browser. One person in their own channel needs
    // nobody else's device, which is why this screen can still arrange itself.
    await app.conversationRow(slug).click();
    await app.sendMessage(MESSAGES[0]);
    await expect(app.messageBodies).toHaveText([MESSAGES[0]], { timeout: 30_000 });
    await expectRightToLeftDocument(page, "message view");

    const dialog = await app.openSettings();
    await expect(dialog.getByRole("tablist", { name: t("settings.title") })).toBeVisible();
    await expectRightToLeftDocument(page, "user settings");

    // A hard navigation, not a client-side one: /admin used to 404 because the
    // server enumerates its routes, and a link-only test never saw it.
    await page.goto("/admin/invites");
    await expect(page.getByRole("heading", { name: t("admin.invites.title") })).toBeVisible();
    await expectRightToLeftDocument(page, "admin panel");
  });
});

test.describe("Persian right-to-left screenshots", () => {
  test.skip(
    !rendersOnLinux,
    "the committed baselines are Linux renderings — see this file's header for how to compare against them from here",
  );

  test("@fa-smoke sign-in", async ({ app, page }) => {
    await app.gotoSignIn();
    await expectBaseline(page, "sign-in");
  });

  test("@fa-smoke channel list", async ({ app, accounts, page, t }) => {
    const account = await accounts.createReady("e2ertllist", { displayName: AUTHOR_NAME });
    const first = await seedChannelList(accounts, account);

    // Opened explicitly rather than by landing on "/", which takes whichever
    // conversation the sidebar happens to list first.
    await signInAndSettle(app, account, t, `/c/${first}`);
    await expect(app.chatSidebar.getByRole("link")).toHaveCount(LIST_SLUGS.length);
    await app.declineBackupOffer();

    await expectBaseline(page, "channel-list");
  });

  test("@fa-smoke message view", async ({ app, accounts, openApp, page, t }) => {
    test.setTimeout(180_000);

    const author = await accounts.createReady("e2ertlmsg", { displayName: AUTHOR_NAME });
    const peer = await accounts.createReady("e2ertlpeer", { displayName: PEER_NAME });

    const authorSession = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorSession, MESSAGE_SLUG);
    await inviteApi(authorSession, channelId, peer.id);

    // The peer's browser first, and not for convenience: the channel is
    // encrypted, and a device that has never opened the app has published no
    // key packages for the author's client to claim. Opening them in this
    // order is what makes the peer a member of the group at all.
    const peerApp = await openApp(peer, `/c/${channelId}`);
    await expect(peerApp.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({
      timeout: 30_000,
    });

    await signInAndSettle(app, author, t, `/c/${channelId}`);

    // One from each side, then a second from the author: the own bubble, the
    // other person's bubble, and a continuation group all in one frame. Each
    // is typed into its own composer, because that is the only thing that
    // produces a message an encrypted channel will take.
    await app.sendMessage(MESSAGES[0]);
    await peerApp.sendMessage(MESSAGES[1]);
    await app.sendMessage(MESSAGES[2]);

    await expect(app.messageBodies).toHaveText([...MESSAGES], { timeout: 60_000 });
    await app.declineBackupOffer();

    await expectBaseline(page, "message-view");
  });

  test("@fa-smoke user settings", async ({ app, accounts, page, t }) => {
    const account = await accounts.createReady("e2ertlset", { displayName: AUTHOR_NAME });

    await signInAndSettle(app, account, t);
    const dialog = await app.openSettings();
    // Security opens first and loads two resources; both have to have landed
    // or the shutter falls on a skeleton.
    await expect(dialog.getByRole("button", { name: t("settings.totp.setUp") })).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: t("settings.security.manageSessions") }),
    ).toBeVisible();

    await expectBaseline(page, "user-settings");
  });

  /**
   * Invites, not Users.
   *
   * The users dashboard lists every account on the instance, and the instance
   * is shared by four workers creating accounts while this runs — the table,
   * the tally and the nav badge are all a number nobody can pin, and covering
   * them would leave a screenshot that is mostly mask. Invites is the same
   * AdminShell — the mirrored sidebar, nav, header and action button that
   * carry the right-to-left risk — over content a fresh instance fixes at
   * zero. The users dashboard has its own coverage at 375px in
   * mobile-layout.e2e.ts.
   */
  test("@fa-smoke admin panel", async ({ app, accounts, page, t }) => {
    const account = await accounts.createReady("e2ertladm", {
      displayName: AUTHOR_NAME,
      isAdmin: true,
    });

    await app.gotoSignIn("/admin/invites");
    await app.signIn(account.username, account.password);
    await expect(page.getByRole("heading", { name: t("admin.invites.empty.title") })).toBeVisible();

    await expectBaseline(page, "admin-panel");
  });
});
