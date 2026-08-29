/**
 * Base64 and the length-prefixed framing, the two conversions that sit on
 * every path between the contract, the wasm wrapper and IndexedDB.
 *
 * The contract carries every MLS payload as base64 (openapi.yaml, "E2EE
 * transport schemas"); the wrapper speaks `Uint8Array`; the framing is how a
 * list of byte strings crosses the wasm boundary. `webapp/src-mls/src/frames.rs`
 * is the other half of the framing and the two must stay in step.
 *
 * No library for any of it: `atob`/`btoa` are in every browser this app
 * supports and in Node, and the framing is twenty lines.
 */

/** Bytes to standard base64, as the contract's `*_b64` fields carry them. */
export function toBase64(bytes: Uint8Array): string {
  // Chunked: `String.fromCharCode(...bytes)` on a 256 KiB commit overflows the
  // argument limit, which is exactly the size the contract permits.
  const CHUNK = 0x8000;
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + CHUNK));
  }
  return btoa(binary);
}

/**
 * Base64 to bytes. Throws on input that is not base64 — a malformed blob from
 * the server is a condition the caller renders, never one it guesses past.
 */
export function fromBase64(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

/** Packs a list of byte strings: `count, (len, bytes)*`, big-endian u32. */
export function packFrames(items: readonly Uint8Array[]): Uint8Array {
  const total = 4 + items.reduce((sum, item) => sum + 4 + item.length, 0);
  const out = new Uint8Array(total);
  const view = new DataView(out.buffer);
  let offset = 0;
  view.setUint32(offset, items.length);
  offset += 4;
  for (const item of items) {
    view.setUint32(offset, item.length);
    offset += 4;
    out.set(item, offset);
    offset += item.length;
  }
  return out;
}

/** Unpacks a blob written by {@link packFrames}. Throws on truncation. */
export function unpackFrames(blob: Uint8Array): Uint8Array[] {
  const view = new DataView(blob.buffer, blob.byteOffset, blob.byteLength);
  const readUint32 = (offset: number): number => {
    if (offset + 4 > blob.length) {
      throw new Error("truncated frame length");
    }
    return view.getUint32(offset);
  };

  const count = readUint32(0);
  let offset = 4;
  const items: Uint8Array[] = [];
  for (let index = 0; index < count; index += 1) {
    const length = readUint32(offset);
    offset += 4;
    if (offset + length > blob.length) {
      throw new Error("truncated frame body");
    }
    items.push(blob.slice(offset, offset + length));
    offset += length;
  }
  if (offset !== blob.length) {
    throw new Error("trailing bytes after the last frame");
  }
  return items;
}

/** Frames holding UTF-8 text — the wrapper's member identities. */
export function unpackStrings(blob: Uint8Array): string[] {
  const decoder = new TextDecoder();
  return unpackFrames(blob).map((item) => decoder.decode(item));
}
