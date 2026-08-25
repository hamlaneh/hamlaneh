import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * The sidebar's two counts, while the reader is looking somewhere else.
 *
 * They are different badges answering different questions — "something new
 * happened here" and "somebody needs YOU here" — and only the second one
 * involves the contract's `<@{user_id}>` token. So the mention is inserted the
 * way a person inserts one, through the composer's picker, rather than typed
 * as a literal: a test that hand-wrote the token would pass against a picker
 * that inserted display names.
 *
 * The channel slugs are letters only (support/chat.ts), so asserting on a
 * badge's numeral cannot accidentally match the channel's own name.
 */
test.describe("unread and mention counts", () => {
  test("someone else's message raises the unread count, and a mention raises the mention badge", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    const reader = await accounts.createReady("e2ereads");
    const writer = await accounts.createReady("e2ewrites");

    const writerApi = await accounts.open(writer.username, writer.password);
    const openSlug = uniqueSlug("open");
    const quietSlug = uniqueSlug("quiet");
    const openId = await createChannelApi(writerApi, openSlug);
    const quietId = await createChannelApi(writerApi, quietSlug);
    for (const channelId of [openId, quietId]) {
      await inviteApi(writerApi, channelId, reader.id);
    }

    // The reader is reading one channel; the counts under test belong to the
    // other one, which is the only state where a badge is drawn at all.
    await app.gotoSignIn(`/c/${openId}`);
    await app.signIn(reader.username, reader.password);
    await expect(app.identityButton).toContainText(t("chat.presence.online"));

    const quietRow = app.conversationRow(quietSlug);
    await expect(quietRow).toHaveAttribute("data-unread", "false");

    const writerApp = await openApp(writer, `/c/${quietId}`);
    await expect(writerApp.identityButton).toContainText(t("chat.presence.online"));

    await writerApp.sendMessage("Anybody know why staging is red?");
    await expect(quietRow).toHaveAttribute("data-unread", "true");
    await expect(quietRow).toContainText(`1 ${t("chat.sidebar.unreadLabel")}`);
    await expect(quietRow).not.toContainText(t("chat.sidebar.mentionsLabel"));

    // A mention: typed text, then the picker's `<@{user_id}>` token.
    await writerApp.composerField.fill("Your call");
    await writerApp.mentionInComposer(reader.displayName);
    await writerApp.composerField.press("Enter");

    await expect(quietRow).toContainText(`@1 ${t("chat.sidebar.mentionsLabel")}`);
    // A mention outranks the plain count: one badge is drawn, not two.
    await expect(quietRow).not.toContainText(t("chat.sidebar.unreadLabel"));

    // Opening the channel clears both, and the mention renders as a name
    // rather than as the raw token it travelled as.
    await quietRow.click();
    await expect(app.messageBodies.last()).toContainText(`@${reader.displayName}`);
    await expect(quietRow).toHaveAttribute("data-unread", "false");
  });
});
