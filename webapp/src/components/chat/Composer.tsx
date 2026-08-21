import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { CircleAlertIcon, PaperclipIcon, SendIcon } from "../icons";
import { MentionPicker } from "./plumbing/MentionPicker";

interface ComposerProps {
  channelId: string;
  /** "#deploys" or a person's name — the placeholder names where this goes. */
  target: string;
  disabled: boolean;
  /** The reason always travels with the disabled state. */
  disabledReason: string | null;
  onSend: (content: string) => void;
}

/** Grows with content to 120px, then scrolls (chat-components -> composer). */
const MAX_BLOCK_SIZE = 120;

export function Composer({ channelId, target, disabled, disabledReason, onSend }: ComposerProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const fieldRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    const field = fieldRef.current;
    if (field === null) {
      return;
    }
    field.style.height = "auto";
    field.style.height = `${String(Math.min(field.scrollHeight, MAX_BLOCK_SIZE))}px`;
  }, [draft]);

  const ready = draft.trim() !== "" && !disabled;

  const submit = () => {
    if (!ready) {
      return;
    }
    onSend(draft);
    setDraft("");
  };

  return (
    <form
      className="hm-composer"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <div className="hm-composer__box" data-disabled={disabled}>
        {/* Uploads arrive with the Phase 1.3 pipeline; the control is drawn
            but has nothing behind it yet, so it carries its reason. The reason
            is in the accessible NAME, not only the tooltip: aria-label wins
            over title, and a disabled button shows no tooltip in Firefox. */}
        <button
          type="button"
          className="hm-icon-button"
          disabled
          aria-label={t("chat.composer.attachUnavailableLabel")}
          title={t("chat.composer.attachUnavailable")}
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
          onChange={(event) => {
            setDraft(event.target.value);
          }}
          onKeyDown={(event) => {
            // Enter sends, Shift+Enter opens a line. The hint row below says
            // so permanently rather than teaching it once.
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              submit();
            }
          }}
        />

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

      <MentionPicker
        channelId={channelId}
        onInsert={(token) => {
          setDraft((current) => (current === "" ? token : `${current} ${token}`));
          fieldRef.current?.focus();
        }}
      />

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
