/**
 * Date, time and size formatting for the chat shell.
 *
 * Digit shaping follows the Persian artboard (chat-rtl-fa), which sets times,
 * member counts and badge counts in Latin digits and file sizes in Persian
 * digits. That split is the design's, not a default: `formatTime`/`formatCount`
 * pin the `latn` numbering system for `fa`, `formatFileSize` uses the locale's
 * own. See the handoff note in the slice report.
 */

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * Bidi isolation for values interpolated into a sentence.
 *
 * The handoff requires channel slugs, filenames, version tags and URLs to be
 * isolated LTR runs inside Persian, and a person's name to follow its own
 * direction. Unicode isolates do exactly that without any markup, which is
 * what lets them sit inside a translated string: LRI/FSI ... PDI.
 */
export function isolateLtr(value: string): string {
  return `⁦${value}⁩`;
}

/** First-strong isolate: the run takes the direction of its own first letter. */
export function isolateAuto(value: string): string {
  return `⁨${value}⁩`;
}

/** Locale tag with Latin digits forced, for the values the artboard draws that way. */
function latinDigitLocale(locale: string): string {
  return locale.startsWith("fa") ? "fa-u-nu-latn" : locale;
}

/** "09:12" — 24-hour, zero-padded, as drawn on every artboard. */
export function formatTime(iso: string, locale: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  return new Intl.DateTimeFormat(latinDigitLocale(locale), {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(value);
}

/** Sidebar badges and member counts — Latin digits per the Persian artboard. */
export function formatCount(count: number, locale: string): string {
  return new Intl.NumberFormat(latinDigitLocale(locale)).format(count);
}

/** Calendar-day key in local time; two messages share a day separator iff these match. */
export function dayKey(iso: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${String(value.getFullYear())}-${month}-${day}`;
}

/** Whole days between two instants, counted on calendar-day boundaries. */
function daysAgo(value: Date, now: Date): number {
  const startOfValue = new Date(value.getFullYear(), value.getMonth(), value.getDate());
  const startOfNow = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  return Math.round((startOfNow.getTime() - startOfValue.getTime()) / DAY_MS);
}

export type DaySeparatorLabel =
  | { kind: "today" }
  | { kind: "yesterday" }
  | { kind: "date"; text: string };

/**
 * The day separator's label. "Today" and "Yesterday" are drawn on the
 * artboards; anything older falls back to the locale's own long date, which
 * the design does not draw.
 */
export function daySeparatorLabel(
  iso: string,
  locale: string,
  now: Date = new Date(),
): DaySeparatorLabel {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return { kind: "date", text: "" };
  }
  const distance = daysAgo(value, now);
  if (distance <= 0) {
    return { kind: "today" };
  }
  if (distance === 1) {
    return { kind: "yesterday" };
  }
  return {
    kind: "date",
    text: new Intl.DateTimeFormat(locale, {
      day: "numeric",
      month: "long",
      year: value.getFullYear() === now.getFullYear() ? undefined : "numeric",
    }).format(value),
  };
}

/**
 * Search results are stamped with a time today, a short weekday within the
 * week, and a short date beyond it — the "09:12 / Mon / Fri" ladder on
 * chat-search-results.
 */
export function formatResultStamp(iso: string, locale: string, now: Date = new Date()): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  const distance = daysAgo(value, now);
  if (distance <= 0) {
    return formatTime(iso, locale);
  }
  if (distance < 7) {
    return new Intl.DateTimeFormat(locale, { weekday: "short" }).format(value);
  }
  return new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(value);
}

const SIZE_UNITS = ["B", "KB", "MB", "GB", "TB"] as const;

/** "248 KB", "1.2 MB" — the second line of the file and image cards. */
export function formatFileSize(bytes: number, locale: string): string {
  let size = Math.max(0, bytes);
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < SIZE_UNITS.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const fractionDigits = unitIndex === 0 || size >= 100 ? 0 : 1;
  const formatted = new Intl.NumberFormat(locale, {
    minimumFractionDigits: 0,
    maximumFractionDigits: fractionDigits,
  }).format(size);
  return `${formatted} ${SIZE_UNITS[unitIndex] ?? "B"}`;
}

/**
 * The card's short type label: "PDF" from application/pdf, "PNG" from
 * image/png. Falls back to the whole type when the subtype is not a plain
 * extension-like token.
 */
export function fileTypeLabel(contentType: string): string {
  const subtype = contentType.split(";")[0]?.split("/")[1] ?? "";
  const token = subtype.split("+").pop() ?? "";
  const cleaned = token.replace(/^x-/, "");
  if (cleaned === "" || !/^[a-z0-9.-]+$/i.test(cleaned)) {
    return contentType.toUpperCase();
  }
  return cleaned.toUpperCase();
}

/** The host line under a link preview — never the raw URL, never a broken parse. */
export function linkPreviewHost(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
