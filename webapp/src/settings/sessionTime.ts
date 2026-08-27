/**
 * The "Active now / Active 12 minutes ago / Last active yesterday / Last
 * active 6 Aug" ladder the `settings-sessions` artboard draws.
 *
 * The contract calls `last_active_at` approximate — it is the newest
 * generation's creation time, so its precision is the access-token lifetime —
 * which is exactly why the design phrases it in words rather than a clock
 * reading.
 *
 * Every Intl construction here goes through `latinDigitLocale`. The Persian
 * UI sets every app-generated number in ASCII digits (docs/design/
 * CHAT_HANDOFF.md, "Numerals"), and a date is a number: without the pin these
 * render `۲۹ مرداد ۱۴۰۵` beside counts and timestamps that are Latin, which
 * is the mixed-digit result the correction exists to prevent. The rule lives
 * in one module precisely so a formatter written later cannot quietly opt out
 * of it — this one did, for four screens, until 1.6.
 */

import { latinDigitLocale } from "../i18n/digits";

const MINUTE_MS = 60 * 1000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

/** "Active now", "Active {{when}}", "Last active {{when}}", or nothing usable. */
export type LastActiveLabel =
  | { kind: "now" }
  | { kind: "active"; when: string }
  | { kind: "last"; when: string }
  | { kind: "unknown" };

export function lastActiveLabel(
  iso: string,
  locale: string,
  now: Date = new Date(),
): LastActiveLabel {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return { kind: "unknown" };
  }
  // A clock-skewed future stamp is still "now" rather than a negative age.
  const elapsed = Math.max(0, now.getTime() - value.getTime());
  // "auto" is what turns -1 day into "yesterday" instead of "1 day ago".
  const relative = new Intl.RelativeTimeFormat(latinDigitLocale(locale), { numeric: "auto" });

  if (elapsed < 2 * MINUTE_MS) {
    return { kind: "now" };
  }
  if (elapsed < HOUR_MS) {
    return { kind: "active", when: relative.format(-Math.floor(elapsed / MINUTE_MS), "minute") };
  }
  if (elapsed < DAY_MS) {
    return { kind: "active", when: relative.format(-Math.floor(elapsed / HOUR_MS), "hour") };
  }
  const days = Math.floor(elapsed / DAY_MS);
  if (days < 7) {
    return { kind: "last", when: relative.format(-days, "day") };
  }
  return {
    kind: "last",
    when: new Intl.DateTimeFormat(latinDigitLocale(locale), {
      day: "numeric",
      month: "short",
      year: value.getFullYear() === now.getFullYear() ? undefined : "numeric",
    }).format(value),
  };
}

/** "21 Aug 2026" — the date beside "Turned on" in the two-step card. */
export function formatActivationDate(iso: string, locale: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  return new Intl.DateTimeFormat(latinDigitLocale(locale), {
    day: "numeric",
    month: "short",
    year: "numeric",
  }).format(value);
}

/**
 * The manual two-step key, in the groups of four the contract asks the client
 * to render ("The client renders it in groups of four") and the artboard draws
 * separated by a middle dot.
 */
export function groupSecret(secret: string): string {
  return (secret.match(/.{1,4}/gu) ?? []).join(" · ");
}
