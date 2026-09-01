import { describe, expect, it } from "vitest";

import {
  deriveBackupKey,
  formatRecoveryKey,
  generateRecoveryKey,
  parseRecoveryKey,
  sealBackup,
  unsealBackup,
} from "./backup";
import type { BackupPayload } from "./backup";
import { unpackFrames } from "./bytes";

/**
 * The format, against fixed vectors (ADR 010, decision 4, and ADR 008's vector
 * discipline).
 *
 * What these pin is the layout itself, not that the code agrees with itself: a
 * round trip alone would pass with any encoding at all, and the whole reason
 * the ADR fixes the envelope is so that a client written next year opens a
 * blob sealed today.
 */

const ME = "6a5f7dbe-0000-4000-8000-00000000ffff";

/** The all-zero-through-31 key. Its rendering is the vector below. */
const FIXED_KEY = new Uint8Array(Array.from({ length: 32 }, (_, index) => index));

/**
 * The recovery key for {@link FIXED_KEY}: 52 Crockford characters plus a
 * four-character checksum, in groups of four.
 */
const FIXED_TEXT = "000G-40R4-0M30-E209-185G-R38E-1W81-24GK-2GAH-C5RR-34D1-P70X-3RFG-1810";

const payload: BackupPayload = {
  v: 1,
  createdAt: "2026-03-03T09:15:00.000Z",
  verificationRecords: {
    "11111111-0000-4000-8000-000000000001": {
      userId: "11111111-0000-4000-8000-000000000001",
      keys: ["a2V5LW9uZQ==", "a2V5LXR3bw=="],
      level: "verified",
      at: 1_772_000_000_000,
    },
  },
};

describe("the recovery key", () => {
  it("renders a fixed key as the ADR's fixed string", async () => {
    await expect(formatRecoveryKey(FIXED_KEY)).resolves.toBe(FIXED_TEXT);
  });

  it("reads back exactly what it printed", async () => {
    await expect(parseRecoveryKey(FIXED_TEXT)).resolves.toEqual(FIXED_KEY);
  });

  it("forgives case, spacing and Crockford's confusable letters", async () => {
    // A person copying 56 characters off another screen produces all of
    // these, and none of them is a mistake. `0` typed as `O` and `1` typed as
    // `I` or `L` are the two Crockford names explicitly.
    const typed = FIXED_TEXT.toLowerCase().replaceAll("-", " ").replaceAll("0", "O");
    await expect(parseRecoveryKey(typed)).resolves.toEqual(FIXED_KEY);
  });

  it("refuses a typo, and refuses it here rather than on the wire", async () => {
    // One character wrong in the key half — the commonest possible slip, and
    // the reason the checksum exists. Nothing in this path touches the
    // network: `parseRecoveryKey` is pure, and the service calls it before it
    // calls the server (see backup.service.test.ts, which pins that ordering).
    const typo = FIXED_TEXT.replace("000G", "000H");
    await expect(parseRecoveryKey(typo)).resolves.toBeNull();

    // A wrong checksum with a correct key half is refused too — otherwise the
    // checksum would be decoration.
    await expect(parseRecoveryKey(FIXED_TEXT.slice(0, -1) + "2")).resolves.toBeNull();

    for (const malformed of ["", "not a key", FIXED_TEXT.slice(0, -5), `${FIXED_TEXT}0`]) {
      await expect(parseRecoveryKey(malformed)).resolves.toBeNull();
    }
  });

  it("generates 256 bits that round-trip through their own rendering", async () => {
    const { text, key } = await generateRecoveryKey();
    expect(key).toHaveLength(32);
    // 52 + 4 characters in 14 groups of four.
    expect(text.replaceAll("-", "")).toHaveLength(56);
    await expect(parseRecoveryKey(text)).resolves.toEqual(key);
  });
});

describe("the envelope", () => {
  it("seals and unseals a payload, carrying the counter in the header", async () => {
    const key = await deriveBackupKey(FIXED_KEY);
    const envelope = await sealBackup(key, ME, 42, payload);

    const opened = await unsealBackup(key, envelope);
    expect(opened.counter).toBe(42);
    expect(opened.userId).toBe(ME);
    expect(opened.payload).toEqual(payload);
  });

  it("lays the header out exactly as the ADR fixes it", async () => {
    const key = await deriveBackupKey(FIXED_KEY);
    const envelope = await sealBackup(key, ME, 42, payload);

    expect(new TextDecoder().decode(envelope.subarray(0, 4))).toBe("HMLB");
    expect(envelope[4]).toBe(1);

    // `packFrames(counter as 8-byte big-endian, user id UTF-8)` — framed, so
    // no two (counter, user) pairs can share a preimage.
    const framedLength = 4 + 4 + 8 + 4 + new TextEncoder().encode(ME).length;
    const frames = unpackFrames(envelope.subarray(5, 5 + framedLength));
    expect(frames).toHaveLength(2);
    expect([...(frames[0] ?? [])]).toEqual([0, 0, 0, 0, 0, 0, 0, 42]);
    expect(new TextDecoder().decode(frames[1])).toBe(ME);

    // Then twelve fresh IV bytes, then the ciphertext.
    expect(envelope.length).toBeGreaterThan(5 + framedLength + 12);
  });

  it("uses a fresh iv for every seal", async () => {
    const key = await deriveBackupKey(FIXED_KEY);
    const first = await sealBackup(key, ME, 1, payload);
    const second = await sealBackup(key, ME, 1, payload);
    expect(first).not.toEqual(second);
  });

  it("derives the same key from the same recovery key, across a re-parse", async () => {
    // The path a real restore takes: print, type, parse, derive. If any of the
    // three drifted, this is where it shows.
    const sealed = await sealBackup(await deriveBackupKey(FIXED_KEY), ME, 7, payload);
    const typed = await parseRecoveryKey(FIXED_TEXT);
    if (typed === null) {
      throw new Error("the fixed vector did not parse back");
    }
    const opened = await unsealBackup(await deriveBackupKey(typed), sealed);
    expect(opened.payload).toEqual(payload);
  });

  it("fails cleanly under a different recovery key", async () => {
    const sealed = await sealBackup(await deriveBackupKey(FIXED_KEY), ME, 3, payload);
    const other = await deriveBackupKey(new Uint8Array(32).fill(9));
    // A rejection, not a wrong answer and not a hang: the GCM tag is the only
    // check there is, and it is the right one.
    await expect(unsealBackup(other, sealed)).rejects.toThrow();
  });

  it("refuses an envelope whose header was edited", async () => {
    const key = await deriveBackupKey(FIXED_KEY);
    const sealed = await sealBackup(key, ME, 5, payload);

    // The counter lives in the header and the header IS the AAD, so raising it
    // breaks the tag rather than producing a blob that opens at a higher
    // number. That is the whole reason a decrypted counter can be trusted.
    const tampered = sealed.slice();
    tampered[5 + 4 + 4 + 7] = 99;
    await expect(unsealBackup(key, tampered)).rejects.toThrow();

    // And the same for the magic and the version byte, which fail before any
    // decryption is attempted.
    const wrongMagic = sealed.slice();
    wrongMagic[0] = "X".charCodeAt(0);
    await expect(unsealBackup(key, wrongMagic)).rejects.toThrow();

    const wrongVersion = sealed.slice();
    wrongVersion[4] = 2;
    await expect(unsealBackup(key, wrongVersion)).rejects.toThrow();
  });

  it("drops a malformed record rather than inventing a level", async () => {
    const key = await deriveBackupKey(FIXED_KEY);
    const sealed = await sealBackup(key, ME, 1, {
      v: 1,
      createdAt: "2026-03-03T09:15:00.000Z",
      // A record whose level is not one of the two. It must vanish, so the
      // person is re-pinned and warned rather than shown a badge nobody
      // recorded.
      verificationRecords: {
        bad: { userId: "bad", keys: ["k"], level: "trusted", at: 1 },
      } as unknown as BackupPayload["verificationRecords"],
    });
    const opened = await unsealBackup(key, sealed);
    expect(opened.payload.verificationRecords).toEqual({});
  });
});
