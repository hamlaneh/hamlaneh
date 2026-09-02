import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatFileSize } from "../../chat/format";
import type { Attachment } from "../../chat/types";
import {
  MAX_ATTACHMENTS,
  plaintextBudget,
  uploadAttachment,
  type UploadFailure,
} from "../../chat/uploads";
import { entriesFor } from "../../mls/attachments";
import { useInstance } from "../../instance/instanceInfo";
import { CircleAlertIcon, FileTextIcon, PaperclipIcon, SendIcon, UserIcon, XIcon } from "../icons";
import { AttachmentList } from "./AttachmentCards";
import { useMentions } from "./plumbing/MentionPicker";

interface ComposerProps {
  channelId: string;
  /**
   * Whether this conversation is encrypted. It changes what an upload is: the
   * file is sealed on this device first, so the budget is a little smaller
   * and the card is drawn from what only this device knows (ADR 013).
   */
  e2ee: boolean;
  /** "#deploys" or a person's name — the placeholder names where this goes. */
  target: string;
  disabled: boolean;
  /** The reason always travels with the disabled state. */
  disabledReason: string | null;
  onSend: (content: string, attachments: Attachment[]) => void;
}

/** Grows with content to 120px, then scrolls (chat-components -> composer). */
const MAX_BLOCK_SIZE = 120;

/**
 * One picked file on its way into the message.
 *
 * UNDESIGNED STATES — no artboard draws a file waiting in the composer, so
 * the tray below is assembled from delivered parts only: the delivered file
 * and image cards once an upload has finished, and the `hm-card` shell with
 * its own meta line for the two states that have no card of their own.
 * Filed in docs/design/STATUS.md.
 */
type PendingUpload =
  | { key: string; filename: string; status: "uploading" }
  | { key: string; filename: string; status: "failed"; reason: UploadFailure }
  | { key: string; filename: string; status: "ready"; attachment: Attachment };

let uploadKeySeed = 0;

export function Composer({
  channelId,
  e2ee,
  target,
  disabled,
  disabledReason,
  onSend,
}: ComposerProps) {
  const { t, i18n } = useTranslation();
  const { info } = useInstance();
  // Sealing costs a nonce and a tag, and the cap the server enforces applies
  // to the bytes it receives, which on an encrypted channel are ciphertext.
  const budget = plaintextBudget(info.max_file_size_bytes, e2ee);
  const [draft, setDraft] = useState("");
  const [uploads, setUploads] = useState<PendingUpload[]>([]);
  /** The one refusal that belongs to the pick rather than to any one file. */
  const [tooMany, setTooMany] = useState(false);
  const fieldRef = useRef<HTMLTextAreaElement | null>(null);
  const pickerRef = useRef<HTMLInputElement | null>(null);
  /* The mention list is an editable combobox over this very field: focus
     never leaves the composer, and the picker only ever edits the draft
     (chat-addendum-overlay-components -> 03). */
  const mentions = useMentions({ channelId, draft, setDraft, fieldRef });

  useEffect(() => {
    const field = fieldRef.current;
    if (field === null) {
      return;
    }
    field.style.height = "auto";
    field.style.height = `${String(Math.min(field.scrollHeight, MAX_BLOCK_SIZE))}px`;
  }, [draft]);

  const attachments = uploads.flatMap((entry) =>
    entry.status === "ready" ? [entry.attachment] : [],
  );
  // Send waits for every picked file to have settled: one still uploading
  // would be silently dropped from the message, and a failed one has to be
  // removed deliberately rather than quietly forgotten.
  const settled = attachments.length === uploads.length;
  const ready = (draft.trim() !== "" || attachments.length > 0) && settled && !disabled;

  const failureText = (reason: UploadFailure): string => {
    switch (reason) {
      case "tooLarge": {
        const limit = formatFileSize(budget, i18n.language);
        return t("chat.composer.uploadTooLarge", {
          limit: t(limit.unitKey, { value: limit.value }),
        });
      }
      case "typeMismatch":
        return t("chat.composer.uploadTypeMismatch");
      case "failed":
        return t("chat.composer.uploadFailed");
    }
  };

  const pick = (files: readonly File[]) => {
    // Room is counted against everything already in the tray, settled or
    // not: attachment_ids carries at most MAX_ATTACHMENTS, and a send the
    // server would refuse wholesale is worse than a pick that says no.
    const accepted = files.slice(0, Math.max(0, MAX_ATTACHMENTS - uploads.length));
    setTooMany(accepted.length < files.length);

    const started: PendingUpload[] = accepted.map((file) => {
      uploadKeySeed += 1;
      const key = `upload-${String(uploadKeySeed)}`;
      return file.size > budget
        ? // The server enforces the cap regardless (openapi.yaml: "a
          // courtesy, not the check"); refusing here only saves the person's
          // bandwidth on a request that cannot succeed.
          { key, filename: file.name, status: "failed", reason: "tooLarge" }
        : { key, filename: file.name, status: "uploading" };
    });
    setUploads((current) => [...current, ...started]);

    for (const [index, entry] of started.entries()) {
      const file = accepted[index];
      if (entry.status !== "uploading" || file === undefined) {
        continue;
      }
      // One request per file, so each has its own outcome (openapi.yaml ->
      // uploadFile). A slow one never holds up the others.
      void uploadAttachment(channelId, file, e2ee).then((result) => {
        setUploads((current) =>
          current.map((other) =>
            other.key !== entry.key
              ? other
              : result.ok
                ? { ...entry, status: "ready", attachment: result.attachment }
                : { ...entry, status: "failed", reason: result.reason },
          ),
        );
      });
    }
  };

  const submit = () => {
    if (!ready) {
      return;
    }
    onSend(draft, attachments);
    setDraft("");
    setUploads([]);
    setTooMany(false);
  };

  return (
    <form
      className="hm-composer"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      {uploads.length === 0 ? null : (
        <ul className="hm-composer__attachments" aria-label={t("chat.composer.attachments")}>
          {uploads.map((entry) => (
            <li key={entry.key} className="hm-composer__attachment">
              {entry.status === "ready" ? (
                <AttachmentList
                  attachments={[entry.attachment]}
                  entries={e2ee ? entriesFor([entry.attachment.id]) : undefined}
                />
              ) : (
                <div className="hm-card">
                  <span className="hm-card__glyph">
                    <FileTextIcon size={18} />
                  </span>
                  <div className="hm-card__text">
                    {/* Filenames are isolated LTR runs inside Persian. */}
                    <span className="hm-card__name" dir="ltr">
                      {entry.filename}
                    </span>
                    <span className="hm-card__meta">
                      {entry.status === "uploading"
                        ? t("chat.composer.uploading")
                        : failureText(entry.reason)}
                    </span>
                  </div>
                </div>
              )}
              <button
                type="button"
                className="hm-icon-button"
                aria-label={t("chat.composer.removeAttachment", { filename: entry.filename })}
                onClick={() => {
                  setUploads((current) => current.filter((other) => other.key !== entry.key));
                }}
              >
                <XIcon size={16} />
              </button>
            </li>
          ))}
        </ul>
      )}

      {tooMany ? (
        <p className="hm-composer__reason" role="status">
          <CircleAlertIcon size={14} strokeWidth={2} />
          {t("chat.composer.uploadTooMany", { max: MAX_ATTACHMENTS })}
        </p>
      ) : null}

      <div className="hm-composer__box" data-disabled={disabled}>
        {/* The file input is the control; the paperclip the design draws is
            what opens it. The button carries the accessible name because a
            bare input would draw its own chrome the artboard has no slot for. */}
        <input
          ref={pickerRef}
          type="file"
          multiple
          hidden
          onChange={(event) => {
            pick([...(event.target.files ?? [])]);
            // Cleared so picking the same file twice in a row still fires.
            event.target.value = "";
          }}
        />
        <button
          type="button"
          className="hm-icon-button"
          disabled={disabled}
          aria-label={t("chat.composer.attach")}
          onClick={() => {
            pickerRef.current?.click();
          }}
        >
          <PaperclipIcon size={18} />
        </button>

        <textarea
          ref={fieldRef}
          className="hm-composer__field"
          rows={1}
          value={draft}
          disabled={disabled}
          placeholder={t("chat.composer.placeholder", { target })}
          aria-label={t("chat.composer.placeholder", { target })}
          /* The combobox semantics belong to the moment there is a list to
             navigate. Closed, this is the plain multi-line message field the
             rest of the shell (and every caller) treats it as. */
          {...(mentions.open
            ? {
                role: "combobox",
                "aria-expanded": true,
                "aria-autocomplete": "list" as const,
                "aria-controls": mentions.listId,
                ...(mentions.activeId === undefined
                  ? {}
                  : { "aria-activedescendant": mentions.activeId }),
              }
            : {})}
          onChange={(event) => {
            setDraft(event.target.value);
            mentions.sync();
          }}
          onKeyUp={mentions.sync}
          onClick={mentions.sync}
          onKeyDown={(event) => {
            // The picker answers first: while it is open, Enter inserts rather
            // than sends and the arrows move the active row instead of the
            // caret. Tab and Left/Right fall through on purpose.
            if (mentions.handleKeyDown(event)) {
              return;
            }
            // Enter sends, Shift+Enter opens a line. The hint row below says
            // so permanently rather than teaching it once.
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              submit();
            }
          }}
        />

        {/* No artboard draws this control, so it is assembled from delivered
            parts: the composer's own icon button, carrying the delivered
            "Mention someone" name. It opens the same listbox a typed `@`
            does, and hands focus straight back to the field. */}
        <button
          type="button"
          className="hm-icon-button"
          disabled={disabled}
          aria-label={t("chat.composer.mention")}
          aria-expanded={mentions.open}
          onClick={mentions.openFromTrigger}
        >
          <UserIcon size={18} />
        </button>

        <button
          type="submit"
          className="hm-icon-button hm-composer__send"
          data-ready={ready}
          disabled={!ready}
          aria-label={t("chat.composer.send")}
        >
          {/* Send is one of the three directional glyphs that mirror in RTL
              (chat-components -> Bidi); attach and search do not. */}
          <SendIcon size={18} strokeWidth={1.85} className="hm-mirror-glyph" />
        </button>
      </div>

      {mentions.popover}

      {disabled && disabledReason !== null ? (
        <p className="hm-composer__reason" role="status">
          <CircleAlertIcon size={14} strokeWidth={2} />
          {disabledReason}
        </p>
      ) : (
        <>
          <p className="hm-composer__hints">
            <span>{t("chat.composer.hintBold")}</span>
            <span>{t("chat.composer.hintItalic")}</span>
            <span>{t("chat.composer.hintCode")}</span>
            <span>{t("chat.composer.hintQuote")}</span>
            <span className="hm-composer__hint-send">{t("chat.composer.hintSend")}</span>
          </p>
          <p className="hm-composer__hints-compact">{t("chat.composer.hintCompact")}</p>
        </>
      )}
    </form>
  );
}
