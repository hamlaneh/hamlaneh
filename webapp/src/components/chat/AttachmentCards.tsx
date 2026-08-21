import { useTranslation } from "react-i18next";

import {
  fileTypeLabel,
  formatCount,
  formatFileSize,
  isolateLtr,
  linkPreviewHost,
} from "../../chat/format";
import type { Attachment, LinkPreview, Message } from "../../chat/types";
import { DownloadIcon, FileTextIcon, ImageIcon } from "../icons";

/**
 * The file, image and link-preview cards from chat-components.
 *
 * Both arrays are always empty in Phase 1.2 — attachments arrive with the
 * upload pipeline and previews with the egress proxy (openapi.yaml). The
 * cards are implemented now because the design draws them and the contract
 * types them; nothing here fetches anything on its own.
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
  const { i18n } = useTranslation();
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
          {`${fileTypeLabel(attachment.content_type)} · ${formatFileSize(
            attachment.size_bytes,
            i18n.language,
          )}`}
        </span>
      </div>
      <DownloadLink url={attachment.url} filename={attachment.filename} />
    </div>
  );
}

function ImageCard({ attachment }: { attachment: Attachment }) {
  const { i18n } = useTranslation();
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

  return (
    <div className="hm-image-card">
      <div className="hm-image-card__frame">
        {attachment.thumbnail_url === undefined || attachment.thumbnail_url === null ? (
          <ImageIcon size={30} strokeWidth={1.4} />
        ) : (
          <img
            className="hm-image-card__preview"
            src={attachment.thumbnail_url}
            alt={attachment.filename}
          />
        )}
      </div>
      <div className="hm-image-card__footer">
        <div className="hm-image-card__text">
          <span className="hm-image-card__name" dir="ltr">
            {attachment.filename}
          </span>
          <span className="hm-card__meta">
            {`${fileTypeLabel(attachment.content_type)} · ${formatFileSize(
              attachment.size_bytes,
              i18n.language,
            )}${dimensions}`}
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

export function AttachmentCards({ message }: { message: Message }) {
  return (
    <>
      {message.attachments.map((attachment) =>
        isImage(attachment) ? (
          <ImageCard key={attachment.id} attachment={attachment} />
        ) : (
          <FileCard key={attachment.id} attachment={attachment} />
        ),
      )}
      {message.link_preview === undefined ? null : (
        <LinkPreviewCard preview={message.link_preview} />
      )}
    </>
  );
}
