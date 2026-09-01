import { createChannelApi, seedMessages, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";

/**
 * The IDOR property the whole authz matrix exists for, asserted where a user
 * experiences it: a private channel somebody else owns is unreachable by URL
 * and undeliverable to a socket that is listening.
 *
 * The Go matrix already proves the REST and WS rules endpoint by endpoint.
 * What it cannot prove is that the client asks for what it was told to ask
 * for — a shell that quietly rendered a 404 body, or a socket that received a
 * frame nobody drew, would pass the matrix and fail here.
 *
 * The outsider has a conversation of their own, so "nothing is here" is a
 * statement about this channel rather than about an empty account.
 *
 * # Where the secret lives, and why it moved
 *
 * It used to be a message. Since ADR 011 a message is ciphertext the server
 * cannot read, so "the outsider's socket never carried the secret" would have
 * been true of a socket that leaked every frame in the channel — the strongest
 * possible pass for the weakest possible reason. A vacuous assertion is worse
 * than a missing one, because it reads like coverage.
 *
 * The secret is therefore the channel's TOPIC: prose a person typed that this
 * server stores and serves in the clear BY DESIGN, and that rides inside the
 * Channel payload of the very frames this test is about. That keeps the claim
 * exactly what it was — the gateway does not hand a non-member content it
 * holds in the clear — and takes encryption out of the argument entirely.
 * e2ee-messaging.e2e.ts reaches for a topic as its plaintext control for the
 * same reason.
 *
 * The message sends that remain are here for their ORDERING, which is the only
 * job they ever really had: they are what gives the private channel's frame
 * its chance to arrive before the absence below is read as evidence.
 */
test.describe("channel isolation", () => {
  test("a non-member reaches nothing by URL and receives none of the channel's frames", async ({
    app,
    accounts,
    openApp,
    t,
  }) => {
    test.setTimeout(120_000);

    const insider = await accounts.createReady("e2einsider");
    const outsider = await accounts.createReady("e2eoutsider");

    const insiderApi = await accounts.open(insider.username, insider.password);
    const privateSlug = uniqueSlug("vault");
    const secret = "The staging database password rotates on Friday.";
    const privateId = await createChannelApi(insiderApi, privateSlug, "private", {
      topic: secret,
    });
    // The insider's own browser, because a message in an encrypted channel can
    // only come from an MLS client (support/chat.ts). It stays open: the
    // ordering barrier below needs a second send from it.
    const insiderApp = await seedMessages(openApp, insider, privateId, [
      "The rotation runbook is pinned above.",
    ]);

    const outsiderApi = await accounts.open(outsider.username, outsider.password);
    const ownSlug = uniqueSlug("mine");
    const ownId = await createChannelApi(outsiderApi, ownSlug);

    // Every frame the outsider's socket receives, captured from before it
    // exists — the listener is attached before the page is opened.
    const frames: string[] = [];
    app.page.on("websocket", (socket) => {
      socket.on("framereceived", (frame) => {
        frames.push(String(frame.payload));
      });
    });

    await app.gotoSignIn(`/c/${privateId}`);
    await app.signIn(outsider.username, outsider.password);
    await expect(app.identityButton).toContainText(t("chat.presence.online"));

    // Their own sidebar is intact; the private channel is simply not in it.
    await expect(app.conversationRow(ownSlug)).toBeVisible();
    await expect(app.conversationRow(privateSlug)).toHaveCount(0);
    // And the address bar bought them nothing: no conversation, no composer.
    await expect(app.page.getByText(secret)).toHaveCount(0);
    await expect(app.messageLog).toHaveCount(0);
    await expect(app.composerField).toHaveCount(0);

    // Now the same question of the socket. The insider sends into the private
    // channel first, then the outsider's own channel gets a message the
    // outsider IS entitled to: the gateway dispatches events one at a time in
    // arrival order, so once the second has been delivered the first has
    // already had its chance. Waiting on a real event rather than on a clock
    // is what makes the absence below evidence.
    await insiderApp.sendMessage("And the vault code changes with it.");
    // The outsider's own send comes from their own composer, in their own
    // channel — the one conversation on this page with a composer at all.
    await app.conversationRow(ownSlug).click();
    await app.sendMessage("A message I am allowed to read.");

    // The barrier is the FRAME the outsider is entitled to, matched on its
    // type and channel rather than on its text: their own message is
    // ciphertext on the wire now, so the words are not in it to find. A
    // `message_created` for their own channel is the same event it always
    // was, and narrowing to that type keeps the subscribe chatter their click
    // produces from satisfying the barrier before the insider's send.
    await expect
      .poll(
        () =>
          frames
            .map(parseFrame)
            .some((frame) => frame.chan === ownId && frame.type === "message_created"),
        { timeout: 30_000 },
      )
      .toBe(true);

    // The secret the server really does hold in the clear, in none of them.
    expect(frames.filter((frame) => frame.includes(secret))).toEqual([]);
    expect(frames.filter(carriesChannel(privateId)).map(typeOf)).toEqual([]);

    // The socket did answer about that channel — with the contract's
    // non-leaking refusal, which says nothing a stranger could not guess.
    expect(frames.map(parseFrame).filter((frame) => frame.type === "error")).toContainEqual(
      expect.objectContaining({ code: "channel_not_found" }),
    );
  });
});

interface ParsedFrame {
  type: string;
  chan?: string;
  code?: string;
}

/** A server frame, reduced to the three fields these assertions turn on. */
function parseFrame(raw: string): ParsedFrame {
  const frame = JSON.parse(raw) as { type?: string; chan?: string; data?: { code?: string } };
  return {
    type: frame.type ?? "",
    ...(frame.chan === undefined ? {} : { chan: frame.chan }),
    ...(frame.data?.code === undefined ? {} : { code: frame.data.code }),
  };
}

function typeOf(raw: string): string {
  return parseFrame(raw).type;
}

/**
 * Frames that carry a channel the socket has no business hearing about.
 *
 * `error` is excluded deliberately: the client asked to subscribe to the
 * channel in the URL, and the refusal echoes the id the client itself sent
 * (ws-protocol.md §3). That is the correct answer, not a leak.
 */
function carriesChannel(channelId: string): (raw: string) => boolean {
  return (raw) => {
    const frame = parseFrame(raw);
    return frame.chan === channelId && frame.type !== "error";
  };
}
