/**
 * Avatar identity: four token fills assigned by a stable hash of the user id,
 * with initials as the no-photo fallback (chat-components -> "Avatar tints").
 * The design fixes the palette at four; do not add a fifth here.
 */

export const AVATAR_TINTS = ["brand", "warm", "info", "success"] as const;
export type AvatarTint = (typeof AVATAR_TINTS)[number];

/**
 * FNV-1a over the user id. Any stable, well-spread hash would do; the
 * requirement is only that one user always gets one tint, in every client.
 */
function hashUserId(userId: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < userId.length; index += 1) {
    hash ^= userId.charCodeAt(index);
    // 32-bit FNV prime multiply, kept in unsigned range.
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash;
}

export function avatarTint(userId: string): AvatarTint {
  const tint = AVATAR_TINTS[hashUserId(userId) % AVATAR_TINTS.length];
  // Modulo of a non-negative integer is always in range; the fallback exists
  // only to satisfy noUncheckedIndexedAccess.
  return tint ?? "brand";
}

/**
 * First grapheme of the display name, upper-cased. Persian has no case, so
 * toUpperCase is a no-op there and the letter is drawn as authored.
 * Uses Intl.Segmenter where available so a combining mark or an emoji is not
 * cut in half.
 */
export function avatarInitial(displayName: string): string {
  const trimmed = displayName.trim();
  if (trimmed === "") {
    return "?";
  }
  if (typeof Intl.Segmenter === "function") {
    const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
    const first = segmenter.segment(trimmed)[Symbol.iterator]().next();
    if (!first.done) {
      return first.value.segment.toLocaleUpperCase();
    }
  }
  // Array.from splits by code point, so a surrogate pair survives.
  return Array.from(trimmed)[0]?.toLocaleUpperCase() ?? "?";
}
