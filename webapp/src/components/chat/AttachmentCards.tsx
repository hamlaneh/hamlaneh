import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  fileTypeLabel,
  formatCount,
  formatFileSize,
  linkPreviewHost,
} from "../../chat/format";
import type { Attachment, LinkPreview, Message } from "../../chat/types";
import { isolateLtr } from "../../i18n/bidi";
import type { AttachmentEntry } from "../../mls/attachments";
import { DownloadIcon, FileTextIcon, ImageIcon } from "../icons";
import { EncryptedAttachmentCard, UnopenableAttachmentCard } from "./EncryptedAttachments";

/**
 * The file, image and link-preview cards from chat-components.
 *
 * Nothing here fetches anything on its own: an Attachment arrives with its
 * url and thumbnail_url already signed, and `AttachmentList` is also what the
 * composer's pending tray and the optimistic bubble render, so an uploaded
 * file looks the same before the send as after it.
 *
 * Signed URLs expire in about an hour and the contract says never to store
 * them. This component stores nothing, but the chat store keeps message
 * objects for as long as the tab lives — see `ImageCard` below and
 * docs/design/STATUS.md for what that means once an hour has passed.
 */

function isImage(attachment: Attachment): boolean {
  return attachment.content_type.startsWith("image/");
}

interface DownloadLinkProps {
  url: string;
  filename: string;
}

function DownloadLink({ url, filename }: DownloadLinkProps) {
  const { t } = useTranslation();
  return (
    <a
      className="hm-icon-button hm-card__download"
      href={url}
      download={filename}
      rel="noopener"
      aria-label={t("chat.messages.download", { filename: isolateLtr(filename) })}
    >
      <DownloadIcon size={17} />
    </a>
  );
}

function FileCard({ attachment }: { attachment: Attachment }) {
  const { t, i18n } = useTranslation();
  const size = formatFileSize(attachment.size_bytes, i18n.language);
  return (
    <div className="hm-card">
      <span className="hm-card__glyph">
        <FileTextIcon size={18} />
      </span>
      <div className="hm-card__text">
        {/* Filenames are isolated LTR runs inside Persian. */}
        <span className="hm-card__name" dir="ltr">
          {attachment.filename}
        </span>
        <span className="hm-card__meta">
          {`${fileTypeLabel(attachment.content_type)} · ${t(size.unitKey, {
            value: size.value,
          })}`}
        </span>
      </div>
      <DownloadLink url={attachment.url} filename={attachment.filename} />
    </div>
  );
}

function ImageCard({ attachment }: { attachment: Attachment }) {
  const { t, i18n } = useTranslation();
  // A thumbnail URL that has outlived its signature answers 404, and a broken
  // <img> glyph is nobody's design. Falling back to the frame the card
  // already draws for an attachment that has no thumbnail keeps the failure
  // inside a delivered state instead of inventing one.
  const [thumbnailFailed, setThumbnailFailed] = useState(false);
  const dimensions =
    attachment.width !== undefined &&
    attachment.width !== null &&
    attachment.height !== undefined &&
    attachment.height !== null
      ? ` · ${formatCount(attachment.width, i18n.language)} × ${formatCount(
          attachment.height,
          i18n.language,
        )}`
      : "";
  const size = formatFileSize(attachment.size_bytes, i18n.language);

  return (
    <div className="hm-image-card">
      <div className="hm-image-card__frame">
        {attachment.thumbnail_url === undefined ||
        attachment.thumbnail_url === null ||
        thumbnailFailed ? (
          <ImageIcon size={30} strokeWidth={1.4} />
        ) : (
          <img
            className="hm-image-card__preview"
            src={attachment.thumbnail_url}
            alt={attachment.filename}
            onError={() => {
              setThumbnailFailed(true);
            }}
          />
        )}
      </div>
      <div className="hm-image-card__footer">
        <div className="hm-image-card__text">
          <span className="hm-image-card__name" dir="ltr">
            {attachment.filename}
          </span>
          <span className="hm-card__meta">
            {`${fileTypeLabel(attachment.content_type)} · ${t(size.unitKey, {
              value: size.value,
            })}${dimensions}`}
          </span>
        </div>
        <DownloadLink url={attachment.url} filename={attachment.filename} />
      </div>
    </div>
  );
}

function LinkPreviewCard({ preview }: { preview: LinkPreview }) {
  return (
    <a
      className="hm-link-card"
      href={preview.url}
      target="_blank"
      rel="noopener noreferrer nofollow ugc"
    >
      <span className="hm-link-card__thumb">
        <ImageIcon size={22} strokeWidth={1.5} />
      </span>
      <span className="hm-link-card__text">
        {preview.title === undefined || preview.title === null ? null : (
          <span className="hm-link-card__title">{preview.title}</span>
        )}
        {preview.description === undefined || preview.description === null ? null : (
          <span className="hm-link-card__description">{preview.description}</span>
        )}
        {/* The host line is derived client-side and stays an LTR run. */}
        <span className="hm-link-card__host" dir="ltr">
          {linkPreviewHost(preview.url)}
        </span>
      </span>
    </a>
  );
}

/**
 * The cards for a list of attachments — a stored message's, or a pending one's.
 *
 * `entries` is what makes a card encrypted: present, and every card is drawn
 * from the decrypted envelope (ADR 013) rather than from the server's row,
 * whose filename is the literal placeholder and whose type is opaque. Absent
 * is the plaintext channel, unchanged.
 *
 * An id with no entry gets the cannot-be-opened card rather than the
 * placeholder one. That is not a defensive nicety: it is what a hostile or
 * truncated envelope produces, and drawing "encrypted · OCTET-STREAM" there
 * would be showing the reader the server's fiction as though it were the file.
 */
export function AttachmentList({
  attachments,
  entries,
}: {
  attachments: readonly Attachment[];
  entries?: readonly AttachmentEntry[] | undefined;
}) {
  if (entries !== undefined) {
    return (
      <>
        {attachments.map((attachment) => {
          const entry = entries.find((candidate) => candidate.id === attachment.id);
          return entry === undefined ? (
            <UnopenableAttachmentCard key={attachment.id} reason="key" />
          ) : (
            <EncryptedAttachmentCard key={attachment.id} attachment={attachment} entry={entry} />
          );
        })}
      </>
    );
  }
  return (
    <>
      {attachments.map((attachment) =>
        isImage(attachment) ? (
          <ImageCard key={attachment.id} attachment={attachment} />
        ) : (
          <FileCard key={attachment.id} attachment={attachment} />
        ),
      )}
    </>
  );
}

/**
 * The files of a message this device cannot read.
 *
 * The key rides inside the message, so a message that will not open takes its
 * files with it. Saying so is the honest rendering of E2EE's join boundary
 * applied to files; drawing nothing would leave the reader to guess.
 */
export function UnreadableAttachments({ message }: { message: Message }) {
  return (
    <>
      {message.attachments.map((attachment) => (
        <UnopenableAttachmentCard key={attachment.id} reason="message" />
      ))}
    </>
  );
}

export function AttachmentCards({
  message,
  entries,
}: {
  message: Message;
  entries?: readonly AttachmentEntry[] | undefined;
}) {
  return (
    <>
      <AttachmentList attachments={message.attachments} entries={entries} />
      {message.link_preview === undefined ? null : (
        <LinkPreviewCard preview={message.link_preview} />
      )}
    </>
  );
}
