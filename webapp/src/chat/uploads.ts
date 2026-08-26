import { api } from "../api/client";
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
 * `bodySerializer` is what turns the typed body into the multipart request
 * the contract describes; the generated type calls the field a string
 * because OpenAPI's `format: binary` has no better TypeScript shape, so the
 * File goes through it unchanged.
 */
export async function uploadAttachment(channelId: string, file: File): Promise<UploadResult> {
  try {
    const { data, response } = await api.POST("/api/v1/channels/{channelId}/files", {
      params: { path: { channelId } },
      body: { file: file as unknown as string },
      bodySerializer: (body: { file: unknown }) => {
        const form = new FormData();
        form.append("file", body.file as Blob);
        return form;
      },
    });
    if (data === undefined) {
      return { ok: false, reason: reasonFor(response.status) };
    }
    return { ok: true, attachment: data };
  } catch (error) {
    console.warn("Could not upload the file:", error);
    return { ok: false, reason: "failed" };
  }
}
