import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * The promise slice 1.2a exists to keep: pick a channel, read its history,
 * send a message, and have it appear on somebody else's screen without a
 * refresh.
 *
 * Nothing here is mocked and nothing is short-circuited. The send happens in
 * the composer, the delivery happens over the real WebSocket the gateway
 * serves, and the reader's page is never reloaded — the test counts frame
 * navigations to prove it, because "it appeared" would otherwise be satisfied
 * by an accidental reload.
 *
 * # What ADR 011 changed about the first test, and what it cost
 *
 * The reader used to open onto a channel that already had a message in it, so
 * one test covered both "history loads" and "the next one arrives live". That
 * arrangement is not merely inconvenient now, it is unreachable: the channel
 * is end-to-end encrypted, and a device is added to an MLS group at an epoch —
 * a message sent before the reader's device existed cannot be opened by it
 * afterwards, by design and permanently. So the reader opens FIRST (which is
 * also what publishes their key packages, without which the author could not
 * add them at all) and watches both messages arrive.
 *
 * **This test therefore no longer covers "a reader sees history that predates
 * them", and nothing else does either, because the product no longer does it.**
 * What survives the change is the whole of the live-delivery claim, which is
 * what the test is named for. History that a device produced ITSELF is still
 * covered, by the reload test below — that is the branch where it is still
 * true, and the asymmetry between the two is MLS working rather than failing.
 */
test.describe("messaging", () => {
  test("a message sent in one browser appears in another without a reload", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    const author = await accounts.createReady("e2eauthor");
    const reader = await accounts.createReady("e2ereader");

    test.setTimeout(120_000);

    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("live"));
    await inviteApi(authorApi, channelId, reader.id);

    // The reader is on their own browser context — a second tab would be the
    // same person, which proves nothing about delivery to somebody else — and
    // they open before the author for the protocol reason in the header.
    const readerApp = await openApp(reader, `/c/${channelId}`);
    // Their socket has to be up BEFORE anything is sent; otherwise a pass
    // could come from a page load that happened at the right moment.
    await expect(readerApp.identityButton).toContainText(t("chat.presence.online"));
    // And their device has to be running encryption, not merely signed in:
    // this is the observable that key packages exist for the author to claim.
    await expect(readerApp.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({
      timeout: 30_000,
    });

    let readerNavigations = 0;
    readerApp.page.on("framenavigated", () => {
      readerNavigations += 1;
    });

    // Now the author. Opening the channel bootstraps the group; the composer
    // stays disabled until it can carry a message, and Playwright's
    // actionability wait on it is what synchronises with that.
    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);

    const seeded = "Ready when you are.";
    await app.sendMessage(seeded);
    await expect(app.messageBodies).toHaveText([seeded], { timeout: 30_000 });

    const live = "Rolling to canary in ten minutes.";
    await app.sendMessage(live);

    // The author's own send is confirmed by the server, not just optimistic:
    // the pending bubble is replaced by the stored message.
    await expect(app.messageBodies).toHaveText([seeded, live]);
    // And the whole point — the other screen, untouched.
    await expect(readerApp.messageBodies).toHaveText([seeded, live], { timeout: 60_000 });
    expect(readerNavigations).toBe(0);
  });

  test("history survives a reload, in reading order", async ({ app, accounts }) => {
    const account = await accounts.createReady("e2ehistory");
    const api = await accounts.open(account.username, account.password);
    const channelId = await createChannelApi(api, uniqueSlug("history"));

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(account.username, account.password);

    // Sent one at a time from the composer, so the order under test is the
    // order they were written in rather than the order a batch happened to
    // commit in.
    const lines = ["First, the plan.", "Then, the objection.", "Finally, the decision."];
    for (const line of lines) {
      await app.sendMessage(line);
      await expect(app.messageBodies.last()).toHaveText(line);
    }

    // A reload throws away every bit of React state, so what comes back is
    // the stored history, paged and ordered by the server.
    await app.page.reload();
    await expect(app.messageBodies).toHaveText(lines);
  });
});
