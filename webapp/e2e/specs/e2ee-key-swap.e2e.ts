import type { Page } from "@playwright/test";

import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * Poisons every directory answer for one member with extra signature keys —
 * the server-that-lies this spec's adversary is made of.
 *
 * The try/catch is not about the poisoning, which cannot fail; it is about
 * WHEN the handler runs. The client polls this endpoint on a timer, so a
 * request is in flight whenever the page reloads (twice in this spec) or
 * closes (once, in teardown) — and at that moment Playwright disposes the
 * fetched response mid-handler. Left unhandled, the throw is attributed to
 * the spec after every real assertion has already passed, which is how
 * main's first post-merge run went red on a test whose subject was fine.
 * An aborted poll is indistinguishable from a dropped one to the client,
 * which retries on its own timer; the next answer is poisoned the same way.
 */
async function plantDirectoryKeys(page: Page, userId: string, keys: string[]): Promise<void> {
  await page.route("**/api/v1/channels/*/mls/member-devices*", async (route) => {
    try {
      const response = await route.fetch();
      const body = (await response.json()) as {
        members: { user_id: string; signature_public_keys: string[] }[];
      };
      for (const member of body.members) {
        if (member.user_id === userId) {
          member.signature_public_keys = [...member.signature_public_keys, ...keys];
        }
      }
      await route.fulfill({ response, json: body });
    } catch {
      await route.abort().catch(() => undefined);
    }
  });
}

/**
 * ROADMAP Phase 3's key-swap gate, met as written: "an adversarial test
 * server substitutes a device key mid-conversation → client surfaces a
 * verification warning and refuses to silently encrypt to the new key."
 *
 * The adversary is played by intercepting the one answer the client's trust
 * rests on. ADR 007 named the residue precisely — the sweep believes the
 * directory's key↔person mapping — and ADR 008 built the defence, so the
 * attack this drives is exactly the one the design concedes is possible:
 * a server that attributes an attacker's key to a member who really is in
 * the channel. No production seam exists or is needed for it; the directory
 * read is an ordinary request and Playwright rewrites the response.
 *
 * What is asserted is the whole of the roadmap's sentence and not the easy
 * half of it. Surfacing a warning is cheap; refusing to send is the part
 * that costs something, so the composer's refusal is asserted directly, and
 * then the acceptance path is asserted to lift it — because a block with no
 * way out would pass a naive reading of the same sentence while making the
 * product unusable.
 */
test.describe("key swap", () => {
  test("a substituted device key warns and stops sending until a person decides", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    test.setTimeout(180_000);

    const author = await accounts.createReady("swapauthor");
    const peer = await accounts.createReady("swappeer");

    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("swapped"), "private", {
      e2ee: true,
    });
    await inviteApi(authorApi, channelId, peer.id);

    // The peer's browser first, so their device exists and has published key
    // packages before the author bootstraps the group — the ordering the
    // encryption suite explains and the protocol requires.
    const peerApp = await openApp(peer, `/c/${channelId}`);
    await expect(peerApp.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({
      timeout: 30_000,
    });

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);
    await expect(app.page.getByText(t("chat.e2ee.indicator"))).toBeVisible();

    // Baseline: sending works, and the composer is enabled. Without this the
    // later refusal would be indistinguishable from a conversation that had
    // never become sendable at all — the failure mode a green-looking test
    // would hide.
    await app.sendMessage("before the swap");
    await expect(app.messageBodies).toHaveText(["before the swap"], { timeout: 30_000 });

    // The substitution. Every entry the directory returns for the peer gains
    // a key their device does not hold and never published — the shape of a
    // server that lies about whose key is whose. The author's own entry is
    // untouched on purpose: ADR 008's non-circularity rule means their own
    // half of the safety number comes from their own signer, so an attack on
    // their own row would be a different test.
    const planted = Buffer.from(`planted-key-${peer.id}`).toString("base64");
    await plantDirectoryKeys(app.page, peer.id, [planted]);

    // Reopening is what forces the reconcile that reads the directory. The
    // reload also throws away every scrap of in-memory state, so what the
    // client concludes it concludes from the poisoned answer alone.
    await app.page.reload();

    // The warning, naming the person and what changed about them.
    await expect(app.page.getByText(t("chat.e2ee.verification.blockedTitle"))).toBeVisible({
      timeout: 60_000,
    });
    await expect(
      app.page.getByText(t("chat.e2ee.verification.newDevice", { name: peer.displayName })),
    ).toBeVisible();

    // And the half that costs something: the composer is gone, so there is
    // nothing to type into and nothing that could encrypt to the planted key.
    await expect(app.composerField).toHaveCount(0);

    // Reading is explicitly unaffected — ADR 008 refuses to block it, because
    // hiding the conversation would remove the context a person needs to
    // judge the warning, and the sweep is itself a defence that must keep
    // running.
    await expect(app.page.getByText(t("chat.e2ee.verification.readingContinues"))).toBeVisible();

    // The way out, and there are exactly two: compare, or accept. Accepting
    // records the set that is actually there — the planted key included, which
    // is the honest consequence of a human saying they checked — and sending
    // resumes.
    await app.page.getByRole("button", { name: t("chat.e2ee.verification.accept") }).click();
    await expect(app.page.getByText(t("chat.e2ee.verification.blockedTitle"))).toHaveCount(0, {
      timeout: 30_000,
    });
    await expect(app.composerField).toBeEnabled({ timeout: 30_000 });

    // Enabled is not the same as working: an unblocked composer over a gate
    // that still refuses would leave the message queued and the person told
    // nothing. Sending for real is what proves the decision actually restored
    // the thing it was blocking.
    await app.sendMessage("after the swap");
    // Both, and the earlier one is the interesting half: it was sent before
    // the reload, and MLS deleted the key that opened it the moment it was
    // used. It is readable because the local plaintext store now keeps what
    // this device sent (ROADMAP Phase 3), wrapped in the keystore under the
    // same key as everything else. This spec asserted only the new message
    // while that store did not exist, and the sentence explaining why is gone
    // with the limitation it described.
    await expect(app.messageBodies).toHaveText(["before the swap", "after the swap"], {
      timeout: 30_000,
    });

    // The acceptance is per key set, not a standing permission: a second
    // substitution has to be decided on its own. This is what stops one
    // "I checked" from blessing every future swap, which is the difference
    // between a decision and a switch.
    const second = Buffer.from(`planted-again-${peer.id}`).toString("base64");
    await app.page.unrouteAll({ behavior: "ignoreErrors" });
    await plantDirectoryKeys(app.page, peer.id, [planted, second]);
    await app.page.reload();
    await expect(app.page.getByText(t("chat.e2ee.verification.blockedTitle"))).toBeVisible({
      timeout: 60_000,
    });

    // The mid-spec unroute above already follows this rule; teardown gets the
    // same courtesy, so no poll handler is left in flight to race the page
    // close it cannot survive.
    await app.page.unrouteAll({ behavior: "ignoreErrors" });
    await peerApp.page.close();
  });
});
