import { expect, test } from "../support/fixtures";

/**
 * A direct message, opened from the picker.
 *
 * A DM has no slug: everything that names it — the sidebar row, the header,
 * the composer's placeholder — is drawn from `dm_peer`, which is caller-scoped
 * (openapi.yaml -> Channel). "Caller-scoped" is the whole difficulty: the two
 * participants must each see the OTHER one's name, which is why this test
 * looks at both screens rather than only at the one that opened it.
 */
test.describe("direct messages", () => {
  test("opening a direct message names the other person, on both screens", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    const initiator = await accounts.createReady("e2edmfrom");
    const peer = await accounts.createReady("e2edmto");

    const peerApp = await openApp(peer);
    await expect(peerApp.identityButton).toContainText(t("chat.presence.online"));

    await app.gotoSignIn();
    await app.signIn(initiator.username, initiator.password);
    await expect(app.chatSidebar).toBeVisible();

    await app.startDirectMessage(peer.username, peer.displayName);

    await expect(app.channelHeading).toHaveText(peer.displayName);
    await expect(app.conversationRow(peer.displayName)).toBeVisible();
    // Scoped to the header: the sidebar's "Direct messages" heading contains
    // the same words, and an unscoped match would find both.
    await expect(app.page.locator("header")).toContainText(t("chat.header.directMessage"));

    // The same conversation, from the other side, named after the initiator —
    // never after the person reading it.
    await expect(peerApp.conversationRow(initiator.displayName)).toBeVisible();
    await expect(peerApp.conversationRow(peer.displayName)).toHaveCount(0);

    // The peer had no conversations at all, so the shell opens their only one
    // for them — which means the message lands in a channel they are reading,
    // and the header has to name the initiator there too.
    await app.sendMessage("Two of us, then.");
    await expect(peerApp.messageBodies).toHaveText(["Two of us, then."]);
    await expect(peerApp.channelHeading).toHaveText(initiator.displayName);
  });
});
