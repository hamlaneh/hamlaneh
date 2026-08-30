/**
 * Reading what a call's peer connections actually did.
 *
 * `getStats()` on the page's own `RTCPeerConnection` objects is the only
 * account of a call that the application cannot flatter: a spec that asked the
 * app whether media was flowing would be asking the thing under test. So the
 * connections are captured as they are constructed — before any application
 * script runs — and the numbers are read off the real objects afterwards.
 *
 * The capture is test-side instrumentation of a browser API, not a seam in the
 * product: nothing in `src/` knows this file exists.
 */
import type { BrowserContext, Page } from "@playwright/test";

declare global {
  interface Window {
    /** Every peer connection the page opened; installed by the init script. */
    __hamlanehPeerConnections?: RTCPeerConnection[];
  }
}

/**
 * Launch flags every spec that places a real call needs.
 *
 * Headless Chromium has no capture device, so a call joined without these
 * fails at getUserMedia rather than at anything the spec is about. The fake
 * device publishes a real tone, which is what makes the inbound numbers below
 * grow. `support/linux-browser.mjs` passes the same pair — it cannot import
 * this file, being plain ESM run inside the Playwright image.
 */
export const MEDIA_ARGS = ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"];

/**
 * Runs in the page before any application script: keeps every peer connection
 * the page opens, and changes nothing else about them.
 *
 * A spec that also needs to constrain the connections (relay-only ICE, say)
 * installs its own subclass instead and fills the same array — see
 * `calls-relay-only.e2e.ts`.
 */
function keepPeerConnections(): void {
  const Native = window.RTCPeerConnection;
  const seen: RTCPeerConnection[] = [];

  class RecordedPeerConnection extends Native {
    constructor(config?: RTCConfiguration) {
      super(config);
      seen.push(this);
    }
  }

  window.RTCPeerConnection = RecordedPeerConnection;
  window.__hamlanehPeerConnections = seen;
}

/** Installs {@link keepPeerConnections} on a context before its first page. */
export const recordPeerConnections = async (context: BrowserContext): Promise<void> => {
  await context.addInitScript(keepPeerConnections);
};

export interface CandidateFacts {
  type: string;
  protocol: string;
  address: string;
  port: number;
}

export interface PeerFacts {
  /** What `getConfiguration()` reports — the setting, kept beside the fact. */
  policy: string;
  connectionState: string;
  nominated: boolean;
  pairState: string;
  local: CandidateFacts | null;
  remote: CandidateFacts | null;
  audioBytesReceived: number;
  audioPacketsReceived: number;
  /**
   * `totalAudioEnergy` summed over inbound audio — the one number here that
   * only grows when samples come OUT of the decoder.
   *
   * Bytes and packets are counted at the transport, before the E2EE worker
   * ever sees a frame, so they keep growing for a receiver whose keyring
   * cannot open anything. Energy does not: a receiver with no key drops every
   * frame, the decoder is fed nothing, and concealment decays to silence
   * within a few packets. That is the difference between "media is arriving"
   * and "the call still works".
   */
  audioEnergy: number;
}

/** `getStats()` on every peer connection the page opened, reduced to facts. */
export function peerFacts(page: Page): Promise<PeerFacts[]> {
  return page.evaluate(async () => {
    const num = (value: unknown): number => (typeof value === "number" ? value : 0);
    const str = (value: unknown): string => (typeof value === "string" ? value : "");

    const facts: PeerFacts[] = [];
    for (const connection of window.__hamlanehPeerConnections ?? []) {
      const entries: Record<string, unknown>[] = [];
      (await connection.getStats()).forEach((entry: Record<string, unknown>) => {
        entries.push(entry);
      });

      const byId = new Map(entries.map((entry) => [str(entry.id), entry]));
      const pair =
        entries.find((entry) => entry.type === "candidate-pair" && entry.nominated === true) ??
        null;
      const audio = entries.filter(
        (entry) => entry.type === "inbound-rtp" && entry.kind === "audio",
      );

      const candidate = (id: unknown): CandidateFacts | null => {
        const entry = byId.get(str(id));
        return entry === undefined
          ? null
          : {
              type: str(entry.candidateType),
              protocol: str(entry.protocol),
              address: str(entry.address),
              port: num(entry.port),
            };
      };

      facts.push({
        policy: str(connection.getConfiguration().iceTransportPolicy),
        connectionState: connection.connectionState,
        nominated: pair !== null,
        pairState: pair === null ? "" : str(pair.state),
        local: pair === null ? null : candidate(pair.localCandidateId),
        remote: pair === null ? null : candidate(pair.remoteCandidateId),
        audioBytesReceived: audio.reduce((total, one) => total + num(one.bytesReceived), 0),
        audioPacketsReceived: audio.reduce((total, one) => total + num(one.packetsReceived), 0),
        audioEnergy: audio.reduce((total, one) => total + num(one.totalAudioEnergy), 0),
      });
    }
    return facts;
  });
}

/** Bytes of audio this page has received across all of its connections. */
export async function audioBytes(page: Page): Promise<number> {
  const facts = await peerFacts(page);
  return facts.reduce((total, one) => total + one.audioBytesReceived, 0);
}

/**
 * Decoded audio energy across all of this page's connections — the measure of
 * audio that came out, rather than arrived. See {@link PeerFacts.audioEnergy}.
 */
export async function audioEnergy(page: Page): Promise<number> {
  const facts = await peerFacts(page);
  return facts.reduce((total, one) => total + one.audioEnergy, 0);
}
