import type { Locator } from "@playwright/test";

import type { AccountFactory, TestAccount } from "../support/accounts";
import type { App } from "../support/app";
import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test, type OpenApp } from "../support/fixtures";
import type { Translate } from "../support/i18n";

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
 *
 * # Why this is two tests
 *
 * It was one, and one of the two badges stopped working. Leaving them together
 * would have meant marking the whole thing expected-to-fail, which would have
 * taken the unread count — which is fine, and which nothing else covers — out
 * of the green suite along with the broken one. Splitting keeps the working
 * half honest evidence and puts the marker on exactly the claim that is no
 * longer true. The arrangement they share is the function below.
 */

interface Stage {
  readerApp: App;
  writerApp: App;
  reader: TestAccount;
  quietRow: Locator;
}

/**
 * Two people in two channels, with the reader reading one of them.
 *
 * The counts under test belong to the OTHER channel, which is the only state
 * in which a badge is drawn at all. Both browsers are open and settled before
 * anything is sent: the writer's, because a message in an encrypted channel
 * can only come from an MLS client, and the reader's before it, because a
 * device that has not published key packages cannot be added to the group.
 */
async function stage(
  app: App,
  accounts: AccountFactory,
  openApp: OpenApp,
  t: Translate,
): Promise<Stage> {
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

  await app.gotoSignIn(`/c/${openId}`);
  await app.signIn(reader.username, reader.password);
  await expect(app.identityButton).toContainText(t("chat.presence.online"));
  // Encryption running, not merely signed in: this is the observable that the
  // reader's device has registered and published the key packages the writer's
  // client has to claim before it can add them to the group.
  await expect(app.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({ timeout: 30_000 });

  const quietRow = app.conversationRow(quietSlug);
  await expect(quietRow).toHaveAttribute("data-unread", "false");

  const writerApp = await openApp(writer, `/c/${quietId}`);
  await expect(writerApp.identityButton).toContainText(t("chat.presence.online"));

  return { readerApp: app, writerApp, reader, quietRow };
}

test.describe("unread and mention counts", () => {
  test("someone else's message raises the unread count", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    test.setTimeout(120_000);

    const { writerApp, quietRow } = await stage(app, accounts, openApp, t);

    await writerApp.sendMessage("Anybody know why staging is red?");
    await expect(quietRow).toHaveAttribute("data-unread", "true");
    await expect(quietRow).toContainText(`1 ${t("chat.sidebar.unreadLabel")}`);
    await expect(quietRow).not.toContainText(t("chat.sidebar.mentionsLabel"));

    // Opening the channel clears it.
    await quietRow.click();
    await expect(quietRow).toHaveAttribute("data-unread", "false");
  });

  /**
   * EXPECTED TO FAIL — mentions no longer reach anybody, and the product does
   * not say so.
   *
   * The mention badge is server-derived: `storage.CreateMessage` runs
   * `ParseMentions(nm.Content)` over the message body and writes a
   * `message_mentions` row for every member it names. On an encrypted channel
   * `content` is the empty string by construction — the contract requires it,
   * and `e2ee.go` refuses the write otherwise — so the parse runs over nothing
   * and no mention row is ever created. ADR 011 made every conversation
   * encrypted, so this is now true everywhere: `@` notifies nobody, instance
   * wide.
   *
   * What makes it worth a red test rather than a note is that nothing tells
   * anyone. The composer still offers the mention picker in every channel, it
   * still inserts the `<@{user_id}>` token, the recipient's client still
   * renders it as the person's name once decrypted — so both people see a
   * mention that happened, and only the badge that was supposed to carry it
   * anywhere is missing. A refusal would be a feature; this is silence.
   *
   * The assertions are untouched. Fixing this is a design decision about
   * whether a client may tell the server whom it mentioned — which leaks the
   * social graph of an encrypted conversation to the server that is supposed
   * to be excluded from it — and that is an ADR, not an edit to this file.
   */
  test("a mention raises the mention badge", async ({ app, accounts, openApp, t }) => {
    // Scoped to this test, not the group: the count above still works and has
    // to stay real evidence.
    test.fail();
    test.setTimeout(120_000);

    const { writerApp, reader, quietRow } = await stage(app, accounts, openApp, t);

    await writerApp.sendMessage("Anybody know why staging is red?");
    await expect(quietRow).toHaveAttribute("data-unread", "true");

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
