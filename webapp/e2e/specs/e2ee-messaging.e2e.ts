import { randomBytes } from "node:crypto";

import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import { composeExec } from "../support/stack";

/**
 * The Phase 3 promise, met end to end for the first time: a message typed in
 * one browser is readable in another, and the server that carried it holds
 * only ciphertext.
 *
 * Nothing here is mocked and nothing is seeded through a back door. The
 * channel is created by the endpoint the "+" calls with the contract's e2ee
 * flag, both MLS devices come into being the way a user's browser creates
 * them — by opening the app — and the canary is typed into the real composer.
 * The ciphertext claim is then asserted where it actually matters: a
 * `pg_dump` from inside the database container, which is exactly what an
 * attacker with the server's disk would hold.
 *
 * Ordering note that is part of the design rather than test convenience: the
 * reader opens the app BEFORE the author does. A device that has never run
 * cannot have published key packages, and a client bootstrapping a group can
 * only add devices whose packages exist — the "cannot add yet" state. Opening
 * the reader first is the honest sequence, not a workaround.
 */
test.describe("end-to-end encryption", () => {
  test("a message crosses encrypted, and the database holds no plaintext", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    test.setTimeout(180_000);

    const author = await accounts.createReady("e2eeauthor");
    const reader = await accounts.createReady("e2eereader");

    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("sealed"), "private", {
      e2ee: true,
    });
    await inviteApi(authorApi, channelId, reader.id);

    // The vacuity control, following the key-leak scan's precedent: a dump in
    // which the canary is absent proves nothing unless the same dump is shown
    // to carry plaintext where plaintext is expected.
    //
    // It is a channel TOPIC rather than a message in a plaintext channel,
    // because since ADR 011 an instance is Strict and cannot create one — the
    // control would have to ask the product to do the thing this whole slice
    // exists to stop. A topic is better anyway: it is prose a person typed
    // that this server stores in the clear BY DESIGN, so finding it proves
    // the scan reads user-authored text out of the dump, which is exactly the
    // claim the canary's absence needs propped up.
    const nonce = randomBytes(12).toString("hex");
    const canary = `canary-${nonce}`;
    const control = `control-${nonce}`;
    await createChannelApi(authorApi, uniqueSlug("plain"), "private", { topic: control });

    // Reader first (see the header comment). Their app sees an encrypted
    // channel in the sidebar, starts MLS, registers its device and publishes
    // key packages; the indicator is the observable that the e2ee surface is
    // live, and "Online" is the observable that their socket is up.
    const readerApp = await openApp(reader, `/c/${channelId}`);
    await expect(readerApp.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({
      timeout: 30_000,
    });
    await expect(readerApp.identityButton).toContainText(t("chat.presence.online"));

    let readerNavigations = 0;
    readerApp.page.on("framenavigated", () => {
      readerNavigations += 1;
    });

    // Now the author. Opening the channel bootstraps the group: create,
    // claim the reader's packages, commit with the Welcome riding along.
    // sendMessage() fills the composer, and Playwright's actionability wait
    // on the disabled-until-ready field is what synchronizes with the
    // bootstrap — the composer refuses text until the group can carry it.
    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);
    await expect(app.page.getByText(t("chat.e2ee.indicator"))).toBeVisible();
    await app.sendMessage(canary);
    await expect(app.messageBodies).toHaveText([canary], { timeout: 30_000 });

    // The other screen: Welcome fetched, group joined, ciphertext decrypted —
    // with no reload, proven the same way the plaintext suite proves it.
    await expect(readerApp.messageBodies).toHaveText([canary], { timeout: 60_000 });
    expect(readerNavigations).toBe(0);

    // What the server holds. Row-level first: the stored message has empty
    // content and non-null ciphertext — the contract's invariant, asserted
    // against the real table rather than the API that promised it.
    const row = composeExec("db", [
      "psql",
      "-U",
      "hamlaneh",
      "-d",
      "hamlaneh",
      "-At",
      "-c",
      `SELECT content = '', mls_ciphertext IS NOT NULL, mls_epoch FROM messages WHERE channel_id = '${channelId}'`,
    ]).trim();
    expect(row).toBe("t|t|1");

    // Then the whole database, dumped from inside its own container. The
    // canary must be nowhere; the control must be found, or the scan could
    // not have found anything and the pass would be vacuous.
    const dump = composeExec("db", ["pg_dump", "-U", "hamlaneh", "hamlaneh"]);
    expect(dump).toContain(control);
    expect(dump).not.toContain(canary);

    // The limitation this used to pin is gone: MLS deletes a message key once
    // used, so the sender could not reopen its own words after a reload, and
    // this spec asserted that honestly. The local plaintext store (ROADMAP
    // Phase 3) now keeps what this device sent, wrapped in the keystore under
    // the same key as everything else, so the author's own history survives.
    //
    // What has NOT changed, and is asserted instead: the recipient's copy is
    // still only openable by their own device, and the server still holds
    // nothing but ciphertext — the dump above is the proof of that, and it ran
    // before this reload.
    await app.page.reload();
    await expect(app.messageBodies).toHaveText([canary], { timeout: 30_000 });
  });
});
