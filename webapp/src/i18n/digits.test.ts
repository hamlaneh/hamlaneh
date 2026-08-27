import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import { latinDigitLocale } from "./digits";

/**
 * The Persian UI sets every app-generated number in ASCII digits — docs/design/
 * CHAT_HANDOFF.md, "Numerals". `latinDigitLocale` is how that reaches Intl,
 * and the rule lives in one module so the copies cannot disagree.
 *
 * Having one module is not enough on its own, and this project has the
 * evidence twice over: `chat/format.ts` defined the helper and still had three
 * date formatters that skipped it, and `settings/sessionTime.ts` was written
 * later and never called it at all — so four screens rendered `۲۹ مرداد ۱۴۰۵`
 * beside Latin counts, and no test noticed for two phases.
 *
 * A formatter opting out is invisible in review and invisible at runtime to
 * anyone reading English. So the source is the thing under test: every Intl
 * constructor that can emit a digit has to be handed a locale that went
 * through the rule.
 */
const SRC = path.join(__dirname, "..");

/** Intl constructors whose output can contain a digit. */
const NUMERIC_INTL = ["NumberFormat", "DateTimeFormat", "RelativeTimeFormat"];

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      return sourceFiles(full);
    }
    if (!/\.tsx?$/.test(entry.name) || /\.test\.tsx?$/.test(entry.name)) {
      return [];
    }
    return [full];
  });
}

/** Every `new Intl.X(<first arg>` in app source, as file:line plus that argument. */
function numericIntlCallSites(): { where: string; locale: string }[] {
  const sites: { where: string; locale: string }[] = [];
  for (const file of sourceFiles(SRC)) {
    const lines = readFileSync(file, "utf8").split("\n");
    lines.forEach((line, index) => {
      for (const ctor of NUMERIC_INTL) {
        const at = line.indexOf(`new Intl.${ctor}(`);
        if (at === -1) {
          continue;
        }
        const rest = line.slice(at + `new Intl.${ctor}(`.length);
        const end = rest.search(/[,)]/);
        sites.push({
          where: `${path.relative(SRC, file)}:${String(index + 1)}`,
          locale: (end === -1 ? rest : rest.slice(0, end)).trim(),
        });
      }
    });
  }
  return sites;
}

describe("ASCII digits in the Persian UI", () => {
  it("pins fa to the latn numbering system, and leaves other locales alone", () => {
    expect(latinDigitLocale("fa")).toBe("fa-u-nu-latn");
    expect(latinDigitLocale("en")).toBe("en");

    // Guards the runtime's own ICU data as much as the helper: if `fa` ever
    // stopped defaulting to Persian digits, every assertion built on this
    // would go quietly vacuous.
    expect(new Intl.NumberFormat("fa").format(26)).toMatch(/^[۰-۹]+$/u);
    expect(new Intl.NumberFormat(latinDigitLocale("fa")).format(26)).toMatch(/^[0-9]+$/u);
  });

  it("routes every numeric Intl constructor through the rule", () => {
    const offenders = numericIntlCallSites().filter(
      (site) =>
        // A literal locale is already a decision, and cannot be `fa` by
        // accident; anything derived from the active language must be pinned.
        !site.locale.startsWith("latinDigitLocale(") && !site.locale.startsWith('"'),
    );
    expect(offenders).toEqual([]);
  });

  it("finds call sites to check in the first place", () => {
    // Without this, a moved directory would empty the list above and the test
    // would pass by having nothing to say.
    expect(numericIntlCallSites().length).toBeGreaterThan(0);
  });
});
