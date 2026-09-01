import { MAX_THUMB_BYTES, toArrayBuffer, type InlineImageType } from "./attachments";

/**
 * The ingest pipeline the server can no longer run, moved to the sender's
 * device (ADR 013).
 *
 * On an encrypted channel the server sees ciphertext, so it cannot strip a
 * photo's GPS coordinates and it cannot derive a preview. Both therefore
 * happen here, before the bytes are sealed, and both follow ADR 003's rules
 * unchanged: metadata SEGMENTS are removed and the pixels are left alone —
 * a re-encode would quietly degrade every photo anybody sends — while the
 * thumbnail, which is a derivative and not the file, is re-encoded freely.
 *
 * The strippers are ports of `server/internal/httpserver/images.go` and the
 * two halves must stay in step. Like that one, anything unparseable is
 * returned unchanged: a stripper that mangles an image is worse than one that
 * occasionally leaves a comment behind.
 */

/** The thumbnail's long edge, as on the server. */
export const THUMB_MAX_EDGE = 512;

/** The quality a thumbnail derived from a photograph is re-encoded at. */
const THUMB_JPEG_QUALITY = 0.8;

/**
 * Removes an image's metadata segments. Anything that is not one of the
 * three formats that can carry capture data comes back untouched.
 */
export function stripImageMetadata(type: InlineImageType, data: Uint8Array): Uint8Array {
  switch (type) {
    case "image/jpeg":
      return stripJPEG(data);
    case "image/png":
      return stripPNG(data);
    case "image/webp":
      return stripWebP(data);
    case "image/gif":
      // GIF carries no EXIF: its only metadata is the comment and
      // application extensions, which hold no capture data.
      return data;
  }
}

/**
 * Drops every APPn segment (APP1 is EXIF, APP2 is ICC, APP13 is IPTC) and
 * every comment, keeping the frame, the tables and the scan.
 *
 * Everything from the first scan header onwards is copied verbatim: past that
 * point the file is entropy-coded data with no segment structure to walk, and
 * guessing where the next marker starts inside it is how strippers corrupt
 * images.
 */
function stripJPEG(data: Uint8Array): Uint8Array {
  const PREFIX = 0xff;
  const SOI = 0xd8;
  const SOS = 0xda;
  const COM = 0xfe;
  const APP0 = 0xe0;
  const APP15 = 0xef;

  if (data.length < 2 || data[0] !== PREFIX || data[1] !== SOI) {
    return data;
  }
  const kept: Uint8Array[] = [data.subarray(0, 2)];

  for (let i = 2; ; ) {
    if (i + 4 > data.length || data[i] !== PREFIX) {
      return data;
    }
    const marker = data[i + 1] ?? 0;
    if (marker === SOS) {
      kept.push(data.subarray(i));
      return concat(kept);
    }
    const length = ((data[i + 2] ?? 0) << 8) | (data[i + 3] ?? 0);
    if (length < 2 || i + 2 + length > data.length) {
      return data;
    }
    const isAppN = marker >= APP0 && marker <= APP15;
    if (!isAppN && marker !== COM) {
      kept.push(data.subarray(i, i + 2 + length));
    }
    i += 2 + length;
  }
}

/**
 * The ancillary chunks that survive: the ones that decide how the pixels look
 * rather than where they came from. tRNS is why this is an allowlist and not
 * "drop every lowercase chunk" — dropping it turns transparent pixels opaque.
 */
const PNG_KEPT_ANCILLARY = new Set(["tRNS", "gAMA", "cHRM", "sRGB"]);

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];

/** Keeps the critical and rendering-relevant chunks, drops the rest. */
function stripPNG(data: Uint8Array): Uint8Array {
  if (!PNG_SIGNATURE.every((byte, index) => data[index] === byte)) {
    return data;
  }
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const kept: Uint8Array[] = [data.subarray(0, PNG_SIGNATURE.length)];

  for (let i = PNG_SIGNATURE.length; ; ) {
    // length (4) + type (4) + data + CRC (4).
    if (i + 8 > data.length) {
      return data;
    }
    const length = view.getUint32(i);
    if (i + 12 + length > data.length) {
      return data;
    }
    const chunkType = String.fromCharCode(...data.subarray(i + 4, i + 8));
    // A chunk is critical when its type's first letter is upper case.
    if (/^[A-Z]/.test(chunkType) || PNG_KEPT_ANCILLARY.has(chunkType)) {
      kept.push(data.subarray(i, i + 12 + length));
    }
    i += 12 + length;
    if (chunkType === "IEND") {
      // Whatever trails IEND is not part of the image. Dropping it is the
      // point: it is where a smuggled payload would sit.
      return concat(kept);
    }
  }
}

/** The RIFF chunks stripped from a WebP: metadata plus the colour profile. */
const WEBP_DROPPED = new Set(["EXIF", "XMP ", "ICCP"]);

/**
 * Walks the RIFF container, drops the metadata chunks, clears the VP8X flags
 * that advertised them, and rewrites the file size.
 *
 * A WebP can carry EXIF exactly as a JPEG can, so an unstripped one is the
 * same GPS leak in a different wrapper.
 */
function stripWebP(data: Uint8Array): Uint8Array {
  const HEADER_LEN = 12; // "RIFF" + size + "WEBP"
  const CHUNK_HDR = 8; // FourCC + size
  const FLAGS_TO_CLEAR = 0x20 | 0x08 | 0x04; // ICC, EXIF, XMP

  const fourCC = (offset: number): string =>
    String.fromCharCode(...data.subarray(offset, offset + 4));
  if (data.length < HEADER_LEN || fourCC(0) !== "RIFF" || fourCC(8) !== "WEBP") {
    return data;
  }
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const kept: Uint8Array[] = [data.subarray(0, HEADER_LEN)];

  for (let i = HEADER_LEN; i < data.length; ) {
    if (i + CHUNK_HDR > data.length) {
      return data;
    }
    const name = fourCC(i);
    const size = view.getUint32(i + 4, true);
    // RIFF chunks are padded to an even length; the pad byte is not counted.
    const padded = size + (size % 2);
    if (i + CHUNK_HDR + padded > data.length) {
      return data;
    }
    if (!WEBP_DROPPED.has(name)) {
      const chunk = data.slice(i, i + CHUNK_HDR + padded);
      if (name === "VP8X" && size > 0) {
        chunk[CHUNK_HDR] = (chunk[CHUNK_HDR] ?? 0) & ~FLAGS_TO_CLEAR;
      }
      kept.push(chunk);
    }
    i += CHUNK_HDR + padded;
  }

  const out = concat(kept);
  // The RIFF size field counts everything after itself.
  new DataView(out.buffer).setUint32(4, out.length - 8, true);
  return out;
}

function concat(parts: readonly Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

/* ── the thumbnail ────────────────────────────────────────────────────── */

/**
 * A photograph's preview stays a photograph; everything else keeps its alpha
 * channel, which JPEG cannot carry — the server's own rule.
 */
export function thumbnailType(source: InlineImageType): "image/jpeg" | "image/png" {
  return source === "image/jpeg" ? "image/jpeg" : "image/png";
}

/** The size the preview is drawn at: the long edge capped, aspect kept. */
export function thumbnailSize(
  width: number,
  height: number,
): { width: number; height: number } | null {
  if (width <= 0 || height <= 0) {
    return null;
  }
  const scale = THUMB_MAX_EDGE / Math.max(width, height);
  if (scale >= 1) {
    // Already small enough to be its own preview; a re-encode would cost
    // quality and a request for nothing.
    return null;
  }
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale)),
  };
}

export interface PreparedImage {
  /** The image with its metadata segments removed. */
  stripped: Uint8Array;
  /** Absent when the browser could not decode the image at all. */
  width?: number;
  height?: number;
  /** Absent only when there is no preview these bytes can carry. */
  thumbnail?: Uint8Array;
}

/**
 * Strips an image, reads its real dimensions and derives its preview.
 *
 * The stripping always happens; the rest is best-effort. A browser that
 * cannot decode these bytes — no OffscreenCanvas, or a file that sniffed as
 * an image and is not one — still sends the file, as an opaque blob with no
 * dimensions and no preview. That is the safe end: an attachment with no
 * preview works, and refusing the send would not.
 */
export async function prepareImage(
  type: InlineImageType,
  data: Uint8Array,
): Promise<PreparedImage> {
  const stripped = stripImageMetadata(type, data);
  if (typeof createImageBitmap !== "function" || typeof OffscreenCanvas !== "function") {
    return { stripped };
  }
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(new Blob([toArrayBuffer(stripped)], { type }));
  } catch {
    return { stripped };
  }
  try {
    const prepared: PreparedImage = { stripped, width: bitmap.width, height: bitmap.height };
    const size = thumbnailSize(bitmap.width, bitmap.height);
    if (size === null) {
      // Already its own preview. It is sent as the thumbnail rather than
      // re-encoded, so a small image still gets a card with a picture on it
      // and the reader never has to fetch the whole file to see one.
      // ponytail: the same bytes are sealed twice, once per AAD; a shared
      // blob would need the read path to know when one stands for both.
      if (stripped.length <= MAX_THUMB_BYTES) {
        prepared.thumbnail = stripped;
      }
      return prepared;
    }
    const canvas = new OffscreenCanvas(size.width, size.height);
    const context = canvas.getContext("2d");
    if (context === null) {
      return prepared;
    }
    context.drawImage(bitmap, 0, 0, size.width, size.height);
    const thumbType = thumbnailType(type);
    const blob = await canvas.convertToBlob({ type: thumbType, quality: THUMB_JPEG_QUALITY });
    prepared.thumbnail = new Uint8Array(await blob.arrayBuffer());
    return prepared;
  } catch {
    return { stripped };
  } finally {
    bitmap.close();
  }
}
