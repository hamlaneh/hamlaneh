import { randomBytes } from "node:crypto";

import { App } from "../support/app";
import { uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import { FIRST_ADMIN_ANNOUNCEMENT, HomeInstance } from "../support/homestack";

/**
 * ROADMAP Phase 4, test gate clause 4, second half: "a sent message survives
 * process restart".
 *
 * The first half of that clause — the single binary starting on Windows, macOS
 * and Linux, and creating its SQLite database on first run — is a manual check
 * on three machines. This is the half a machine can hold, and it could not be
 * held by curl: a fresh instance is Strict (ADR 011), so a message can only be
 * sent by a client that can encrypt one, which means the real webapp driving
 * real MLS in a real browser. That is the whole reason this lives in the
 * Playwright suite rather than in a shell script.
 *
 * WHAT SURVIVES WHAT, precisely, because the two halves of an encrypted
 * message live in different places and only one of them is in the database:
 *
 *   the server holds  the message row in SQLite — an empty `content` and the
 *                     MLS ciphertext — plus the account, the session and the
 *                     channel. All of it under HAMLANEH_DATA_DIR, all of it
 *                     what the restart is a test of.
 *   the device holds  the MLS keystore and the local plaintext store, both in
 *                     this browser profile's IndexedDB. They are what turn the
 *                     ciphertext back into words, and the server has never
 *                     seen either.
 *
 * So the restart is a SERVER restart and the browser context deliberately
 * survives it: same context, same profile, same device. That is the useful
 * claim — a household closes the laptop lid and reopens the app. Destroying
 * the browser profile too would be a different and much weaker test: a device
 * that lost its keystore cannot read its own history, and that is correct MLS
 * behaviour rather than a persistence bug, so a spec that did it would either
 * assert nothing or assert the wrong thing.
 *
 * The vacuity control is the console. Home mode announces a generated first
 * administrator on a first run and only on a first run, so a second start that
 * announced one would be a start that found no database — and every assertion
 * after it would be measuring a fresh instance rather than a restarted one.
 */
test.describe("home mode", () => {
  test("a message sent before the process restarts is still readable after it", async ({
    browser,
    t,
  }) => {
    test.setTimeout(300_000);

    const home = await HomeInstance.start();
    try {
      // One context for the whole test, and it outlives the server restart —
      // see the header. `baseURL` is this instance's own origin, not the
      // compose stack's.
      const context = await browser.newContext({ baseURL: home.baseURL });
      const app = new App(await context.newPage(), t);

      // The generated first admin, signing in with the password the console
      // announced. It arrives with must_change_password, exactly as the
      // install flow leaves it.
      const settled = `settled-${randomBytes(10).toString("hex")}`;
      await app.gotoSignIn();
      await app.signIn(home.admin.username, home.admin.password);
      await expect(app.changePasswordHeading).toBeVisible();
      await app.completeForcedPasswordChange(home.admin.password, settled);
      await app.chatSidebar.waitFor();

      // A channel through the sidebar's "+", which on a Strict instance is
      // born encrypted (ADR 011 decision 1) — nothing here asks for that, and
      // nothing may: an instance that stopped encrypting by default is a
      // regression this spec should feel.
      const slug = uniqueSlug("nest");
      await app.createChannel(slug, "private");
      await expect(app.channelHeading).toHaveText(`#${slug}`);
      await expect(app.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({ timeout: 60_000 });
      const channelId = channelIdFromURL(app.page.url());

      // The composer refuses text until the MLS group can carry it, so
      // Playwright's actionability wait on the field is what synchronizes with
      // the bootstrap — the same mechanism the e2ee specs rely on.
      const canary = `home-canary-${randomBytes(8).toString("hex")}`;
      await app.sendMessage(canary);
      await expect(app.messageBodies).toHaveText([canary]);

      /* ── the restart ─────────────────────────────────────────────────── */

      // SIGTERM, gone, and a NEW process on the same data directory.
      const secondBoot = await home.restart();

      // The control. A restart that landed on an empty directory would mint
      // and announce a second administrator here, and everything below would
      // pass against a brand-new instance holding nothing.
      expect(secondBoot).not.toContain(FIRST_ADMIN_ANNOUNCEMENT);

      // The session survived too, which is itself part of the claim: it lives
      // in the database, so a reload that reached the shell rather than the
      // sign-in screen is evidence the row is still there.
      await app.page.reload();
      await app.chatSidebar.waitFor();
      await expect(app.conversationRow(slug)).toBeVisible();

      // The words, on the same device that typed them. This is the clause.
      await expect(app.messageBodies).toHaveText([canary], { timeout: 60_000 });

      /* ── and what the server actually holds ──────────────────────────── */

      // The row came back out of SQLite, with the ciphertext and without the
      // words: the message the screen above is showing was reassembled from a
      // stored ciphertext plus a key only this browser has. A response that
      // carried the canary would mean home mode had quietly stored plaintext.
      //
      // Asked from inside the page rather than through Playwright's own HTTP
      // client, because the session cookie is Secure and this origin is plain
      // http — a browser makes the localhost exception for that and a Node
      // client does not, so the request that reaches the server has to be the
      // browser's. It is the more honest question anyway: the same client that
      // is rendering the words is the one asking what the server kept.
      const stored = await app.page.evaluate(async (id: string) => {
        const response = await fetch(`/api/v1/channels/${id}/messages`);
        return { status: response.status, body: await response.text() };
      }, channelId);
      expect(stored.status).toBe(200);
      expect(stored.body).not.toContain(canary);

      const page = JSON.parse(stored.body) as MessagePage;
      expect(page.messages).toHaveLength(1);
      const [message] = page.messages;
      expect(message?.content).toBe("");
      expect(message?.mls?.ciphertext ?? "").not.toBe("");
    } finally {
      home.dispose();
    }
  });
});

/** The shape this spec reads out of MessagePage (docs/api/openapi.yaml). */
interface MessagePage {
  messages: { content: string; mls?: { epoch: number; ciphertext: string } }[];
}

/** Creating a channel opens it, so the id the API needs is in the URL. */
function channelIdFromURL(url: string): string {
  const id = /^\/c\/(?<id>[^/]+)$/u.exec(new URL(url).pathname)?.groups?.id;
  if (id === undefined) {
    throw new Error(`expected to be inside a channel, but the URL is ${url}`);
  }
  return id;
}
