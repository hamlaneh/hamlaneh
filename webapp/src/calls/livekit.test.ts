import { describe, expect, it } from "vitest";

import { connectLiveKit } from "./livekit";
import { CallEncryptionUnsupportedError } from "./media";

/**
 * The one thing worth asking the real media client under jsdom.
 *
 * jsdom has no insertable streams, so it *is* the unsupported browser gate 2
 * of ADR 009 exists for — which makes this the honest test of it rather than a
 * simulation: `isE2EESupported()` genuinely returns false here, and the
 * refusal has to come before any room, any worker, or any use of the ticket.
 *
 * Everything else about this module needs real WebRTC and is exercised through
 * `MediaConnect` doubles instead (see `media.ts`).
 */
describe("connectLiveKit", () => {
  it("refuses a keyed call in a browser that cannot encrypt media", async () => {
    await expect(
      connectLiveKit("ws://localhost:3000", "a-ticket", {
        epoch: 1,
        secret: new Uint8Array(32),
      }),
    ).rejects.toBeInstanceOf(CallEncryptionUnsupportedError);
  });
});
