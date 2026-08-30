import process from "node:process";

import type { Page } from "@playwright/test";

import { expectOk, type ApiSession } from "../support/accounts";
import {
  createChannelApi,
  inviteApi,
  removeMemberApi,
  uniqueSlug,
} from "../support/chat";
import { expect, test } from "../support/fixtures";
import { audioBytes, audioEnergy, MEDIA_ARGS, recordPeerConnections } from "../support/webrtc";

/**
 * ADR 009 step 4's last test, met as written: "an e2e two-browser encrypted
 * call over fake media devices in which a mid-call membership change rotates
 * the key and the call survives it."
 *
 * Two people are in an encrypted call. A third member of the same channel —
 * in the group, not in the call — is removed while the call is running. The
 * removal is committed by one of the two callers' own clients, which advances
 * the MLS epoch under a live call, and decision 3 says the media key follows:
 * every member derives the new exporter and fills the slot `epoch mod 16`
 * names, with no signalling of any kind. The call has to come out the other
 * side still working.
 *
 * # Why the removal is the membership change, and not an invite
 *
 * Both advance the epoch. Removal is the one decision 3 spends its argument
 * on — a fixed per-call key "concedes the removed listener" — and it is also
 * the harder path for the client, because for the seconds between the
 * directory dropping the third member and the sweep evicting their leaf, the
 * tree holds a leaf the directory attributes to nobody. That is a
 * needs-attention state, so the publish gate closes and both callers stop
 * publishing mid-call, and then the sweep's own commit clears it and they
 * publish again. Asserting that media flows afterwards therefore asserts the
 * whole of that sequence, not only the rotation.
 *
 * # What this proves, and what it does not
 *
 * It proves the integration: a membership change made through the product's
 * own API advances the epoch under a live call, both clients merge that
 * commit, and audio is still being decoded on both sides afterwards.
 *
 * It does NOT prove that the key rotated, and no end-to-end test can. Both
 * ends rotate together or not at all — LiveKit keeps the old slot filled, so
 * a client that never called `setKey` would keep sending and receiving under
 * the previous slot and this call would look identical. The rotation itself
 * is pinned where it can be: `wasm.roundtrip.test.ts` proves two devices
 * derive the same exporter at an epoch and cannot reach the old one after a
 * commit, and `useCallSession.test.ts` proves the session is rekeyed once per
 * epoch and no more. This spec is the end-to-end complement to those, not a
 * replacement for them.
 *
 * # Measuring "the call survives"
 *
 * `bytesReceived` alone would not say it. It is counted at the transport,
 * before the E2EE worker sees a frame, so it keeps growing for a receiver
 * whose keyring cannot open anything — a broken rotation would look like a
 * working call. So both sides are measured on `totalAudioEnergy` as well,
 * which only moves when samples come out of the decoder, and a sample counts
 * only if both grew (see `support/webrtc.ts`).
 *
 * # Where it runs
 *
 * Media has to reach the browser, and the e2e stack tells LiveKit to advertise
 * the compose bridge gateway (`docker-compose.e2e.yml`) — an address a Linux
 * host owns and a Windows or macOS host cannot route to. Same condition as
 * `calls-relay-only.e2e.ts`, same escape hatch: `HAMLANEH_E2E_LINUX_BROWSER`
 * puts the browser on the stack's own network.
 */

/** A Linux browser on the stack's own network; see the header. */
const LINUX_BROWSER = process.env.HAMLANEH_E2E_LINUX_BROWSER;
const CAN_RUN = process.platform === "linux" || LINUX_BROWSER !== undefined;

/** Long enough for the tone to produce packets a second sample can compare. */
const MEDIA_SAMPLE_MS = 2_500;

/**
 * How long media is allowed to take to (re)appear. Generous on purpose after
 * the membership change: the publish gate closes and reopens across it, and
 * a peer that has not merged the commit yet hears silence until it does —
 * "degraded honestly, healed automatically, seconds wide" (ADR 009).
 */
const MEDIA_SETTLE_MS = 60_000;

test.use({
  launchOptions: { args: MEDIA_ARGS },
  ...(LINUX_BROWSER === undefined ? {} : { connectOptions: { wsEndpoint: LINUX_BROWSER } }),
});

/**
 * The highest epoch the channel's commit log holds — the server's own account
 * of how far the group has advanced, read through the endpoint every client
 * catches up on.
 *
 * Unpaged: this channel is three people and a handful of commits, well inside
 * the contract's page of 50.
 */
async function committedEpoch(session: ApiSession, channelId: string): Promise<number> {
  const response = await expectOk(
    await session.context.get(`/api/v1/channels/${channelId}/mls/commits`, {
      params: { after_epoch: 0, limit: 50 },
    }),
    "the channel's MLS commit log",
  );
  const { commits } = (await response.json()) as { commits: { epoch: number }[] };
  return commits.reduce((highest, commit) => Math.max(highest, commit.epoch), 0);
}

/**
 * Decoded inbound audio over one sample window, or zero.
 *
 * Zero when either half stalled, so a caller polling this for "greater than
 * zero" is asking for audio that both arrived AND came out of the decoder.
 */
async function decodedAudio(page: Page): Promise<number> {
  const before = { bytes: await audioBytes(page), energy: await audioEnergy(page) };
  // The interval IS the measurement here, not a wait for a condition.
  await page.waitForTimeout(MEDIA_SAMPLE_MS);
  const bytes = (await audioBytes(page)) - before.bytes;
  const energy = (await audioEnergy(page)) - before.energy;
  return bytes > 0 && energy > 0 ? energy : 0;
}

async function expectAudioFlowing(page: Page, who: string, when: string): Promise<void> {
  await expect
    .poll(() => decodedAudio(page), {
      timeout: MEDIA_SETTLE_MS,
      message: `${who} is decoding no audio ${when}`,
    })
    .toBeGreaterThan(0);
}

test.describe("an encrypted call across a membership change", () => {
  test.skip(
    !CAN_RUN,
    "the browser must be able to reach the stack's bridge gateway: run on Linux, or point HAMLANEH_E2E_LINUX_BROWSER at support/linux-browser.mjs",
  );

  test("the epoch advances under a live call and media keeps flowing both ways", async ({
    accounts,
    openApp,
    t,
  }) => {
    // Three browsers, a group that has to settle across all of them, a call,
    // and two media samples. Well past the default.
    test.setTimeout(300_000);

    const alice = await accounts.createReady("e2erota");
    const bob = await accounts.createReady("e2erotb");
    // In the channel and in the group, never in the call: removing her is the
    // membership change, and her leaf is what makes it advance the epoch.
    const carol = await accounts.createReady("e2erotc");

    const aliceApi = await accounts.open(alice.username, alice.password);
    const channelId = await createChannelApi(aliceApi, uniqueSlug("rotate"), "private", {
      e2ee: true,
    });
    await inviteApi(aliceApi, channelId, bob.id);
    await inviteApi(aliceApi, channelId, carol.id);

    // Bob and Carol first, so their devices exist and have published key
    // packages before Alice's client bootstraps the group — the ordering the
    // encryption suite explains and the protocol requires.
    const bobApp = await openApp(bob, `/c/${channelId}`, recordPeerConnections);
    const carolApp = await openApp(carol, `/c/${channelId}`);
    for (const [who, app] of [
      ["bob", bobApp],
      ["carol", carolApp],
    ] as const) {
      await expect(
        app.page.getByText(t("chat.e2ee.indicator")),
        `${who}'s encrypted channel never opened`,
      ).toBeVisible({ timeout: 60_000 });
    }

    const aliceApp = await openApp(alice, `/c/${channelId}`, recordPeerConnections);
    await expect(aliceApp.page.getByText(t("chat.e2ee.indicator"))).toBeVisible({
      timeout: 60_000,
    });

    // The one honest proof that all three leaves are in the tree: a message
    // Alice seals opens on both other screens. Without it, removing Carol
    // might change nothing at all — and an epoch that never moved would make
    // everything below vacuous.
    const canary = `three of us ${uniqueSlug("in")}`;
    await aliceApp.sendMessage(canary);
    for (const [who, app] of [
      ["bob", bobApp],
      ["carol", carolApp],
    ] as const) {
      await expect(app.messageBodies, `${who} is not in the group`).toHaveText([canary], {
        timeout: 90_000,
      });
    }

    /* ── the call, and the promise it makes ───────────────────────────── */

    await aliceApp.joinCall();
    await bobApp.joinCall();

    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      await expect(
        app.callParticipants.getByRole("listitem"),
        `${who} does not see both participants`,
      ).toHaveCount(2);
      // The seam the slice surfaced for exactly this: whatever else is true,
      // this call claims to be end-to-end encrypted. Everything below is
      // about that claim surviving a commit.
      await expect(
        app.page.getByText(t("calls.encrypted.label")),
        `${who}'s call is not keyed`,
      ).toBeVisible();
    }

    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      await expectAudioFlowing(app.page, who, "before the membership change");
    }

    /* ── the membership change, mid-call ──────────────────────────────── */

    const before = await committedEpoch(aliceApi, channelId);
    expect(before, "the group never committed anything").toBeGreaterThan(0);

    // Through the endpoint the channel menu calls, by the channel's creator —
    // no seam, no back door. Every remaining member's client races to sweep
    // her leaf; all but one lose harmlessly.
    await removeMemberApi(aliceApi, channelId, carol.id);

    await expect
      .poll(() => committedEpoch(aliceApi, channelId), {
        timeout: 120_000,
        message: "the removal never produced a commit, so no epoch moved under the call",
      })
      .toBeGreaterThan(before);

    // She is out of the conversation now; leaving her page open only produces
    // requests for a channel she may no longer read.
    await carolApp.page.close();

    /* ── and the call is still a call ─────────────────────────────────── */

    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      // Still connected and still claiming encryption: a call that had
      // dropped and left the two numbers below frozen would otherwise read as
      // the same failure as a call that had gone deaf.
      await expect(
        app.callParticipants.getByRole("listitem"),
        `${who}'s call did not survive the membership change`,
      ).toHaveCount(2);
      await expect(
        app.page.getByText(t("calls.encrypted.label")),
        `${who}'s call stopped claiming to be encrypted`,
      ).toBeVisible();
    }

    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      await expectAudioFlowing(app.page, who, "after the membership change");
    }
  });
});
