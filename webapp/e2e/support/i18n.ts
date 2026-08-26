/**
 * Reads the app's own locale files so specs assert on translation KEYS, not
 * on English sentences.
 *
 * Two reasons this is not pedantry:
 *   - the same spec then runs unchanged in Persian, which is what the
 *     "full suite in both locales" tier needs;
 *   - copy edits are frequent and are not regressions, while a renamed or
 *     deleted key IS one. Keying on `login.error.invalidCredentials` fails
 *     for the second and not for the first.
 */
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import type { TestOptions } from "./options";

const LOCALES_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../src/locales",
);

type Catalog = Record<string, unknown>;

const catalogs = new Map<string, Catalog>();

function catalog(locale: TestOptions["uiLocale"]): Catalog {
  const cached = catalogs.get(locale);
  if (cached !== undefined) {
    return cached;
  }
  const raw = readFileSync(path.join(LOCALES_DIR, locale, "common.json"), "utf8");
  const parsed = JSON.parse(raw) as Catalog;
  catalogs.set(locale, parsed);
  return parsed;
}

/**
 * Resolves a dotted i18n key, with i18next's `{{name}}` interpolation and
 * the `_one`/`_other` plural suffixes the catalogue uses.
 *
 * A missing key throws rather than returning the key: a spec that silently
 * matched the literal string "login.submit" would pass against a screen that
 * renders nothing at all.
 */
export function translate(
  locale: TestOptions["uiLocale"],
  key: string,
  values: Record<string, string | number> = {},
): string {
  const candidates =
    typeof values.count === "number"
      ? [`${key}_${values.count === 1 ? "one" : "other"}`, key]
      : [key];

  for (const candidate of candidates) {
    let node: unknown = catalog(locale);
    for (const segment of candidate.split(".")) {
      if (typeof node !== "object" || node === null) {
        node = undefined;
        break;
      }
      node = (node as Catalog)[segment];
    }
    if (typeof node === "string") {
      return interpolate(locale, node, values);
    }
  }
  throw new Error(`i18n key ${key} is missing from ${locale}/common.json`);
}

/**
 * Locale tag with Latin digits forced, matching `src/chat/format.ts`.
 *
 * The Persian UI uses ASCII 0-9 for every app-generated number. That is the
 * designer's locked correction of 2026-08-21 (docs/design/CHAT_HANDOFF.md,
 * "Numerals"), which explicitly supersedes the artboards that show Persian
 * digits — so do not re-derive this from an artboard. A bare
 * `Intl.NumberFormat("fa")` yields ۱۲, which is what the screen used to do
 * and no longer does.
 */
function latinDigitLocale(locale: string): string {
  return locale.startsWith("fa") ? "fa-u-nu-latn" : locale;
}

/**
 * i18next placeholders, including the formatted `{{count, number}}` form.
 * Numbers go through Intl exactly as i18next's own number formatter does, so
 * a spec asserts the string the screen actually shows.
 */
function interpolate(
  locale: TestOptions["uiLocale"],
  template: string,
  values: Record<string, string | number>,
): string {
  return template.replace(
    /\{\{\s*(\w+)\s*(?:,\s*([\w-]+)\s*)?\}\}/gu,
    (whole, name: string, format: string | undefined) => {
      const value = values[name];
      if (value === undefined) {
        return whole;
      }
      return format === "number"
        ? new Intl.NumberFormat(latinDigitLocale(locale)).format(Number(value))
        : String(value);
    },
  );
}

/** Binds `translate` to one locale for the length of a test. */
export function translator(locale: TestOptions["uiLocale"]) {
  return (key: string, values?: Record<string, string | number>): string =>
    translate(locale, key, values ?? {});
}

export type Translate = ReturnType<typeof translator>;
