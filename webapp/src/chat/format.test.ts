import { describe, expect, it } from "vitest";

import {
  dayKey,
  daySeparatorLabel,
  fileTypeLabel,
  formatCount,
  formatFileSize,
  formatResultStamp,
  formatTime,
  isolateAuto,
  isolateLtr,
  linkPreviewHost,
} from "./format";

/**
 * The digit-shaping split here is the artboard's, not a default: times and
 * counts are Latin in Persian, file sizes follow the locale. It is recorded as
 * an open question to the designer, so these tests pin what is drawn today and
 * will be the thing that fails when the answer changes it.
 */

const NOW = new Date("2026-08-21T12:00:00.000Z");

/** By code point, not literal: the isolates are invisible in source. */
const LRI = String.fromCodePoint(0x2066);
const FSI = String.fromCodePoint(0x2068);
const PDI = String.fromCodePoint(0x2069);

/** Everything the string has left once the ASCII digits are removed. */
function digitsOnly(value: string): string {
  return value.replace(/\D/g, "");
}

describe("bidi isolation", () => {
  it("wraps a value in an isolate rather than adding markup", () => {
    // LRI ... PDI, so a slug stays an LTR run inside a Persian sentence.
    expect(isolateLtr("#deploys")).toBe(`${LRI}#deploys${PDI}`);
    // FSI ... PDI, so a name takes the direction of its own first letter.
    expect(isolateAuto("Parisa")).toBe(`${FSI}Parisa${PDI}`);
  });
});

describe("formatTime", () => {
  it("is 24-hour and zero-padded", () => {
    expect(formatTime("2026-08-21T09:12:00.000Z", "en")).toMatch(/^\d{2}:\d{2}$/);
  });

  it("keeps Latin digits in Persian, as the artboard draws them", () => {
    expect(formatTime("2026-08-21T09:12:00.000Z", "fa")).toMatch(/^\d{2}:\d{2}$/);
  });

  it("returns nothing for an unparseable instant instead of throwing", () => {
    expect(formatTime("not a date", "en")).toBe("");
  });
});

describe("formatCount", () => {
  it("keeps badge and member counts in Latin digits in both locales", () => {
    // `\d` is ASCII-only, so a Persian-digit rendering would strip to nothing.
    // The grouping separator is the locale's own; only the digits are asserted.
    expect(digitsOnly(formatCount(1234, "en"))).toBe("1234");
    expect(digitsOnly(formatCount(1234, "fa"))).toBe("1234");
  });
});

describe("dayKey", () => {
  it("is stable for two instants on the same local day", () => {
    const morning = dayKey("2026-08-21T00:30:00.000Z");
    const evening = dayKey("2026-08-21T00:31:00.000Z");
    expect(morning).toBe(evening);
    expect(morning).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it("returns an empty key for an unparseable instant", () => {
    expect(dayKey("nope")).toBe("");
  });
});

describe("daySeparatorLabel", () => {
  it("names today and yesterday, and dates anything older", () => {
    expect(daySeparatorLabel("2026-08-21T09:00:00.000Z", "en", NOW)).toEqual({ kind: "today" });
    expect(daySeparatorLabel("2026-08-20T09:00:00.000Z", "en", NOW)).toEqual({
      kind: "yesterday",
    });

    const older = daySeparatorLabel("2026-08-02T09:00:00.000Z", "en", NOW);
    expect(older.kind).toBe("date");
    expect(older.kind === "date" ? older.text : "").not.toBe("");
  });

  it("carries the year only when it is not the current one", () => {
    const thisYear = daySeparatorLabel("2026-02-02T09:00:00.000Z", "en", NOW);
    const lastYear = daySeparatorLabel("2025-02-02T09:00:00.000Z", "en", NOW);
    expect(thisYear.kind === "date" ? thisYear.text : "").not.toMatch(/2026/);
    expect(lastYear.kind === "date" ? lastYear.text : "").toMatch(/2025/);
  });
});

describe("formatResultStamp", () => {
  it("walks the time / weekday / date ladder the search artboard draws", () => {
    expect(formatResultStamp("2026-08-21T09:12:00.000Z", "en", NOW)).toMatch(/^\d{2}:\d{2}$/);
    // Within the week: a short weekday, never a time.
    expect(formatResultStamp("2026-08-18T09:12:00.000Z", "en", NOW)).not.toMatch(/:/);
    // Beyond it: a short date, which carries a numeral.
    expect(formatResultStamp("2026-07-02T09:12:00.000Z", "en", NOW)).toMatch(/\d/);
  });
});

describe("formatFileSize", () => {
  it("drops the fraction on bytes and on large values", () => {
    expect(formatFileSize(248, "en")).toBe("248 B");
    expect(formatFileSize(248 * 1024, "en")).toBe("248 KB");
    expect(formatFileSize(Math.round(1.2 * 1024 * 1024), "en")).toBe("1.2 MB");
  });

  it("never reports a negative size", () => {
    expect(formatFileSize(-5, "en")).toBe("0 B");
  });

  it("stops climbing at the largest unit it knows", () => {
    expect(formatFileSize(1024 ** 6, "en")).toMatch(/TB$/);
  });
});

describe("fileTypeLabel", () => {
  it("takes the subtype and upper-cases it", () => {
    expect(fileTypeLabel("application/pdf")).toBe("PDF");
    expect(fileTypeLabel("image/png")).toBe("PNG");
    // A structured suffix names the format, not the base.
    expect(fileTypeLabel("image/svg+xml")).toBe("XML");
    expect(fileTypeLabel("application/x-tar")).toBe("TAR");
    // Parameters are not part of the label.
    expect(fileTypeLabel("text/plain; charset=utf-8")).toBe("PLAIN");
  });

  it("falls back to the whole type when the subtype is not a plain token", () => {
    expect(fileTypeLabel("application")).toBe("APPLICATION");
  });
});

describe("linkPreviewHost", () => {
  it("shows the host, never the raw URL", () => {
    expect(linkPreviewHost("https://status.example.com/incidents/42?x=1")).toBe(
      "status.example.com",
    );
  });

  it("returns the input unchanged rather than throwing on a broken URL", () => {
    expect(linkPreviewHost("not a url")).toBe("not a url");
  });
});
