import { createChannelApi, inviteApi, sendMessageApi, uniqueSlug } from "../support/chat";
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

    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("live"));
    await inviteApi(authorApi, channelId, reader.id);
    const seeded = "Ready when you are.";
    await sendMessageApi(authorApi, channelId, seeded);

    // The reader is on their own browser context — a second tab would be the
    // same person, which proves nothing about delivery to somebody else.
    const readerApp = await openApp(reader, `/c/${channelId}`);
    await expect(readerApp.messageBodies).toHaveText([seeded]);
    // Their socket has to be up BEFORE anything is sent; otherwise a pass
    // could come from history that happened to load at the right moment.
    await expect(readerApp.identityButton).toContainText(t("chat.presence.online"));

    let readerNavigations = 0;
    readerApp.page.on("framenavigated", () => {
      readerNavigations += 1;
    });

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);
    await expect(app.messageBodies).toHaveText([seeded]);

    const live = "Rolling to canary in ten minutes.";
    await app.sendMessage(live);

    // The author's own send is confirmed by the server, not just optimistic:
    // the pending bubble is replaced by the stored message.
    await expect(app.messageBodies).toHaveText([seeded, live]);
    // And the whole point — the other screen, untouched.
    await expect(readerApp.messageBodies).toHaveText([seeded, live]);
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
