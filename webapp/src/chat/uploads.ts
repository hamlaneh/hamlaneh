import { api } from "../api/client";
import {
  ENCRYPTED_FILENAME,
  OPAQUE_TYPE,
  newAttachmentKey,
  rememberEntry,
  sealAttachment,
  sniffImageType,
  toArrayBuffer,
} from "../mls/attachments";
import { toBase64 } from "../mls/bytes";
import { prepareImage, type PreparedImage } from "../mls/imagePrep";
import type { Attachment } from "./types";

/**
 * One file, one request — the contract's own rule (openapi.yaml ->
 * uploadFile). The composer uploads a multi-file selection as several of
 * these so each carries its own progress and its own failure; a batch
 * endpoint would make one bad file spoil the rest.
 */

/** SendMessageRequest.attachment_ids maxItems. */
export const MAX_ATTACHMENTS = 10;

/**
 * What sealing costs: the 12-byte nonce prefix and the 16-byte GCM tag.
 *
 * `max_file_size_bytes` applies to the bytes that reach the server, which on
 * an encrypted channel are the ciphertext, so a client budgets its plaintext
 * against the cap less this (ADR 013).
 */
export const SEAL_OVERHEAD_BYTES = 28;

/** The most plaintext this instance can carry on a channel of either regime. */
export function plaintextBudget(maxFileSizeBytes: number, e2ee: boolean): number {
  return e2ee ? maxFileSizeBytes - SEAL_OVERHEAD_BYTES : maxFileSizeBytes;
}

/**
 * Why an upload was refused, in the vocabulary the composer has copy for.
 * The server's codes are collapsed to the three answers a person can act on:
 * pick a smaller file, pick a real image, or try again.
 */
export type UploadFailure = "tooLarge" | "typeMismatch" | "failed";

export type UploadResult =
  | { ok: true; attachment: Attachment }
  | { ok: false; reason: UploadFailure };

function reasonFor(status: number): UploadFailure {
  if (status === 413) {
    return "tooLarge";
  }
  if (status === 415) {
    return "typeMismatch";
  }
  // 401/403/404/429/5xx and every transport failure land here: different
  // causes, but one thing the person can do about them.
  return "failed";
}

/**
 * Uploads `file` into `channelId` and returns the stored attachment.
 *
 * On a plaintext channel the bytes go up as they are and the server does the
 * ingest. On an encrypted one none of that is possible — the server cannot
 * read what it stores — so this is where the whole pipeline runs: the
 * metadata segments come off, a preview is derived, a fresh per-file key
 * seals both, and the request says nothing about the plaintext at all. The
 * real name, type and size stay on this device until the send seals them into
 * the message (`rememberEntry`), which is what makes a file readable exactly
 * when its message is.
 */
export async function uploadAttachment(
  channelId: string,
  file: File,
  e2ee: boolean,
): Promise<UploadResult> {
  try {
    const body = e2ee ? await sealedBody(file) : plainBody(file);
    const { data, response } = await api.POST("/api/v1/channels/{channelId}/files", {
      params: { path: { channelId } },
      // The generated type calls the field a string because OpenAPI's
      // `format: binary` has no better TypeScript shape; the serializer below
      // is what actually builds the request.
      body: { file: "" },
      bodySerializer: () => body.form,
    });
    if (data === undefined) {
      return { ok: false, reason: reasonFor(response.status) };
    }
    body.remember?.(data.id);
    return { ok: true, attachment: data };
  } catch (error) {
    console.warn("Could not upload the file:", error);
    return { ok: false, reason: "failed" };
  }
}

interface UploadBody {
  form: FormData;
  /** Records what only this device knows, once the server has named the row. */
  remember?: (attachmentId: string) => void;
}

function plainBody(file: File): UploadBody {
  const form = new FormData();
  // The filename is passed explicitly rather than left to the File: the
  // contract stores it as the card's label, and it is the one part of the
  // part header nothing else can reconstruct.
  form.append("file", file, file.name);
  return { form };
}

/**
 * The encrypted body: `file` first and then, for an image, `thumb`.
 *
 * Both parts declare the placeholder name and the opaque type, which is what
 * the server refuses anything else against (`e2ee_metadata_in_clear`). The
 * refusal is deliberate and this side must never provoke it: a real filename
 * in the part header would already have left the device by the time anything
 * noticed.
 */
async function sealedBody(file: File): Promise<UploadBody> {
  const raw = new Uint8Array(await file.arrayBuffer());
  const kind = sniffImageType(raw);
  const prepared: PreparedImage = kind === "" ? { stripped: raw } : await prepareImage(kind, raw);
  const plaintext = prepared.stripped;

  const key = newAttachmentKey();
  const form = new FormData();
  form.append(
    "file",
    opaquePart(await sealAttachment(key, "original", plaintext)),
    ENCRYPTED_FILENAME,
  );
  if (prepared.thumbnail !== undefined) {
    form.append(
      "thumb",
      opaquePart(await sealAttachment(key, "thumb", prepared.thumbnail)),
      ENCRYPTED_FILENAME,
    );
  }

  const { width, height } = prepared;
  return {
    form,
    remember: (attachmentId) => {
      rememberEntry({
        id: attachmentId,
        key: toBase64(key),
        name: file.name,
        // The browser's own type, and only as the card's caption: a reader
        // decides what to render from the decrypted bytes, never from this.
        type: file.type === "" ? OPAQUE_TYPE : file.type,
        size: plaintext.length,
        ...(width === undefined ? {} : { width }),
        ...(height === undefined ? {} : { height }),
      });
    },
  };
}

const opaquePart = (sealed: Uint8Array): Blob =>
  new Blob([toArrayBuffer(sealed)], { type: OPAQUE_TYPE });
