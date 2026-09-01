import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { fileTypeLabel, formatCount, formatFileSize } from "../../chat/format";
import type { Attachment } from "../../chat/types";
import { SEAL_OVERHEAD_BYTES } from "../../chat/uploads";
import { isolateLtr } from "../../i18n/bidi";
import { fetchDecrypted, objectUrl, saveDecrypted } from "../../mls/attachmentFiles";
import { MAX_THUMB_BYTES, safeFilename, type AttachmentEntry } from "../../mls/attachments";
import { useInstance } from "../../instance/instanceInfo";
import { DownloadIcon, FileTextIcon, ImageIcon } from "../icons";

/**
 * The cards for a file in an encrypted conversation (ADR 013).
 *
 * UNDESIGNED STATES — no artboard draws an encrypted file, so these are
 * assembled from delivered parts only: the `hm-card` and `hm-image-card`
 * shells exactly as the plaintext cards use them, with the states that have
 * no card of their own — opening, failed, cannot be opened at all — written on
 * the meta line. That is the treatment the composer tray already settled on.
 * Filed in docs/design/STATUS.md.
 *
 * What differs from the plaintext card is not visual. The server's row says
 * nothing true about this file — its filename is the literal placeholder and
 * its type is opaque — so every word here comes from the decrypted envelope,
 * and every byte comes back through `attachmentFiles`, which is the one place
 * a decrypted blob URL is ever minted.
 *
 * There is deliberately no `<a href>` anywhere below. The download is a
 * button, so the encrypted card holds no navigable URL at all: a decrypted
 * `blob:` URL exists only inside `saveDecrypted`, for one click, and is
 * revoked afterwards.
 */

type Saving = "idle" | "working" | "failed";

interface CardProps {
  attachment: Attachment;
  entry: AttachmentEntry;
}

/** One encrypted file: its real name, its preview if it has one, and a save. */
export function EncryptedAttachmentCard({ attachment, entry }: CardProps) {
  const { t, i18n } = useTranslation();
  const { info } = useInstance();
  const [preview, setPreview] = useState<string | null>(null);
  const [saving, setSaving] = useState<Saving>("idle");

  const thumbnailUrl = attachment.thumbnail_url ?? null;
  const { key } = entry;

  // Primitive dependencies, not the entry object: `entry` is rebuilt every
  // time the message body is decoded, so an object dependency here would
  // re-fetch and re-decrypt the preview on every render.
  useEffect(() => {
    if (thumbnailUrl === null) {
      return;
    }
    const abort = new AbortController();
    let minted: { url: string; revoke: () => void } | null = null;
    void (async () => {
      try {
        const bytes = await fetchDecrypted(
          thumbnailUrl,
          key,
          "thumb",
          MAX_THUMB_BYTES,
          abort.signal,
        );
        const blob = objectUrl(bytes);
        // Bytes that are not one of the four proven image types never become
        // a preview, whatever the sender's entry claimed they were.
        if (blob.imageType === "" || abort.signal.aborted) {
          blob.revoke();
          return;
        }
        minted = blob;
        setPreview(blob.url);
      } catch (error) {
        // A thumbnail that will not come back is the card's no-preview state,
        // which is a delivered one. Nothing else follows from it.
        console.warn("Could not open the preview:", error);
      }
    })();
    return () => {
      abort.abort();
      minted?.revoke();
      setPreview(null);
    };
  }, [key, thumbnailUrl]);

  // Sanitized HERE and not only where the envelope was parsed, because this
  // is where the name is shown and where it becomes a saved file's name. An
  // entry can also reach a card straight from an upload, and a path or a
  // right-to-left override must not survive either route.
  const cleaned = safeFilename(entry.name);
  const name = cleaned === "" ? t("chat.messages.unnamedFile") : cleaned;
  const size = formatFileSize(entry.size, i18n.language);
  const dimensions =
    entry.width === undefined || entry.height === undefined
      ? ""
      : ` · ${formatCount(entry.width, i18n.language)} × ${formatCount(entry.height, i18n.language)}`;
  const meta =
    saving === "working"
      ? t("chat.messages.fileOpening")
      : saving === "failed"
        ? t("chat.messages.fileFailed")
        : `${fileTypeLabel(entry.type)} · ${t(size.unitKey, { value: size.value })}${dimensions}`;

  const save = async () => {
    setSaving("working");
    try {
      const bytes = await fetchDecrypted(
        attachment.url,
        key,
        "original",
        info.max_file_size_bytes + SEAL_OVERHEAD_BYTES,
      );
      saveDecrypted(bytes, name);
      setSaving("idle");
    } catch (error) {
      console.warn("Could not open the file:", error);
      setSaving("failed");
    }
  };

  const control = (
    <button
      type="button"
      className="hm-icon-button hm-card__download"
      disabled={saving === "working"}
      aria-label={t("chat.messages.download", { filename: isolateLtr(name) })}
      onClick={() => {
        void save();
      }}
    >
      <DownloadIcon size={17} />
    </button>
  );

  // A row that carried a thumbnail is the one that gets the image shell: it
  // is the only thing the server knows about these bytes that is true.
  if (thumbnailUrl === null) {
    return (
      <div className="hm-card">
        <span className="hm-card__glyph">
          <FileTextIcon size={18} />
        </span>
        <div className="hm-card__text">
          {/* Filenames are isolated LTR runs inside Persian. */}
          <span className="hm-card__name" dir="ltr">
            {name}
          </span>
          <span className="hm-card__meta">{meta}</span>
        </div>
        {control}
      </div>
    );
  }

  return (
    <div className="hm-image-card">
      <div className="hm-image-card__frame">
        {preview === null ? (
          <ImageIcon size={30} strokeWidth={1.4} />
        ) : (
          <img className="hm-image-card__preview" src={preview} alt={name} />
        )}
      </div>
      <div className="hm-image-card__footer">
        <div className="hm-image-card__text">
          <span className="hm-image-card__name" dir="ltr">
            {name}
          </span>
          <span className="hm-card__meta">{meta}</span>
        </div>
        {control}
      </div>
    </div>
  );
}

/** Why a file in an encrypted conversation cannot be opened here. */
export type UnopenableReason =
  /** The message that carried its key cannot be read on this device. */
  | "message"
  /** The message opened and named no key for this file. */
  | "key";

/**
 * A file this device will never open, said plainly.
 *
 * This is the join boundary made visible, and it is a product fact rather
 * than a failure: a member who joined after a file was sent cannot read the
 * message that carries its key, so they cannot open the file — exactly as
 * they cannot read the words beside it (ADR 013). Drawing nothing at all
 * would be the dishonest option, because the row is there and the reader can
 * see that something was shared.
 */
export function UnopenableAttachmentCard({ reason }: { reason: UnopenableReason }) {
  const { t } = useTranslation();
  return (
    <div className="hm-card">
      <span className="hm-card__glyph">
        <FileTextIcon size={18} />
      </span>
      <div className="hm-card__text">
        <span className="hm-card__name">{t("chat.messages.encryptedFile")}</span>
        <span className="hm-card__meta">
          {reason === "message"
            ? t("chat.messages.fileNeedsMessage")
            : t("chat.messages.fileNoKey")}
        </span>
      </div>
    </div>
  );
}
