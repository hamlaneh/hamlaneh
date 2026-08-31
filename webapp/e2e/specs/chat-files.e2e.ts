import { createChannelApi, inviteApi, uniqueSlug, uploadFileApi } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * What Phase 1.3 promises, asserted against the real stack: a file picked in
 * the composer reaches somebody else's screen as a card, and the bytes that
 * come back out can never become a page.
 *
 * The last two specs assert on the RESPONSE rather than on the UI on purpose.
 * "The browser did not run it" is a fact about headers and about what the
 * navigation did, and a screenshot of a chat window is not evidence of either.
 */

/** A real 1×1 PNG: the server decodes the bytes and refuses a mislabelled file. */
const PNG_1X1 = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);

test.describe("files", () => {
  /**
   * EXPECTED TO FAIL — the product cannot do this right now, and the whole of
   * file sharing is what it cannot do.
   *
   * ADR 011 made Strict the default and the only selectable mode, so every
   * conversation an instance creates is end-to-end encrypted. Encrypted
   * attachments are not built: the upload route, the send path and the edit
   * path all answer `400 e2ee_attachments_unsupported`, deliberately and in
   * one place (server/internal/httpserver/e2ee.go). The two facts together
   * mean there is no conversation on a fresh install into which any file can
   * be uploaded — file sharing, one of the four features the project leads
   * with, is unreachable rather than degraded.
   *
   * Nothing below is weakened, skipped or re-aimed at something easier. Every
   * assertion these four tests ever made is still here and still runs; they
   * fail at their first upload, which is the honest place to fail. `test.fail`
   * rather than `test.fixme` for the reason this file already documents from
   * the last time it was used: a test marked this way goes RED the moment it
   * starts passing, so the marker cannot outlive the gap it records.
   *
   * What closes it is encrypted attachments, not a change here. When they
   * land, this marker comes off and Playwright will insist on it.
   */
  test.fail();

  test("an image attached in the composer is stored and comes back as a card", async ({
    app,
    accounts,
  }) => {
    const author = await accounts.createReady("e2efileauthor");
    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("files"));

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);

    const filename = "canary.png";
    await app.attachFile({ name: filename, mimeType: "image/png", buffer: PNG_1X1 });
    // The tray card is filled from the upload's own 201, before any send.
    await expect(app.composerForm.getByText(filename)).toBeVisible();

    // No caption at all: a message may carry files and no text (openapi.yaml
    // -> SendMessageRequest.content), and the send path must not refuse it.
    await app.sendMessage("");
    await expect(app.messageLog.getByText(filename)).toBeVisible();

    // A reload throws away every bit of client state, so what comes back is
    // the stored attachment re-serialized with a freshly signed URL — the
    // only way to see that the ids really rode along with the send.
    await app.page.reload();
    await expect(app.messageLog.getByText(filename)).toBeVisible();
  });

  test("an image attached in one browser appears as a card in another, live", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    // This was marked `test.fail()` when it was written: the gateway kept its
    // own copy of the message serializer, which hard-coded an empty
    // attachments array and knew nothing about previews, so a message with
    // files arrived live carrying no cards. The two copies are now one
    // mapping in internal/api, the gateway holds a signer, and the marker
    // came off the moment Playwright reported this passing unexpectedly —
    // which is exactly what marking it rather than weakening it was for.

    const author = await accounts.createReady("e2eliveauthor");
    const reader = await accounts.createReady("e2elivereader");

    const authorApi = await accounts.open(author.username, author.password);
    const channelId = await createChannelApi(authorApi, uniqueSlug("live"));
    await inviteApi(authorApi, channelId, reader.id);

    // The reader is on their own browser context, with their socket up before
    // anything is sent — otherwise a pass could come from a well-timed load.
    const readerApp = await openApp(reader, `/c/${channelId}`);
    await expect(readerApp.identityButton).toContainText(t("chat.presence.online"));

    let readerNavigations = 0;
    readerApp.page.on("framenavigated", () => {
      readerNavigations += 1;
    });

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(author.username, author.password);

    const filename = "canary.png";
    await app.attachFile({ name: filename, mimeType: "image/png", buffer: PNG_1X1 });
    await app.sendMessage("A picture of the canary.");

    await expect(readerApp.messageLog.getByText("A picture of the canary.")).toBeVisible();
    // The card, on the other screen, without a reload.
    await expect(readerApp.messageLog.getByText(filename)).toBeVisible();
    expect(readerNavigations).toBe(0);
  });

  test("a non-image download is handed over as a file, never sniffed", async ({ accounts }) => {
    const account = await accounts.createReady("e2efiledl");
    const api = await accounts.open(account.username, account.password);
    const channelId = await createChannelApi(api, uniqueSlug("dl"));

    const attachment = await uploadFileApi(api, channelId, {
      name: "rollout-notes.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("Canary at 10%.", "utf8"),
    });

    // The signed URL is the whole credential — this request carries no
    // session of its own beyond the context it happens to be made from.
    const response = await api.context.get(attachment.url);
    expect(response.status()).toBe(200);
    const headers = response.headers();
    expect(headers["content-disposition"]).toMatch(/^attachment/u);
    expect(headers["x-content-type-options"]).toBe("nosniff");
    // The stored label is not handed back: an opaque blob is named as one.
    expect(headers["content-type"]).toBe("application/octet-stream");
  });

  test("an uploaded HTML file is saved, never executed", async ({ app, accounts }) => {
    const account = await accounts.createReady("e2efilehtml");
    const api = await accounts.open(account.username, account.password);
    const channelId = await createChannelApi(api, uniqueSlug("trap"));

    const attachment = await uploadFileApi(api, channelId, {
      name: "trap.html",
      mimeType: "text/html",
      buffer: Buffer.from(
        "<!doctype html><title>trap</title><script>window.__hamlanehUploadEscaped?.();</script>",
        "utf8",
      ),
    });

    await app.gotoSignIn(`/c/${channelId}`);
    await app.signIn(account.username, account.password);
    await app.chatSidebar.waitFor();

    // A binding the uploaded script can reach IF it ever runs. Installed on
    // the page, so it survives the navigation below into every frame.
    let escaped = false;
    await app.page.exposeFunction("__hamlanehUploadEscaped", () => {
      escaped = true;
    });
    const before = app.page.url();

    // The reader clicks the card. Content-Disposition: attachment turns that
    // into a download, so the navigation aborts rather than committing — the
    // rejection is the expected outcome, not a failure.
    const saved = app.page.waitForEvent("download");
    await app.page.goto(attachment.url).catch(() => undefined);
    await saved;

    expect(escaped).toBe(false);
    // The app document is still the app document; nothing was replaced.
    expect(app.page.url()).toBe(before);
    await expect(app.chatSidebar).toBeVisible();
  });
});
