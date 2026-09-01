import { describe, expect, it } from "vitest";

import { digitsOf, groupDigits, safetyHalf, safetyNumber } from "./safetyNumber";

/**
 * The derivation, pinned.
 *
 * The vector below was computed independently of this implementation (a short
 * Python script over the same stated recipe: framed domain, id, count and
 * bytewise-sorted keys, SHA-256, big-endian integer mod 10^30). That is what
 * makes it a vector rather than a recording of whatever the code does today —
 * a refactor that changes the number fails here, which is the point: a changed
 * derivation silently invalidates every number any user has ever compared.
 */

/** `AQID` is [1,2,3]; `BAU=` is [4,5]; `AA==` is [0]. */
const ALICE = { userId: "alice", keys: ["AQID"] };
const BOB = { userId: "bob", keys: ["BAU=", "AA=="] };

const VECTOR = "02223 10856 23195 68191 98981 81738 77344 52451 44950 97148 05931 27891";

describe("the safety number derivation", () => {
  it("matches the fixed vector", async () => {
    expect(await safetyNumber(ALICE, BOB)).toBe(VECTOR);
  });

  it("is sixty ASCII digits in twelve five-digit groups", async () => {
    const number = await safetyNumber(ALICE, BOB);
    expect(number).toMatch(/^(\d{5} ){11}\d{5}$/);
    expect(number.replace(/ /g, "")).toHaveLength(60);
    // ASCII, in every locale: a number read aloud in Persian has to be the
    // same string the other person is looking at (BRIEFS, "Numerals").
    expect(number).toMatch(/^[0-9 ]+$/);
  });

  it("prints the same line whichever side computes it", async () => {
    // The halves are ordered by their own bytes, not by argument position or
    // user id — which is what lets two screens agree without agreeing first
    // on who is "local". Here Bob's half sorts first despite being second.
    expect(await safetyNumber(BOB, ALICE)).toBe(await safetyNumber(ALICE, BOB));
  });

  it("does not depend on the order the keys arrive in", async () => {
    const reordered = { userId: "bob", keys: ["AA==", "BAU="] };
    expect(await safetyNumber(ALICE, reordered)).toBe(await safetyNumber(ALICE, BOB));
  });

  it("moves when a key is added to a set", async () => {
    // The whole reason the number covers the set rather than one device: a
    // number computed over a single device is constant under exactly the
    // change it exists to expose (ADR 008, decision 2).
    const grown = { userId: "bob", keys: [...BOB.keys, "BgcI"] };
    expect(await safetyNumber(ALICE, grown)).not.toBe(await safetyNumber(ALICE, BOB));
  });

  it("moves when the same keys are claimed by a different id", async () => {
    const impostor = { userId: "bob-evil", keys: BOB.keys };
    expect(await safetyNumber(ALICE, impostor)).not.toBe(await safetyNumber(ALICE, BOB));
  });

  it("gives each half thirty zero-padded digits", async () => {
    const half = await safetyHalf(ALICE);
    expect(digitsOf(half)).toHaveLength(30);
    // A hash whose low 30 digits are small must still print thirty of them,
    // or the pair would be 59 characters and the groups would slide.
    expect(digitsOf(new Uint8Array(32))).toBe("0".repeat(30));
  });

  it("groups whatever it is given in fives", () => {
    expect(groupDigits("1234567890")).toBe("12345 67890");
  });
});
