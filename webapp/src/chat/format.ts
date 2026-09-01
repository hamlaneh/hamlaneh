/**
 * Date, time and size formatting for the chat shell.
 *
 * Digit shaping: every app-generated number is set in Latin digits, including
 * in Persian. That is the designer's locked correction of 2026-08-21
 * (docs/design/CHAT_HANDOFF.md, "Numerals"), which replaced the earlier split
 * where file sizes alone followed the locale. `latinDigitLocale` is how it is
 * pinned.
 */

import { latinDigitLocale } from "../i18n/digits";

const DAY_MS = 24 * 60 * 60 * 1000;

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

/**
 * "3 March 2026" — a whole date, year always included.
 *
 * The year is not optional here, unlike on the day separator: this formats the
 * sealed date a person confirms during a restore (ADR 010, decision 3), and
 * "3 March" would be exactly as true of a backup from last year as of one from
 * this week — which is the confusion the confirmation exists to prevent.
 *
 * Empty for an unparseable value, so a missing date renders as absent rather
 * than as "Invalid Date".
 */
export function formatFullDate(iso: string, locale: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  return new Intl.DateTimeFormat(latinDigitLocale(locale), {
    day: "numeric",
    month: "long",
    year: "numeric",
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
    text: new Intl.DateTimeFormat(latinDigitLocale(locale), {
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
    return new Intl.DateTimeFormat(latinDigitLocale(locale), { weekday: "short" }).format(value);
  }
  return new Intl.DateTimeFormat(latinDigitLocale(locale), { day: "numeric", month: "short" }).format(value);
}

/**
 * The unit is a translated string, not a suffix: the Persian artboard writes
 * it out ("۲۴۸ کیلوبایت"), and where the number sits relative to the word is
 * the translator's call, so the whole pair lives in one key.
 */
const SIZE_UNIT_KEYS = [
  "chat.fileSize.b",
  "chat.fileSize.kb",
  "chat.fileSize.mb",
  "chat.fileSize.gb",
  "chat.fileSize.tb",
] as const;

export interface FileSize {
  /** The number alone, already localized. Interpolate as `value`. */
  readonly value: string;
  readonly unitKey: (typeof SIZE_UNIT_KEYS)[number];
}

/**
 * "248" + `chat.fileSize.kb` — the second line of the file and image cards.
 *
 * Latin digits in Persian, like `formatTime` and `formatCount`: the designer's
 * locked correction of 2026-08-21 (CHAT_HANDOFF.md) says the Persian UI uses
 * ASCII 0–9 for every app-generated number and names file sizes explicitly.
 */
export function formatFileSize(bytes: number, locale: string): FileSize {
  let size = Math.max(0, bytes);
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < SIZE_UNIT_KEYS.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const fractionDigits = unitIndex === 0 || size >= 100 ? 0 : 1;
  return {
    value: new Intl.NumberFormat(latinDigitLocale(locale), {
      minimumFractionDigits: 0,
      maximumFractionDigits: fractionDigits,
    }).format(size),
    unitKey: SIZE_UNIT_KEYS[unitIndex] ?? "chat.fileSize.b",
  };
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
