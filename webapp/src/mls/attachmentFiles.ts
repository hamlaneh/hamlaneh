import {
  OPAQUE_TYPE,
  openAttachment,
  sniffImageType,
  toArrayBuffer,
  type BlobVariant,
} from "./attachments";
import { fromBase64 } from "./bytes";

/**
 * Reading an encrypted attachment: fetch the ciphertext, open it, and hand
 * the plaintext to the DOM without ever handing the DOM a sender's claim.
 *
 * This module exists so that one rule has one implementation. A `blob:` URL
 * inherits the app origin, so navigating to sender-controlled HTML from one
 * would hand the sender the origin the whole file-serving design exists to
 * protect. The rule ADR 013 states — never navigate to a decrypted blob URL —
 * is enforced here structurally rather than by discipline: **every** object
 * URL this app mints from decrypted bytes is minted by {@link objectUrl},
 * whose Blob type is the SNIFFED type or `application/octet-stream`, never a
 * string the sender chose. A blob URL typed `image/png` renders a picture if
 * anything ever navigates to it; one typed `application/octet-stream`
 * downloads. Neither runs script, and there is no third case.
 *
 * The thumbnail cap. Both blobs of an attachment come from this instance's
 * own store and are bounded by its own caps, so the cap here is a belt on a
 * body that is already bounded — cheap, and the reason nothing streams into
 * memory unbounded if that ever stops being true.
 */

/** What a decrypted blob became, and how to let go of it. */
export interface DecryptedBlob {
  url: string;
  /** One of the four proven image types, or "" for anything opaque. */
  imageType: string;
  revoke: () => void;
}

/**
 * The single mint. The type comes from the bytes; the sender's `type` field
 * never reaches it.
 */
export function objectUrl(bytes: Uint8Array): DecryptedBlob {
  const imageType = sniffImageType(bytes);
  const blob = new Blob([toArrayBuffer(bytes)], {
    type: imageType === "" ? OPAQUE_TYPE : imageType,
  });
  const url = URL.createObjectURL(blob);
  return {
    url,
    imageType,
    revoke: () => {
      URL.revokeObjectURL(url);
    },
  };
}

/**
 * Fetches one blob of an attachment and opens it.
 *
 * The URL is origin-relative and stays that way (ADR 013's resolved note):
 * the fetch is same-origin, `connect-src 'self'` already admits it, and CORS
 * is never consulted. Throws on anything at all — an expired signature, a
 * deleted file, bytes that do not authenticate — because every one of those is
 * the same sentence on the card.
 */
export async function fetchDecrypted(
  url: string,
  key: string,
  variant: BlobVariant,
  maxBytes: number,
  signal?: AbortSignal,
): Promise<Uint8Array> {
  const response = await fetch(url, { signal: signal ?? null });
  if (!response.ok) {
    throw new Error(`the file could not be fetched (${String(response.status)})`);
  }
  const declared = Number(response.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > maxBytes) {
    throw new Error("the file is larger than this instance stores");
  }
  const sealed = new Uint8Array(await response.arrayBuffer());
  if (sealed.length > maxBytes) {
    throw new Error("the file is larger than this instance stores");
  }
  return openAttachment(fromBase64(key), variant, sealed);
}

/**
 * Hands the reader the decrypted file as a save.
 *
 * A download rather than a navigation, and the difference is not stylistic:
 * `download` on a same-origin blob URL is what makes the browser write the
 * bytes to disk instead of rendering them, and the Blob's own type keeps that
 * true even if something ever managed to open the URL directly. The URL is
 * revoked on the next frame — immediately after `click()` is too early for
 * some browsers, and keeping it would leave a live handle on the plaintext.
 */
export function saveDecrypted(bytes: Uint8Array, filename: string): void {
  const blob = objectUrl(bytes);
  const anchor = document.createElement("a");
  anchor.href = blob.url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(blob.revoke, 0);
}
