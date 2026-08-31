import dgram from "node:dgram";
import { randomBytes } from "node:crypto";
import process from "node:process";

import type { BrowserContext } from "@playwright/test";

import { createChannelApi, inviteApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import { readStackState } from "../support/stack";
import { audioBytes, MEDIA_ARGS, peerFacts } from "../support/webrtc";

/**
 * ROADMAP §2 gate 2: a client forced to relay-only ICE completes a call
 * against the compose stack.
 *
 * Two signed-in people in one channel join the same call with both browsers
 * gathering ONLY relay candidates, and the run asserts three separate things,
 * because any two of them can be true while the call is worthless:
 *
 *   1. the peer connections reach connected and nominate a pair;
 *   2. the nominated pair's LOCAL candidate really is of type `relay`, on the
 *      address the media server advertises;
 *   3. `inbound-rtp` audio grows on both sides between two samples — media
 *      moving, not a handshake that merely looks green.
 *
 * # Why the policy is forced in the constructor
 *
 * `livekit-client` takes an ICE policy as an option, and trusting it would
 * make point 2 a restatement of the setting rather than a measurement. So the
 * page's own `RTCPeerConnection` is replaced before any application script
 * runs (`forceRelayPolicy`), and `setConfiguration` is wrapped as well — the
 * media client re-applies its configuration when the server hands it ICE
 * servers, and an unwrapped setter would quietly undo the policy mid-call.
 * Drill 001 names the constructor shim as one of the two pieces worth keeping.
 *
 * # The control probe — the other piece worth keeping
 *
 * A relay test that passes because the relay port happened to be reachable is
 * worse than no test, so the run measures, while media is flowing, that the
 * nominated relay port is NOT reachable from outside the media server's
 * network namespace. The measurement is a STUN Binding request, sent twice:
 *
 *   - to the node IP on 3478, which IS published: it must come back ANSWERED.
 *     That leg is what proves the runner has a real path to the node IP at
 *     all — without it, silence from an unroutable address would read as
 *     evidence about the relay range.
 *   - to the node IP on the relay port the browser actually nominated, inside
 *     LiveKit's 30000-40000 range and published nowhere: it must come back
 *     REFUSED.
 *
 * Refused, not merely unanswered, and the difference is the point. Measured
 * against this stack: the published media mux on 7882 is reachable and stays
 * silent, because it declines a Binding request carrying no ICE credentials.
 * So "no answer" is a claim the mux would satisfy too. ICMP port-unreachable —
 * ECONNREFUSED on a connected UDP socket — is the outcome that means nothing
 * is bound there at all, and it is what every port in the relay range returns.
 *
 * Both legs run against the live stack while the call is up, which is what
 * makes the pair meaningful rather than a pair of unrelated facts.
 *
 * # What the harness changed, and why it does not invalidate the result
 *
 * `webapp/e2e/docker-compose.e2e.yml` overrides three LiveKit config lines and
 * nothing else — `use_external_ip: false`, an explicit `node_ip` of the pinned
 * bridge gateway, and `turn.allow_restricted_peer_cidrs` for that subnet. The
 * file states the reason for each; the short version is that the shipped
 * default advertises a publicly discovered address, and a test machine has
 * none it owns. The published port list, the hardening, the UDP mux, embedded
 * TURN and auto-create are all inherited untouched, and the control probe
 * above re-establishes on every run that the relay range is still unreachable.
 *
 * # Where it runs
 *
 * The browser must be able to reach the bridge gateway, which is true on Linux
 * — CI, where this gate lives — and false on a Windows or macOS host, whose
 * Docker network lives inside a VM. Elsewhere the spec skips with the reason
 * stated rather than passing weakly; `HAMLANEH_E2E_LINUX_BROWSER` (see
 * support/linux-browser.mjs) puts the browser on the stack's own network and
 * lets it run there too, minus the probe, which the runner cannot make.
 */

/** A Linux browser on the stack's own network; see the header. */
const LINUX_BROWSER = process.env.HAMLANEH_E2E_LINUX_BROWSER;
const RUNNER_IS_DOCKER_HOST = process.platform === "linux";
const CAN_RUN = RUNNER_IS_DOCKER_HOST || LINUX_BROWSER !== undefined;

/** LiveKit's own relay allocation range — drill 001 §2, and its startup log. */
const RELAY_RANGE = { start: 30_000, end: 40_000 };
/** ADR 005's three published ports; none of them may be a relay port. */
const PUBLISHED_PORTS = [3478, 7881, 7882];
/** The published TURN port, and the probe's positive control. */
const TURN_PORT = 3478;

/** Long enough for the tone to produce packets a second sample can compare. */
const MEDIA_SAMPLE_MS = 3_000;

test.use({
  launchOptions: { args: MEDIA_ARGS },
  ...(LINUX_BROWSER === undefined ? {} : { connectOptions: { wsEndpoint: LINUX_BROWSER } }),
});

/* ── the constructor shim ─────────────────────────────────────────────── */

/**
 * Runs in the page before any application script. Forces relay-only ICE on
 * every peer connection the page opens, and keeps them so the test can read
 * `getStats()` off the real objects rather than off anything the app reports.
 */
function forceRelayPolicy(): void {
  const Native = window.RTCPeerConnection;
  const seen: RTCPeerConnection[] = [];
  const relayOnly = (config?: RTCConfiguration): RTCConfiguration => ({
    ...(config ?? {}),
    iceTransportPolicy: "relay",
  });

  class RelayOnlyPeerConnection extends Native {
    constructor(config?: RTCConfiguration) {
      super(relayOnly(config));
      seen.push(this);
    }
  }

  // The media client re-applies its configuration once the server has handed
  // it ICE servers. Unwrapped, that call would drop the policy mid-negotiation
  // and the evidence below would be measuring the default.
  //
  // Detaching the method is the point of the wrapper, and it is re-invoked
  // with `.call(this, ...)` two lines down, so the rule's hazard is the thing
  // being done deliberately.
  // eslint-disable-next-line @typescript-eslint/unbound-method
  const nativeSetConfiguration = Native.prototype.setConfiguration;
  Native.prototype.setConfiguration = function setConfiguration(
    this: RTCPeerConnection,
    config?: RTCConfiguration,
  ): void {
    nativeSetConfiguration.call(this, relayOnly(config));
  };

  window.RTCPeerConnection = RelayOnlyPeerConnection;
  window.__hamlanehPeerConnections = seen;
}

const installRelayShim = async (context: BrowserContext): Promise<void> => {
  await context.addInitScript(forceRelayPolicy);
};

/* ── the control probe ────────────────────────────────────────────────── */

/**
 * What a UDP port did with a STUN Binding request.
 *
 * The distinction between the middle two is the whole strength of the negative
 * leg, and it is measured rather than assumed — against this stack, on the
 * node IP:
 *
 *   answered     3478, the published TURN port: it is a STUN server too.
 *   silent       7882, the published media mux: reachable, something is bound,
 *                and it declines to answer a request carrying no ICE
 *                credentials. Reachable-but-quiet is a real outcome.
 *   refused      7881 (published for TCP, nothing bound on UDP) and every port
 *                in the relay range: ICMP port-unreachable comes back, which a
 *                connected UDP socket surfaces as ECONNREFUSED. Nothing is
 *                there.
 *
 * So "the relay port did not answer" would be a weak claim — the mux does not
 * answer either. "The relay port is refused" is the strong one, and it is what
 * the test asserts.
 */
type PortOutcome = "answered" | "silent" | "refused" | "unreachable";

/** RFC 5389 Binding request: type, zero length, magic cookie, transaction id. */
function bindingRequest(): Buffer {
  const message = Buffer.alloc(20);
  message.writeUInt16BE(0x0001, 0);
  message.writeUInt16BE(0, 2);
  message.writeUInt32BE(0x2112a442, 4);
  randomBytes(12).copy(message, 8);
  return message;
}

/**
 * Sends a STUN Binding request and classifies what came back.
 *
 * Three datagrams, spaced, because a single lost one must not be read as
 * evidence of absence.
 */
function probePort(host: string, port: number, timeoutMs = 2_500): Promise<PortOutcome> {
  return new Promise((resolve) => {
    const socket = dgram.createSocket("udp4");
    const timers: ReturnType<typeof setTimeout>[] = [];
    let settled = false;

    const finish = (outcome: PortOutcome): void => {
      if (settled) {
        return;
      }
      settled = true;
      for (const timer of timers) {
        clearTimeout(timer);
      }
      try {
        socket.close(() => {
          resolve(outcome);
        });
      } catch {
        // Already closing after its own error; the outcome is unchanged.
        resolve(outcome);
      }
    };

    socket.on("message", () => {
      finish("answered");
    });
    socket.on("error", (error: NodeJS.ErrnoException) => {
      // ECONNREFUSED is the ICMP port-unreachable, and the one that carries
      // information. Anything else is this runner having no route at all,
      // which must not be allowed to read as "nothing is listening there".
      finish(error.code === "ECONNREFUSED" ? "refused" : "unreachable");
    });
    socket.connect(port, host, () => {
      for (const delay of [0, 400, 900]) {
        timers.push(
          setTimeout(() => {
            if (!settled) {
              socket.send(bindingRequest(), () => undefined);
            }
          }, delay),
        );
      }
      timers.push(
        setTimeout(() => {
          finish("silent");
        }, timeoutMs),
      );
    });
  });
}

test.describe("relay-only calls", () => {
  test.skip(
    !CAN_RUN,
    "the browser must be able to reach the stack's bridge gateway: run on Linux, or point HAMLANEH_E2E_LINUX_BROWSER at support/linux-browser.mjs",
  );

  test("two clients forced to relay-only ICE exchange media through the embedded TURN", async ({
    accounts,
    openApp,
    t,
  }) => {
    // Two browsers, a call, and a media sample taken after it settles: well
    // past the default, and not something to shave.
    test.setTimeout(180_000);

    const stack = readStackState();

    const alice = await accounts.createReady("e2erelaya");
    const bob = await accounts.createReady("e2erelayb");
    const aliceApi = await accounts.open(alice.username, alice.password);
    const channelId = await createChannelApi(aliceApi, uniqueSlug("relay"));
    await inviteApi(aliceApi, channelId, bob.id);

    // Bob's browser FIRST, and not for convenience. Every conversation is
    // encrypted now (ADR 011), and a device that has never opened the app has
    // published no key package for the creator's client to claim: open Alice
    // first and she bootstraps a group Bob cannot be added to, he sits at
    // "waiting to join", and his call is refused rather than downgraded — the
    // product behaving exactly as designed, against a spec that had raced it.
    // The encryption suite explains the ordering; the protocol requires it.
    const bobApp = await openApp(bob, `/c/${channelId}`, installRelayShim);
    const aliceApp = await openApp(alice, `/c/${channelId}`, installRelayShim);

    await aliceApp.joinCall();
    await bobApp.joinCall();

    // Both clients agree two people are in the room. This is the application's
    // own account of the call; everything below is the transport's.
    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      await expect(
        app.page.getByRole("list", { name: t("calls.view.participants") }).getByRole("listitem"),
        `${who} does not see both participants`,
      ).toHaveCount(2);
    }

    /* ── 1 & 2: connected, over a genuinely relayed pair ──────────────── */

    // What the run measured, printed on success as well as failure: drill 001
    // is worth reading because of its numbers, and a gate that reports only
    // "passed" cannot be told from a gate that ran nothing.
    const evidence: string[] = [];
    const relayPorts = new Set<number>();
    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      const facts = await peerFacts(app.page);
      expect(facts.length, `${who} opened no peer connection at all`).toBeGreaterThan(0);

      const connected = facts.filter((one) => one.connectionState === "connected");
      expect(connected.length, `${who} has no connected peer connection`).toBeGreaterThan(0);

      for (const one of connected) {
        expect(one.policy, `${who}: the peer connection is not relay-only`).toBe("relay");
        expect(one.nominated, `${who}: no candidate pair was nominated`).toBe(true);
        expect(one.pairState, `${who}: the nominated pair did not succeed`).toBe("succeeded");

        const local = one.local;
        expect(local, `${who}: the nominated pair has no local candidate`).not.toBeNull();
        if (local === null) {
          continue;
        }
        // The measurement the whole spec exists for: not "we asked for relay"
        // but "the pair carrying this call has a relay candidate on our side".
        expect(local.type, `${who}: the nominated local candidate is not a relay`).toBe("relay");
        expect(local.protocol, `${who}: the relay candidate is not over UDP`).toBe("udp");
        expect(local.address, `${who}: the relay is not on the advertised node IP`).toBe(
          stack.livekit.nodeIP,
        );
        expect(
          local.port,
          `${who}: the relay port ${String(local.port)} is outside LiveKit's relay range`,
        ).toBeGreaterThanOrEqual(RELAY_RANGE.start);
        expect(local.port, `${who}: the relay port is outside LiveKit's relay range`).toBeLessThan(
          RELAY_RANGE.end,
        );
        expect(
          PUBLISHED_PORTS,
          `${who}: the relay landed on a published port, so it proves nothing`,
        ).not.toContain(local.port);
        relayPorts.add(local.port);
        evidence.push(
          `${who}: ${one.connectionState}/${one.pairState} nominated local=${local.type}/${local.protocol} ${local.address}:${String(local.port)} remote=${one.remote?.type ?? "?"}/${one.remote?.protocol ?? "?"} ${one.remote?.address ?? "?"}:${String(one.remote?.port ?? 0)}`,
        );
      }
    }

    /* ── 3: media actually moves, on both sides ───────────────────────── */

    for (const [who, app] of [
      ["alice", aliceApp],
      ["bob", bobApp],
    ] as const) {
      await expect
        .poll(() => audioBytes(app.page), {
          timeout: 30_000,
          message: `${who} received no audio at all`,
        })
        .toBeGreaterThan(0);
    }

    const before = {
      alice: await audioBytes(aliceApp.page),
      bob: await audioBytes(bobApp.page),
    };
    // The interval IS the measurement here, not a wait for a condition.
    await aliceApp.page.waitForTimeout(MEDIA_SAMPLE_MS);
    const after = {
      alice: await audioBytes(aliceApp.page),
      bob: await audioBytes(bobApp.page),
    };

    expect(after.alice, "alice's inbound audio stopped growing").toBeGreaterThan(before.alice);
    expect(after.bob, "bob's inbound audio stopped growing").toBeGreaterThan(before.bob);

    evidence.push(
      `inbound audio over ${String(MEDIA_SAMPLE_MS)}ms: alice ${String(before.alice)} -> ${String(after.alice)} B, bob ${String(before.bob)} -> ${String(after.bob)} B`,
    );

    /* ── the control probe, while the call is still up ────────────────── */

    if (!RUNNER_IS_DOCKER_HOST) {
      console.log(["relay-only:", ...evidence].join("\n  "));
      test.info().annotations.push({
        type: "warning",
        description:
          "control probe skipped: this runner has no route to the bridge gateway, so it cannot tell an unreachable relay port from an unreachable network. CI runs on Linux, where the probe always runs.",
      });
      return;
    }

    const turn = await probePort(stack.livekit.nodeIP, TURN_PORT);
    evidence.push(
      `control probe: ${stack.livekit.nodeIP}:${String(TURN_PORT)} (published TURN) ${turn}`,
    );
    expect(
      turn,
      `the probe's positive control failed: ${stack.livekit.nodeIP}:${String(TURN_PORT)} is published and should answer STUN, but the probe saw "${turn}" — so its negative results below would prove nothing`,
    ).toBe("answered");

    expect(relayPorts.size, "no relay port was observed to probe").toBeGreaterThan(0);
    for (const port of relayPorts) {
      const relay = await probePort(stack.livekit.nodeIP, port);
      evidence.push(
        `control probe: ${stack.livekit.nodeIP}:${String(port)} (nominated relay, unpublished) ${relay}`,
      );
      // Not "did not answer" — the published media mux does not answer either.
      // Refused is ICMP port-unreachable: nothing is there at all, which is
      // the claim ADR 005 makes about the relay range.
      expect(
        relay,
        `the nominated relay port ${stack.livekit.nodeIP}:${String(port)} came back "${relay}" rather than refused: something is reachable there from outside the media server's namespace, so this call may have worked for a reason the shipped stack does not provide`,
      ).toBe("refused");
    }
    console.log(["relay-only:", ...evidence].join("\n  "));
  });
});
