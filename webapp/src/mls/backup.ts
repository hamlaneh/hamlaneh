/**
 * The recovery key and the sealed envelope (ADR 010, decision 4).
 *
 * Everything a backup is made of lives here and nowhere else: the generated
 * recovery key and its encoding, the KDF that turns it into a backup key, and
 * the seal/unseal pair. The service above decides *when* to call them; this
 * file decides nothing and is therefore the only place the format is written
 * down.
 *
 * The whole crypto surface is `crypto.subtle` natives — HKDF-SHA-256,
 * AES-256-GCM, and `importKey` with `extractable: false`. No dependency
 * enters the client for any of it, and nothing here is hand-rolled: the one
 * thing this file assembles by hand is a byte layout, and the layout is
 * length-prefixed precisely so it cannot be ambiguous.
 *
 * ## What the format fixes, and why each part is load-bearing
 *
 * `header ‖ iv ‖ ciphertext`, where the header is the magic `HMLB`, a version
 * byte, and the repo's length-prefixed framing of the counter and the user id.
 * **The header is the AAD, verbatim** — so a server that edits any of it
 * produces a blob that will not open rather than one that opens differently.
 * That is what makes the counter inside it trustworthy after a successful
 * decrypt, and the counter is the whole anti-rollback story: the client
 * compares THIS number against a floor it keeps itself, never the copy the
 * server mirrors back in JSON, which a hostile server writes.
 *
 * The recovery key is 256 bits of `crypto.getRandomValues`, so the KDF is
 * plain HKDF with no stretching: against full entropy there is no brute force
 * to slow down, only domain separation to get right. A user-chosen passphrase
 * would need argon2id and therefore a wasm dependency — machinery bought to
 * make a weaker root available (ADR 010, decision 2, refused).
 *
 * The checksum exists so a typo fails here, in the browser, before any network
 * call: mistyping a character must not look like a server that has no backup.
 * It is four characters over the first two bytes of a domain-separated hash —
 * enough to catch a slip, not a claim of integrity, which is the GCM tag's.
 */

import { packFrames, unpackFrames } from "./bytes";
import { parseVerificationRecords } from "./keystore";
import type { VerificationRecords } from "./types";

/** The envelope's magic. Four bytes, so a wrong blob fails before decryption. */
const MAGIC = "HMLB";

/** Format version. Bumped only by a change that makes old blobs unreadable. */
const FORMAT_VERSION = 1;

/** AES-GCM wants 12 fresh bytes per seal; never reused, never derived. */
const IV_BYTES = 12;

/** 256 bits from the CSPRNG. The root of everything in this file. */
const RECOVERY_KEY_BYTES = 32;

/**
 * Crockford base32: no I, L, O or U. The excluded letters are exactly the
 * ones a person mistypes or mishears, which is the entire reason to prefer it
 * over RFC 4648 for something read off a screen and typed on another device.
 */
const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/** 32 bytes at 5 bits a character, rounded up. */
const KEY_CHARS = Math.ceil((RECOVERY_KEY_BYTES * 8) / 5);

/** Two checksum bytes at 5 bits a character, rounded up. */
const CHECKSUM_BYTES = 2;
const CHECKSUM_CHARS = Math.ceil((CHECKSUM_BYTES * 8) / 5);

/** Groups of four, as the ADR displays it. */
const GROUP_SIZE = 4;

/**
 * The two domain strings, and they carry the version rather than a separate
 * field: a future ciphersuite change writes new labels instead of pretending
 * old material is comparable to new.
 */
const CHECKSUM_DOMAIN = "hamlaneh recovery key v1";
const SEAL_INFO = "hamlaneh backup seal v1";

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/**
 * What a backup carries: this user's trust decisions, and nothing that is
 * reconstructible (ADR 010, decision 1).
 *
 * Named sections rather than a bare record map, because the own-message
 * history slice adds a section key here rather than a second backup system.
 * `createdAt` is sealed, which is what lets the restore screen show a date the
 * server cannot forge forward.
 */
export interface BackupPayload {
  v: 1;
  /** ISO-8601, sealed inside the envelope. */
  createdAt: string;
  verificationRecords: VerificationRecords;
}

/** A successfully opened envelope. The counter here is the SEALED one. */
export interface OpenedBackup {
  counter: number;
  userId: string;
  payload: BackupPayload;
}

/* ── the recovery key ──────────────────────────────────────────────────── */

/** Left-aligned base32 over a byte string, padding the final group with zeros. */
function toBase32(bytes: Uint8Array, chars: number): string {
  let out = "";
  let buffer = 0;
  let bits = 0;
  for (const byte of bytes) {
    buffer = (buffer << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      out += ALPHABET.charAt((buffer >> bits) & 31);
    }
  }
  if (bits > 0) {
    out += ALPHABET.charAt((buffer << (5 - bits)) & 31);
  }
  return out.slice(0, chars);
}

/**
 * The inverse, over already-normalized characters. Returns null on a character
 * outside the alphabet — the caller has already mapped Crockford's confusable
 * letters, so anything left really is not base32.
 *
 * Trailing padding bits must be zero, which is not pedantry: 52 characters
 * carry 260 bits for 256 of key, so the final character holds one data bit and
 * four of padding — and a decoder that ignored them would accept sixteen
 * different last characters for the same key, letting half the typos in that
 * position through the checksum unnoticed. The encoder only ever writes zeros,
 * so a correct transcription always passes.
 */
function fromBase32(text: string, bytes: number): Uint8Array | null {
  const out = new Uint8Array(bytes);
  let buffer = 0;
  let bits = 0;
  let index = 0;
  for (const character of text) {
    const value = ALPHABET.indexOf(character);
    if (value < 0) {
      return null;
    }
    buffer = (buffer << 5) | value;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      if (index < bytes) {
        out[index] = (buffer >> bits) & 0xff;
        index += 1;
      }
    }
  }
  if (index !== bytes) {
    return null;
  }
  return (buffer & ((1 << bits) - 1)) === 0 ? out : null;
}

async function checksumOf(key: Uint8Array): Promise<Uint8Array> {
  const domain = encoder.encode(CHECKSUM_DOMAIN);
  const preimage = new Uint8Array(domain.length + key.length);
  preimage.set(domain, 0);
  preimage.set(key, domain.length);
  const digest = await crypto.subtle.digest("SHA-256", preimage);
  return new Uint8Array(digest).slice(0, CHECKSUM_BYTES);
}

/** Groups of four, hyphen-separated — how it is shown and how it is read back. */
function group(text: string): string {
  const groups: string[] = [];
  for (let at = 0; at < text.length; at += GROUP_SIZE) {
    groups.push(text.slice(at, at + GROUP_SIZE));
  }
  return groups.join("-");
}

/**
 * A fresh recovery key: 32 bytes of CSPRNG, rendered for a human.
 *
 * Returned as both the string and the bytes, because the caller needs the
 * bytes to derive with and the string to show — and re-parsing what we just
 * printed would be a slower way to get back what we already had, with a
 * failure mode nobody would ever hit and everybody would have to handle.
 */
export async function generateRecoveryKey(): Promise<{ text: string; key: Uint8Array }> {
  const key = crypto.getRandomValues(new Uint8Array(RECOVERY_KEY_BYTES));
  return { text: await formatRecoveryKey(key), key };
}

/** The displayed form: 52 characters plus a four-character checksum. */
export async function formatRecoveryKey(key: Uint8Array): Promise<string> {
  const checksum = await checksumOf(key);
  return group(toBase32(key, KEY_CHARS) + toBase32(checksum, CHECKSUM_CHARS));
}

/**
 * Bytes back out of what somebody typed, or null when it is not a recovery
 * key this device would ever have printed.
 *
 * Case, spacing and hyphens are all forgiven, and Crockford's confusable
 * letters are mapped the way he specifies (O→0, I and L→1), because a person
 * copying 56 characters off another screen will produce all of those and none
 * of them is a mistake. What is NOT forgiven is a wrong character, and that is
 * the point: the checksum refuses it here rather than letting the request
 * reach the server and come back as an ambiguous failure.
 */
export async function parseRecoveryKey(text: string): Promise<Uint8Array | null> {
  const normalized = text
    .toUpperCase()
    .replace(/[\s-]/gu, "")
    .replace(/O/gu, "0")
    .replace(/[IL]/gu, "1");
  if (normalized.length !== KEY_CHARS + CHECKSUM_CHARS) {
    return null;
  }
  const key = fromBase32(normalized.slice(0, KEY_CHARS), RECOVERY_KEY_BYTES);
  const presented = fromBase32(normalized.slice(KEY_CHARS), CHECKSUM_BYTES);
  if (key === null || presented === null) {
    return null;
  }
  const expected = await checksumOf(key);
  // Not constant-time, and it does not need to be: this compares a checksum
  // of a value the person typed against one derived from the same value, in
  // their own browser. There is no secret here an attacker is not already
  // holding if they can observe it.
  if (expected.some((byte, index) => byte !== presented[index])) {
    return null;
  }
  return key;
}

/* ── the backup key ────────────────────────────────────────────────────── */

/**
 * HKDF-SHA-256 from the recovery key to the AES-GCM key that seals the
 * envelope. Salt empty, info the versioned label — ADR 010, decision 4,
 * exactly.
 *
 * Imported non-extractable, so the derived key can be used through this handle
 * and never read out of it. That is the same honest ceiling the wrapping key
 * carries (see `keystore.ts`): it resists a copied database, not code running
 * as this origin.
 */
export async function deriveBackupKey(recoveryKey: Uint8Array): Promise<CryptoKey> {
  const material = await crypto.subtle.importKey("raw", asBuffer(recoveryKey), "HKDF", false, [
    "deriveKey",
  ]);
  return crypto.subtle.deriveKey(
    {
      name: "HKDF",
      hash: "SHA-256",
      salt: new Uint8Array(0),
      info: encoder.encode(SEAL_INFO),
    },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}

/* ── the envelope ──────────────────────────────────────────────────────── */

/**
 * The authenticated header: magic, version, and the length-prefixed framing of
 * the counter and the user id.
 *
 * Framed rather than concatenated, which is ADR 008's lesson applied one file
 * over: with plain concatenation a counter's bytes could run into a user id's
 * and two different (counter, user) pairs could produce one AAD. Framing makes
 * that impossible rather than unlikely.
 */
function backupHeader(counter: number, userId: string): Uint8Array {
  const counterBytes = new Uint8Array(8);
  new DataView(counterBytes.buffer).setBigUint64(0, BigInt(counter));
  const framed = packFrames([counterBytes, encoder.encode(userId)]);
  const header = new Uint8Array(MAGIC.length + 1 + framed.length);
  header.set(encoder.encode(MAGIC), 0);
  header[MAGIC.length] = FORMAT_VERSION;
  header.set(framed, MAGIC.length + 1);
  return header;
}

/** A copy in a plain ArrayBuffer, which is what `crypto.subtle` accepts. */
function asBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy.buffer;
}

/** Seals one payload at `counter` for `userId`. */
export async function sealBackup(
  key: CryptoKey,
  userId: string,
  counter: number,
  payload: BackupPayload,
): Promise<Uint8Array> {
  const header = backupHeader(counter, userId);
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const ciphertext = new Uint8Array(
    await crypto.subtle.encrypt(
      { name: "AES-GCM", iv, additionalData: asBuffer(header) },
      key,
      encoder.encode(JSON.stringify(payload)),
    ),
  );
  const envelope = new Uint8Array(header.length + iv.length + ciphertext.length);
  envelope.set(header, 0);
  envelope.set(iv, header.length);
  envelope.set(ciphertext, header.length + iv.length);
  return envelope;
}

/**
 * The header's length, read out of the envelope itself, or null when the bytes
 * are not one of ours.
 *
 * The length has to be derived rather than assumed because the user id is
 * variable-length. It is bounded on the way: a length prefix from a hostile
 * server is a number this code would otherwise allocate against.
 */
const MAX_USER_ID_BYTES = 128;

function headerLength(envelope: Uint8Array): number | null {
  const prefix = MAGIC.length + 1;
  if (envelope.length < prefix) {
    return null;
  }
  if (decoder.decode(envelope.subarray(0, MAGIC.length)) !== MAGIC) {
    return null;
  }
  if (envelope[MAGIC.length] !== FORMAT_VERSION) {
    return null;
  }
  // The framing is fixed at two items — the counter and the user id — so the
  // only unknown is the second one's length.
  if (envelope.length < prefix + 20) {
    return null;
  }
  const view = new DataView(envelope.buffer, envelope.byteOffset, envelope.byteLength);
  if (view.getUint32(prefix) !== 2 || view.getUint32(prefix + 4) !== 8) {
    return null;
  }
  const userIdLength = view.getUint32(prefix + 16);
  if (userIdLength > MAX_USER_ID_BYTES) {
    return null;
  }
  const total = prefix + 20 + userIdLength;
  return envelope.length >= total + IV_BYTES ? total : null;
}

/**
 * Opens an envelope, or throws.
 *
 * Every failure is one thing to the caller — "this did not open" — and that is
 * correct rather than lazy: a wrong recovery key, a truncated blob and a
 * tampered header are indistinguishable by design, because the GCM tag is the
 * only check there is and there deliberately is no key-check value stored
 * anywhere for a server to run an offline attack against.
 *
 * The counter it returns is the SEALED one, taken from the AAD the tag just
 * covered — never the copy the server mirrors back beside the envelope.
 */
export async function unsealBackup(key: CryptoKey, envelope: Uint8Array): Promise<OpenedBackup> {
  const length = headerLength(envelope);
  if (length === null) {
    throw new Error("this is not a Hamlaneh backup envelope");
  }
  const header = envelope.subarray(0, length);
  const iv = envelope.subarray(length, length + IV_BYTES);
  const ciphertext = envelope.subarray(length + IV_BYTES);

  const plaintext = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: asBuffer(iv), additionalData: asBuffer(header) },
    key,
    asBuffer(ciphertext),
  );

  // Read AFTER the decrypt, so nothing here ever acts on an unauthenticated
  // header. Reading it earlier would work and would be the habit that
  // eventually reads something that matters.
  const [counterBytes, userIdBytes] = unpackFrames(header.subarray(MAGIC.length + 1));
  if (counterBytes === undefined || userIdBytes === undefined) {
    throw new Error("the backup header is malformed");
  }
  const counter = Number(
    new DataView(asBuffer(counterBytes)).getBigUint64(0),
  );
  return {
    counter,
    userId: decoder.decode(userIdBytes),
    payload: readPayload(decoder.decode(new Uint8Array(plaintext))),
  };
}

/**
 * Parses a decrypted payload, dropping anything malformed.
 *
 * Validated even though the tag already proved nobody else wrote it, for the
 * reason `keystore.ts` validates its own stored records: what comes back
 * decides whether a `verified` badge is drawn, so a truncated or half-written
 * blob must degrade to "no record" — which re-pins and warns — rather than to
 * a level nobody ever recorded. Records go through the same parser the
 * keystore uses, so the two can never drift.
 */
function readPayload(json: string): BackupPayload {
  const parsed: unknown = JSON.parse(json);
  if (typeof parsed !== "object" || parsed === null) {
    throw new Error("the backup payload is not an object");
  }
  const body = parsed as { createdAt?: unknown; verificationRecords?: unknown };
  return {
    v: 1,
    createdAt: typeof body.createdAt === "string" ? body.createdAt : "",
    verificationRecords:
      typeof body.verificationRecords === "object" && body.verificationRecords !== null
        ? parseVerificationRecords(JSON.stringify(body.verificationRecords))
        : {},
  };
}
