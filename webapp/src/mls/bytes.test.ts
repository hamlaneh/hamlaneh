import { describe, expect, it } from "vitest";

import { fromBase64, packFrames, toBase64, unpackFrames, unpackStrings } from "./bytes";

describe("base64", () => {
  it("round-trips arbitrary bytes, including every byte value", () => {
    const all = new Uint8Array(256);
    for (let index = 0; index < 256; index += 1) {
      all[index] = index;
    }
    expect(fromBase64(toBase64(all))).toEqual(all);
  });

  it("round-trips a payload past the argument-spread limit", () => {
    // A commit may be ~256 KiB by contract, which is exactly the size that
    // breaks String.fromCharCode(...bytes). The chunking is the point.
    const large = new Uint8Array(300_000).fill(7);
    large[299_999] = 3;
    expect(fromBase64(toBase64(large))).toEqual(large);
  });

  it("round-trips the empty blob", () => {
    expect(toBase64(new Uint8Array())).toBe("");
    expect(fromBase64("")).toEqual(new Uint8Array());
  });
});

describe("frames", () => {
  it("round-trips a list, including empty members and an empty list", () => {
    for (const items of [
      [],
      [new Uint8Array()],
      [new Uint8Array([1, 2, 3]), new Uint8Array(), new Uint8Array(300).fill(9)],
    ]) {
      expect(unpackFrames(packFrames(items))).toEqual(items);
    }
  });

  it("refuses a truncated blob rather than returning a short list", () => {
    // A half-read list of key packages would silently add fewer members than
    // the caller asked for, which is why this throws.
    const packed = packFrames([new Uint8Array([1, 2, 3])]);
    expect(() => unpackFrames(packed.slice(0, packed.length - 1))).toThrow();
    expect(() => unpackFrames(new Uint8Array())).toThrow();
  });

  it("refuses trailing bytes after the last frame", () => {
    const packed = packFrames([new Uint8Array([1])]);
    const extended = new Uint8Array(packed.length + 1);
    extended.set(packed);
    expect(() => unpackFrames(extended)).toThrow();
  });

  it("decodes text frames, Persian included", () => {
    const encoder = new TextEncoder();
    const names = ["user-a", "نسرین", "😀"];
    expect(unpackStrings(packFrames(names.map((name) => encoder.encode(name))))).toEqual(names);
  });
});
