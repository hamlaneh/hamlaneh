/**
 * "in 6 days" / "in 14 hours" — how the invites table states an expiry, and
 * which of those the design flags as near.
 *
 * The list arrives ordered by expiry, soonest first; the artboard colours the
 * nearest one warning. "Near" is under a day, because that is the window in
 * which "send this to somebody" stops being a safe assumption.
 */

const MINUTE_MS = 60 * 1000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

export interface ExpiryLabel {
  text: string;
  /** True under a day out, or already past — the flagged row. */
  near: boolean;
}

export function expiryLabel(iso: string, locale: string, now: Date = new Date()): ExpiryLabel {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return { text: "", near: false };
  }
  const remaining = value.getTime() - now.getTime();
  // "auto" turns +1 day into "tomorrow" rather than "in 1 day".
  const relative = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });

  if (remaining <= 0) {
    return { text: relative.format(0, "minute"), near: true };
  }
  if (remaining < HOUR_MS) {
    return { text: relative.format(Math.ceil(remaining / MINUTE_MS), "minute"), near: true };
  }
  if (remaining < DAY_MS) {
    return { text: relative.format(Math.ceil(remaining / HOUR_MS), "hour"), near: true };
  }
  return { text: relative.format(Math.ceil(remaining / DAY_MS), "day"), near: false };
}
