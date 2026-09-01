import { fromBase64, packFrames } from "./bytes";

/**
 * The safety number two people read to each other (ADR 008, decision 4).
 *
 * Per person: `half = SHA-256(domain ‖ user id ‖ key count ‖ the signature
 * public keys, sorted bytewise)`. Each half renders as 30 decimal digits — the
 * hash as a big-endian integer mod 10^30, zero-padded — and the pair renders as
 * 60 digits in twelve five-digit groups, the halves ordered by their own bytes
 * so both screens print the identical line.
 *
 * Digits rather than words: the product is bilingual, no standardized Persian
 * wordlist exists, and digits read aloud in either language. They are ASCII in
 * both locales on purpose — a number read aloud in Persian has to be the same
 * string the other person is looking at.
 *
 * Taking decimal digits from a hash is **encoding, not cryptography**: the
 * strength is the 256-bit hash, and the 30 digits (~99.7 bits) are what a human
 * can actually read out. The domain string carries the version, which is what
 * lets a future ciphersuite change this derivation without anyone pretending
 * old numbers are comparable to new ones.
 *
 * ## Why there is exactly one of these functions
 *
 * Both halves of every comparison run through {@link safetyHalf}. The security
 * of the ceremony is not in the hash — it is in *whose knowledge feeds which
 * half* — and that rule lives at the call site, in `service.ts`. A second
 * derivation written elsewhere would be a second place for the rule to drift
 * away from, so the call sites pass key sets in and this file never asks where
 * a set came from.
 */

/** Versioned, and the version is the point — see the module note. */
const DOMAIN = "hamlaneh-safety-number-v1";

/** Digits per half. Two halves make the sixty a human reads. */
const HALF_DIGITS = 30;

/** Digits per printed group. Twelve groups of five is the whole number. */
const GROUP_SIZE = 5;

const MODULUS = 10n ** BigInt(HALF_DIGITS);

const encoder = new TextEncoder();

/** One person, and the device signature keys attributed to them. */
export interface KeySet {
  userId: string;
  /** Signature public keys, base64 — as the directory and records carry them. */
  keys: readonly string[];
}

/** Lexicographic byte order: the sort the derivation and the pairing both use. */
function compareBytes(a: Uint8Array, b: Uint8Array): number {
  const shared = Math.min(a.length, b.length);
  for (let index = 0; index < shared; index += 1) {
    const left = a[index] ?? 0;
    const right = b[index] ?? 0;
    if (left !== right) {
      return left - right;
    }
  }
  return a.length - b.length;
}

function uint32(value: number): Uint8Array {
  const bytes = new Uint8Array(4);
  new DataView(bytes.buffer).setUint32(0, value);
  return bytes;
}

/**
 * This person's half of the number, as the raw 32 hash bytes.
 *
 * The inputs are framed with {@link packFrames} rather than concatenated
 * plainly: every field is length-prefixed, so no two different (id, key set)
 * pairs can produce the same preimage by running one field's bytes into the
 * next. A hash whose inputs are ambiguous is a hash two different people can
 * be made to share.
 */
export async function safetyHalf(person: KeySet): Promise<Uint8Array> {
  const keys = person.keys.map(fromBase64).sort(compareBytes);
  const preimage = packFrames([
    encoder.encode(DOMAIN),
    encoder.encode(person.userId),
    uint32(keys.length),
    ...keys,
  ]);
  const digest = await crypto.subtle.digest("SHA-256", preimage as BufferSource);
  return new Uint8Array(digest);
}

/** A half's 30 digits: the hash as a big-endian integer, mod 10^30. */
export function digitsOf(half: Uint8Array): string {
  let value = 0n;
  for (const byte of half) {
    value = (value << 8n) | BigInt(byte);
  }
  return (value % MODULUS).toString().padStart(HALF_DIGITS, "0");
}

/** Sixty digits in twelve five-digit groups, separated by single spaces. */
export function groupDigits(digits: string): string {
  const groups: string[] = [];
  for (let at = 0; at < digits.length; at += GROUP_SIZE) {
    groups.push(digits.slice(at, at + GROUP_SIZE));
  }
  return groups.join(" ");
}

/**
 * The number both people compare.
 *
 * The halves are ordered by their own bytes rather than by user id or by who
 * is "local", which is what makes the two screens print the same line: each
 * client computes one half from its own knowledge and one from the other
 * person's claimed set, and neither knows which of the two it will print first
 * until both are hashed.
 */
export async function safetyNumber(a: KeySet, b: KeySet): Promise<string> {
  const [first, second] = await Promise.all([safetyHalf(a), safetyHalf(b)]);
  const ordered = compareBytes(first, second) <= 0 ? [first, second] : [second, first];
  return groupDigits(ordered.map(digitsOf).join(""));
}
