import { fromBase64 } from "./bytes";

/**
 * Encrypted attachments (ADR 013): the per-file key, the blob format, and the
 * message envelope that carries the key to whoever can read the message.
 *
 * Everything here is frozen by the ADR's constant list — the two AAD strings,
 * the sentinel, AES-128-GCM, the 12-byte nonce prefix — and every primitive is
 * a WebCrypto native. No new dependency, and the wasm wrapper is untouched:
 * a per-file key has no lifecycle to manage, so this is content handling, not
 * key management.
 *
 * The read half of the module is written against a hostile sender. The server
 * cannot see these bytes any more, so every check ingest used to do — is this
 * really an image, is this a filename a line can hold — moves here, and the
 * adversary changes from "an uploader attacks the server" to "a sender attacks
 * the reader".
 */

/** AES-128, matching the MLS ciphersuite's own strength (ADR 013). */
export const ATTACHMENT_KEY_BYTES = 16;

/** A fresh GCM nonce, prefixed to every sealed blob. */
export const ATTACHMENT_NONCE_BYTES = 12;

/** The multipart filename an e2ee upload must declare, and nothing else. */
export const ENCRYPTED_FILENAME = "encrypted";

/** The only content type an e2ee upload may declare. */
export const OPAQUE_TYPE = "application/octet-stream";

/** The server's own bound on the client-derived thumbnail part (ADR 013). */
export const MAX_THUMB_BYTES = 1 << 20;

/**
 * What a message body starts with when it carries files. A NUL, which no
 * composed message can begin with, is what lets readers dispatch on the first
 * character and keeps plain messages exactly as they are — no flag day.
 */
export const PAYLOAD_SENTINEL = "\u0000hamlaneh-msg-v1\u0000";

/** The four types ever rendered inline, matching the server's ingest list. */
export const INLINE_IMAGE_TYPES = ["image/jpeg", "image/png", "image/gif", "image/webp"] as const;

export type InlineImageType = (typeof INLINE_IMAGE_TYPES)[number];

/** An attachment's two blobs. Each is sealed under its own AAD. */
export type BlobVariant = "original" | "thumb";

const AAD: Record<BlobVariant, string> = {
  original: "hamlaneh attachment original v1",
  thumb: "hamlaneh attachment thumb v1",
};

/** The GCM tag, counted so the blob's length can be checked before a decrypt. */
const TAG_BYTES = 16;

/**
 * One file's entry in the message envelope: the key that opens it and the
 * metadata the server was never told.
 *
 * `type` is a label and only a label. What decides how a decrypted file is
 * rendered is a sniff of the bytes themselves — see {@link sniffImageType} —
 * because this field is sender-controlled and a lying one would otherwise be
 * the whole attack.
 */
export interface AttachmentEntry {
  /** The server-assigned attachment id the key belongs to. */
  id: string;
  /** The AES-128 key, base64. */
  key: string;
  /** The real filename. Sanitized on the way in; still display data only. */
  name: string;
  /** The real content type, as a caption. Never trusted for rendering. */
  type: string;
  /** The plaintext size, for the card's meta line. */
  size: number;
  width?: number;
  height?: number;
}

/** A message body once the envelope has been read off it. */
export interface DecodedBody {
  text: string;
  attachments: AttachmentEntry[];
}

/* ── the per-file key ─────────────────────────────────────────────────── */

/** Sixteen fresh random bytes: one file's key, minted once and never rotated. */
export function newAttachmentKey(): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(ATTACHMENT_KEY_BYTES));
}

/** A copy in its own ArrayBuffer — what `crypto.subtle` and `Blob` accept. */
export function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy.buffer;
}

async function importKey(key: Uint8Array, usage: KeyUsage): Promise<CryptoKey> {
  if (key.length !== ATTACHMENT_KEY_BYTES) {
    throw new Error("an attachment key is sixteen bytes");
  }
  return crypto.subtle.importKey("raw", toArrayBuffer(key), "AES-GCM", false, [usage]);
}

const aadOf = (variant: BlobVariant): ArrayBuffer =>
  toArrayBuffer(new TextEncoder().encode(AAD[variant]));

/**
 * Seals one blob: `nonce ‖ ciphertext‖tag`, with the variant as the AAD.
 *
 * A single-use key makes nonce misuse structurally unreachable rather than
 * merely avoided — the two blobs of one attachment are the only two things
 * this key ever seals, and each gets its own nonce and its own AAD.
 */
export async function sealAttachment(
  key: Uint8Array,
  variant: BlobVariant,
  plaintext: Uint8Array,
): Promise<Uint8Array> {
  const handle = await importKey(key, "encrypt");
  const nonce = crypto.getRandomValues(new Uint8Array(ATTACHMENT_NONCE_BYTES));
  const sealed = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce, additionalData: aadOf(variant) },
    handle,
    toArrayBuffer(plaintext),
  );
  const out = new Uint8Array(nonce.length + sealed.byteLength);
  out.set(nonce);
  out.set(new Uint8Array(sealed), nonce.length);
  return out;
}

/**
 * Opens one blob, whole. Single-shot GCM authenticates everything before one
 * byte is shown, which is the point: nothing partial is ever rendered.
 *
 * The named ceiling (ADR 013): no Range requests and no streaming playback of
 * encrypted media. At the 25 MiB cap that is an acceptable memory shape; a
 * chunked construction is the upgrade if the cap ever rises materially.
 */
export async function openAttachment(
  key: Uint8Array,
  variant: BlobVariant,
  sealed: Uint8Array,
): Promise<Uint8Array> {
  if (sealed.length < ATTACHMENT_NONCE_BYTES + TAG_BYTES) {
    throw new Error("the blob is too short to be an encrypted attachment");
  }
  const handle = await importKey(key, "decrypt");
  const plaintext = await crypto.subtle.decrypt(
    {
      name: "AES-GCM",
      iv: toArrayBuffer(sealed.subarray(0, ATTACHMENT_NONCE_BYTES)),
      additionalData: aadOf(variant),
    },
    handle,
    toArrayBuffer(sealed.subarray(ATTACHMENT_NONCE_BYTES)),
  );
  return new Uint8Array(plaintext);
}

/* ── the message envelope ─────────────────────────────────────────────── */

/**
 * The plaintext a message with files is encrypted as. A message with no files
 * stays the bare string it has always been.
 */
export function encodeBody(text: string, attachments: readonly AttachmentEntry[]): string {
  if (attachments.length === 0) {
    return text;
  }
  return PAYLOAD_SENTINEL + JSON.stringify({ text, attachments });
}

/**
 * Reads a decrypted body.
 *
 * Three outcomes, and the third is why this returns null rather than throwing:
 * a body that does not claim the sentinel is its own text; one that claims it
 * and parses is the envelope; one that claims it and does NOT parse is null,
 * which the bubble renders as the honest cannot-display state. Showing the raw
 * JSON — or the sentinel — as if it were a message would be the wrong lie.
 */
export function decodeBody(raw: string): DecodedBody | null {
  if (!raw.startsWith(PAYLOAD_SENTINEL)) {
    return { text: raw, attachments: [] };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw.slice(PAYLOAD_SENTINEL.length));
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return null;
  }
  const record = parsed as Record<string, unknown>;
  const text = record.text ?? "";
  if (typeof text !== "string") {
    return null;
  }
  const list = Array.isArray(record.attachments) ? record.attachments : [];
  return {
    text,
    attachments: list.flatMap((item) => {
      const entry = readEntry(item);
      return entry === null ? [] : [entry];
    }),
  };
}

/**
 * One entry, believed only as far as it can be checked.
 *
 * An entry whose key is not a key is dropped entirely: without it the file
 * cannot be opened at all, so a card promising otherwise would be a worse
 * outcome than no card. Everything else is display data and is clamped rather
 * than dropped — a lying size is a wrong caption, not a hazard.
 */
function readEntry(item: unknown): AttachmentEntry | null {
  if (typeof item !== "object" || item === null) {
    return null;
  }
  const record = item as Record<string, unknown>;
  const { id, key, name, type, size } = record;
  if (typeof id !== "string" || id === "" || id.length > 64) {
    return null;
  }
  if (typeof key !== "string" || !isAttachmentKey(key)) {
    return null;
  }
  const width = readDimension(record.width);
  const height = readDimension(record.height);
  return {
    id,
    key,
    name: typeof name === "string" ? safeFilename(name) : "",
    // Bounded because it is rendered: a type is a short caption, and the card
    // derives its label from it (`fileTypeLabel`).
    type: typeof type === "string" ? type.slice(0, 100) : "",
    size: typeof size === "number" && Number.isFinite(size) && size > 0 ? Math.floor(size) : 0,
    ...(width === undefined ? {} : { width }),
    ...(height === undefined ? {} : { height }),
  };
}

function isAttachmentKey(value: string): boolean {
  try {
    return fromBase64(value).length === ATTACHMENT_KEY_BYTES;
  } catch {
    return false;
  }
}

/** A pixel count a real image could have, or nothing at all. */
function readDimension(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return undefined;
  }
  const pixels = Math.floor(value);
  return pixels > 0 && pixels <= 100_000 ? pixels : undefined;
}

/* ── untrusted metadata ───────────────────────────────────────────────── */

/** Characters that reorder what a filename looks like without changing it. */
const BIDI_CONTROLS = /[\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;

/** Anything that would break a name out of its one line. */
const CONTROLS = /[\p{Cc}\p{Cf}]/gu;

/**
 * A sender's filename reduced to something one line can hold.
 *
 * The server used to do this (`uploadFilename`); on an e2ee channel it never
 * sees the name, so the check moves here and gains one the server did not
 * need: bidi overrides are stripped, because "exploit<U+202E>gnp.exe" renders
 * as "exploitexe.png" and the reader would be saving something else entirely.
 *
 * Returns "" for a name there is nothing to salvage in; the caller renders its
 * own translated placeholder rather than inventing an English one here.
 */
export function safeFilename(name: string): string {
  const base = name.slice(name.search(/[^/\\]*$/u));
  const cleaned = base
    .replace(BIDI_CONTROLS, "")
    .replace(CONTROLS, " ")
    .replace(/\s+/gu, " ")
    .trim();
  if (cleaned === "" || cleaned === "." || cleaned === "..") {
    return "";
  }
  return Array.from(cleaned).slice(0, 255).join("");
}

/* ── what this session knows about a file ─────────────────────────────── */

/**
 * Every attachment entry this session has produced or read, by id.
 *
 * One table with two writers, and that is the point. An upload puts its entry
 * in before the message that will carry it exists, so the send can seal it; a
 * decrypted message puts its entries in as they are read, so an EDIT of that
 * message can re-carry them — ADR 013 requires it, because the stored
 * ciphertext is replaced whole and readers of an edited message would
 * otherwise be left holding cards with no keys.
 *
 * Session-lived on purpose. These are the file keys, and their lifetime is
 * the message's: what carries them across a reload is the same wrapped store
 * that carries the author's own message text, because the key travels inside
 * it.
 */
const knownEntries = new Map<string, AttachmentEntry>();

export function rememberEntry(entry: AttachmentEntry): void {
  knownEntries.set(entry.id, entry);
}

export function entryFor(id: string): AttachmentEntry | undefined {
  return knownEntries.get(id);
}

/** The entries this session knows for these ids, in order, skipping unknowns. */
export function entriesFor(ids: readonly string[]): AttachmentEntry[] {
  return ids.flatMap((id) => {
    const entry = knownEntries.get(id);
    return entry === undefined ? [] : [entry];
  });
}

/** Test seam. Nothing in the app forgets an entry it might still render. */
export function forgetEntries(): void {
  knownEntries.clear();
}

/* ── untrusted bytes ──────────────────────────────────────────────────── */

/**
 * Which of the four inline image types these bytes are, or "" for anything
 * else — SVG, HTML, a zip, a PDF, a lie.
 *
 * This is the whole of what decides inline rendering. The sender's declared
 * type never reaches an `<img>` or a Blob's type, so a hostile label cannot
 * make the app treat a script as a picture; and SVG is deliberately absent
 * from the list, exactly as it is from the server's.
 */
export function sniffImageType(bytes: Uint8Array): InlineImageType | "" {
  const starts = (...signature: number[]): boolean =>
    signature.length <= bytes.length && signature.every((byte, index) => bytes[index] === byte);
  const ascii = (offset: number, text: string): boolean => {
    for (let index = 0; index < text.length; index += 1) {
      if (bytes[offset + index] !== text.charCodeAt(index)) {
        return false;
      }
    }
    return true;
  };

  if (starts(0xff, 0xd8, 0xff)) {
    return "image/jpeg";
  }
  if (starts(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)) {
    return "image/png";
  }
  if (bytes.length >= 6 && ascii(0, "GIF8") && (ascii(4, "7a") || ascii(4, "9a"))) {
    return "image/gif";
  }
  if (bytes.length >= 12 && ascii(0, "RIFF") && ascii(8, "WEBP")) {
    return "image/webp";
  }
  return "";
}
