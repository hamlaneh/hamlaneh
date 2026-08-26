/**
 * The locale to hand Intl so numbers and dates come out in ASCII digits.
 *
 * The Persian UI uses `0-9` for every number the app generates — docs/design/
 * CHAT_HANDOFF.md, "Numerals", which supersedes the earlier artboards that
 * mixed the two systems. Intl asks CLDR for the language's own numbering
 * system, which for `fa` is `arabext` (Persian digits), so the `latn`
 * extension is how that decision is expressed to it.
 *
 * Its own module, with no imports, because the rule has to hold in three
 * places at once and one of them is a pure formatting module: importing
 * i18n/index.ts there would bootstrap i18next, `document` and localStorage
 * inside a unit test that formats a byte count.
 */
export function latinDigitLocale(locale: string): string {
  return locale.startsWith("fa") ? "fa-u-nu-latn" : locale;
}
