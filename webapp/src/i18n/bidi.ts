/**
 * Bidi isolation for values interpolated into a translated sentence.
 *
 * The handoff requires channel slugs, filenames, version tags and URLs to be
 * isolated LTR runs inside Persian, and a person's name to follow its own
 * direction. Unicode isolates do exactly that without any markup, which is
 * what lets them sit inside a translated string: LRI/FSI … PDI.
 *
 * Its own module beside `digits.ts`, and for the same reason that one exists:
 * this is a shared i18n rule that unrelated screens need — chat, sign-in and
 * settings all interpolate a name into a Persian sentence — and one copy is
 * what stops three of them drifting apart. Nothing here imports anything, so a
 * unit test that formats one string does not bootstrap i18next.
 */

/** Left-to-right isolate: the run reads LTR whatever surrounds it. */
export function isolateLtr(value: string): string {
  return `⁦${value}⁩`;
}

/** First-strong isolate: the run takes the direction of its own first letter. */
export function isolateAuto(value: string): string {
  return `⁨${value}⁩`;
}
