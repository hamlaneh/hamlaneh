import { uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * Creating a channel and inviting somebody into it, entirely through the
 * screens — the sidebar's "+", the create dialog, the empty state's "Invite
 * people" and the picker behind it.
 *
 * The invited person is already signed in and watching when the invitation
 * lands, because ws-protocol.md §4 says a `channel_created` reaches somebody
 * an invite just made a member ("The sidebar adds a row"). Asserting it after
 * a reload would test the REST list instead and leave the event unproven.
 */
test.describe("channels", () => {
  test("a channel is created from the sidebar and the person invited sees it appear", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    const owner = await accounts.createReady("e2eowner");
    const guest = await accounts.createReady("e2eguest");

    const guestApp = await openApp(guest);
    await expect(guestApp.identityButton).toContainText(t("chat.presence.online"));

    await app.gotoSignIn();
    await app.signIn(owner.username, owner.password);
    await expect(app.chatSidebar).toBeVisible();

    const slug = uniqueSlug("team");
    await app.createChannel(slug, "private");

    // Creating it opens it: the header names it and the sidebar carries a row.
    await expect(app.channelHeading).toHaveText(`#${slug}`);
    await expect(app.conversationRow(slug)).toBeVisible();
    // A channel with one member has nothing in it, and says so honestly.
    await expect(app.page.getByText(t("chat.empty.onlyYou"))).toBeVisible();

    await app.openInvite();
    await app.invitePerson(guest.username, guest.displayName);

    // The guest never reloaded.
    await expect(guestApp.conversationRow(slug)).toBeVisible();

    // And it is a real membership, not just a row: they can read and write.
    await guestApp.conversationRow(slug).click();
    await expect(guestApp.channelHeading).toHaveText(`#${slug}`);
    await guestApp.sendMessage("Thanks for the invite.");
    await expect(app.messageBodies).toHaveText(["Thanks for the invite."]);
  });
});
