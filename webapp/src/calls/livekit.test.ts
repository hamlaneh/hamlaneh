import { describe, expect, it } from "vitest";

import { MEDIA_KEYRING_SIZE, MlsKeyProvider, connectLiveKit, mediaKeySlot } from "./livekit";
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

/**
 * The keyring size is a wire constant, and this is what stops it from being
 * `livekit-client`'s to choose.
 *
 * The divisor used to come from the library's own defaults, which meant a
 * changed default — or ADR 009's own scheduled rise to 256 landing on one side
 * only — put two clients on different slots with nothing raised: `-1`
 * failure tolerance throws nothing, and a frozen tile reads as a peer who has
 * not caught up. Neither assertion below can hold unless the provider is
 * constructed with the number this module states.
 */
describe("the media keyring", () => {
  it("is sized by our own constant and not the library's default", () => {
    expect(new MlsKeyProvider().getOptions().keyringSize).toBe(MEDIA_KEYRING_SIZE);
  });

  it("wraps the epoch around the ring", () => {
    expect(mediaKeySlot(0)).toBe(0);
    expect(mediaKeySlot(MEDIA_KEYRING_SIZE)).toBe(0);
    expect(mediaKeySlot(MEDIA_KEYRING_SIZE + 3)).toBe(3);
  });
});
