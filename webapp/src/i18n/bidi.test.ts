import { describe, expect, it } from "vitest";

import { isolateAuto, isolateLtr } from "./bidi";

/** By code point, not literal: the isolates are invisible in source. */
const LRI = String.fromCodePoint(0x2066);
const FSI = String.fromCodePoint(0x2068);
const PDI = String.fromCodePoint(0x2069);

describe("bidi isolation", () => {
  it("wraps a value in an isolate rather than adding markup", () => {
    // LRI … PDI, so a slug stays an LTR run inside a Persian sentence.
    expect(isolateLtr("#deploys")).toBe(`${LRI}#deploys${PDI}`);
    // FSI … PDI, so a name takes the direction of its own first letter.
    expect(isolateAuto("Parisa")).toBe(`${FSI}Parisa${PDI}`);
  });
});
