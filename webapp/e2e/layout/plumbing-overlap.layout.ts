import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

import { translate } from "../support/i18n";

/**
 * Nothing an account is offered may land on top of something it can press.
 *
 * THE BUG THIS EXISTS FOR, three times over. `.hm-plumbing` used to mean two
 * things at once — "this surface has no design yet" and "this surface floats"
 * — so every new undesigned surface floated whether or not it was a popover.
 * The encryption notice covered the call strip and media E2EE could not be
 * started at all; the backup offer covered the invite dialog and ate its
 * clicks; the same offer, patched to sit below the dialogs, still covered the
 * call strip, and once every conversation became encrypted it greeted every
 * new account there. Each fix was a new modifier on the same floating class,
 * and each left the next surface holding the same loaded gun.
 *
 * The base class no longer positions anything (chat.css), and the surfaces
 * that really are popovers ask for `--overlay`. These four checks are what
 * holds that: three of them press the controls a person presses, and the
 * fourth states the rule directly so a reintroduced `position: absolute` fails
 * by name rather than by whichever control it happened to land on.
 *
 * `click({ trial: true })` is the assertion in the first two. It runs
 * Playwright's full actionability check — visible, stable, enabled, RECEIVES
 * POINTER EVENTS — and stops short of the click, so it fails with the exact
 * "subtree intercepts pointer events" this bug produced, without navigating
 * anywhere. A `toBeVisible()` would pass with the offer sitting right on top.
 */

/**
 * Obviously-fake, and the source of truth is `FIXTURE_CREDENTIALS` in
 * `src/mocks/handlers.ts` — these only ever reach MSW. Duplicated rather than
 * imported: pulling the handler module into the Node-side spec would drag msw
 * and the whole contract mock in to read two strings, and a drift here fails
 * loudly on the sign-in screen rather than passing quietly.
 */
const MOCK_IDENTIFIER = "fixture.admin";
const MOCK_PASSWORD = "fixture-password-only-for-mocks"; // gitleaks:allow

/** The layout tier runs one language; the keys keep it off English sentences. */
function t(key: string): string {
  return translate("en", key);
}

/**
 * Signs in and lands in a conversation showing the backup offer.
 *
 * The offer needs two things: an MLS device that reached `ready`, and an
 * account that has made no backup decision. A fresh mock page is the second,
 * and opening an encrypted conversation is the first — so the channel is
 * created here rather than picked from the fixtures, whose channels predate
 * strict mode and are all plaintext. The instance mock is `encryption_mode:
 * "strict"`, so a channel created through this dialog is born encrypted, which
 * is the arrangement every new account on a current instance actually meets.
 */
async function conversationWithBackupOffer(page: Page): Promise<void> {
  await page.goto("/");

  await page.getByLabel(t("login.identifierLabel")).fill(MOCK_IDENTIFIER);
  await page.getByLabel(t("login.passwordLabel"), { exact: true }).fill(MOCK_PASSWORD);
  await page.getByRole("button", { name: t("login.submit"), exact: true }).click();

  await page.getByRole("button", { name: t("chat.sidebar.createChannel") }).click();
  const dialog = page.getByRole("dialog", { name: t("chat.createChannel.title") });
  await dialog.getByLabel(t("chat.createChannel.nameLabel")).fill("layout-check");
  await dialog.getByRole("button", { name: t("chat.createChannel.submit") }).click();

  await expect(backupOffer(page)).toBeVisible();
}

function backupOffer(page: Page) {
  return page.getByRole("region", { name: t("chat.e2ee.backup.offerTitle") });
}

test("the call control can be pressed while the backup offer is on screen", async ({ page }) => {
  await conversationWithBackupOffer(page);

  const strip = page.getByRole("region", { name: t("calls.strip.label") });
  await expect(strip).toBeVisible();
  // Fails as "…intercepts pointer events" if anything covers the control.
  await strip.getByRole("button").click({ trial: true });
});

test("the invite dialog can be pressed while the backup offer is on screen", async ({ page }) => {
  await conversationWithBackupOffer(page);

  // The empty-channel invitation opens it; a newly created channel is empty.
  await page.getByRole("button", { name: t("chat.empty.invite") }).click();

  const picker = page.getByRole("dialog", { name: t("chat.empty.invite") });
  await expect(picker).toBeVisible();
  // The bug the second fix was for: the offer floated over this and swallowed
  // its clicks. A dialog IS allowed to float — over the conversation, not
  // under an unasked-for prompt.
  await picker.getByRole("button").first().click({ trial: true });
  await expect(backupOffer(page)).toBeVisible();
});

test("the conversation column still fills the shell with the offer in flow", async ({ page }) => {
  await conversationWithBackupOffer(page);

  // The recorded objection to putting these in the document flow: at the shell
  // ROOT they collapsed the chat's height and everything inside went to zero.
  // They did — that root is the sidebar/conversation ROW, so an in-flow child
  // there is a third column. The conversation column is a vertical stack and
  // absorbs a banner correctly, provided the banner is capped (chat.css). This
  // is the proof of that, not the reasoning about it.
  const viewport = page.viewportSize();
  const shell = await page.locator(".hm-chat").boundingBox();
  const messages = await page.locator(".hm-messages").boundingBox();
  const offer = await backupOffer(page).boundingBox();
  const composer = await page
    .locator("form")
    .filter({ has: page.getByRole("textbox") })
    .first()
    .boundingBox();
  expect([viewport, shell, messages, offer, composer]).not.toContainEqual(null);
  if (
    viewport === null ||
    shell === null ||
    messages === null ||
    offer === null ||
    composer === null
  ) {
    return;
  }

  expect(Math.round(shell.height)).toBe(viewport.height);
  // Not "greater than zero": a one-pixel strip passes that and is exactly as
  // broken. The conversation outweighing the unasked-for prompt above it is
  // the property, and it is the one the cap in chat.css exists to keep.
  expect(messages.height).toBeGreaterThan(offer.height);
  // The offer pushed rather than covered: the list ends where the composer
  // begins, and the composer ends inside the shell rather than below it. The
  // shell clips its overflow, so a composer pushed past the bottom edge is a
  // composer nobody can type in.
  expect(messages.y + messages.height).toBeLessThanOrEqual(composer.y + 1);
  expect(composer.y + composer.height).toBeLessThanOrEqual(shell.y + shell.height + 1);
});

/**
 * Every `.hm-plumbing` currently on screen that disagrees with the rule, named
 * with the position it actually resolved to.
 *
 * The rule, both ways round: `--overlay` is positioned, everything else is in
 * the document flow. Stated like this rather than as a list of controls, so the
 * moment the base class positions anything again — or a notice is handed the
 * popover treatment — this names the offender instead of leaving the next slice
 * to discover it through a button that does nothing.
 */
function surfacesBreakingTheRule(page: Page): Promise<string[]> {
  return page.locator(".hm-plumbing").evaluateAll((nodes) =>
    nodes
      .filter((node) => {
        const positioned = getComputedStyle(node).position !== "static";
        return positioned !== node.classList.contains("hm-plumbing--overlay");
      })
      .map((node) => `${node.getAttribute("class") ?? ""} => ${getComputedStyle(node).position}`),
  );
}

test("an undesigned surface stays in flow unless it opts into the overlay", async ({ page }) => {
  await conversationWithBackupOffer(page);

  // The encryption notice and the backup offer are already up, and they are
  // what `.hm-plumbing` still means: the picker and the account menu left the
  // class behind when their delivered design landed (2026-09-02). The rule is
  // asked with each of them open anyway, because what it guards is the
  // NOTICES underneath — a designed overlay opening above them is exactly the
  // moment the old bug used to surface.
  //
  // The picker is dismissed before the account menu rather than being closed
  // by it. It is a modal dialog now, with the scrim the mobile drawer uses
  // (chat-addendum-overlay-components §01), so nothing behind it is
  // pressable — which is the design working, and the reason the old
  // click-straight-through flow timed out here rather than in the product.
  await page.getByRole("button", { name: t("chat.empty.invite") }).click();
  await expect(page.getByRole("dialog", { name: t("chat.empty.invite") })).toBeVisible();
  expect(await surfacesBreakingTheRule(page)).toEqual([]);

  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: t("chat.empty.invite") })).toBeHidden();

  await page.getByRole("button", { name: t("account.title") }).click();
  await expect(page.getByRole("dialog", { name: t("account.title") })).toBeVisible();
  expect(await surfacesBreakingTheRule(page)).toEqual([]);
});
