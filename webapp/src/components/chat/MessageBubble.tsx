import { memo, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { messageLink } from "../../chat/links";
import type { MentionResolver } from "../../chat/mentions";
import type { Message, PendingMessage } from "../../chat/types";
import { useMessageBody } from "../../mls/MessageBodyContext";
import { LinkIcon, PencilIcon, TrashIcon } from "../icons";
import { AttachmentCards, AttachmentList } from "./AttachmentCards";
import { MessageContent } from "./MessageContent";

interface MessageBubbleProps {
  message: Message;
  /** First bubble of a run — the one that notches its leading top corner. */
  first: boolean;
  own: boolean;
  /** An org admin may delete anyone's message in a channel they are in. */
  canModerate: boolean;
  channelId: string;
  resolveMention: MentionResolver;
  onEdit: (messageId: string, content: string) => Promise<boolean>;
  onDelete: (messageId: string) => void;
}

/**
 * Memoized: a channel holds up to fifty of these, each one rendering markdown
 * through a plugin pipeline. Without this every reducer action — including the
 * one-per-second offline countdown — re-renders the whole conversation.
 */
export const MessageBubble = memo(function MessageBubble({
  message,
  first,
  own,
  canModerate,
  channelId,
  resolveMention,
  onEdit,
  onDelete,
}: MessageBubbleProps) {
  const { t } = useTranslation();
  const body = useMessageBody(message);
  // What the composer starts from, and what an edit replaces. Empty for a
  // body this device cannot read — which is also why editing is hidden below.
  const readableText = body.kind === "plaintext" || body.kind === "decrypted" ? body.text : "";
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(readableText);
  const editRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    if (editing) {
      editRef.current?.focus();
    }
  }, [editing]);

  const removed = message.deleted_at !== undefined && message.deleted_at !== null;
  const edited = message.edited_at !== undefined && message.edited_at !== null;

  if (editing) {
    // Editing replaces the bubble with an inline composer; the message keeps
    // its position in the conversation.
    return (
      <div className="hm-msg" data-first={first}>
        <form
          className="hm-msg__edit"
          onSubmit={(event) => {
            event.preventDefault();
            void onEdit(message.id, draft).then((ok) => {
              if (ok) {
                setEditing(false);
              }
            });
          }}
        >
          <textarea
            ref={editRef}
            className="hm-msg__edit-field"
            value={draft}
            aria-label={t("chat.messages.editLabel")}
            onChange={(event) => {
              setDraft(event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                setEditing(false);
                setDraft(readableText);
              }
            }}
          />
          <div className="hm-confirm__actions">
            <button type="submit" className="hm-compact-button hm-compact-button--primary">
              {t("chat.messages.saveEdit")}
            </button>
            <button
              type="button"
              className="hm-compact-button"
              onClick={() => {
                setEditing(false);
                setDraft(readableText);
              }}
            >
              {t("chat.messages.cancelEdit")}
            </button>
          </div>
        </form>
      </div>
    );
  }

  return (
    <div className="hm-msg" data-first={first} data-message-id={message.id}>
      <div className="hm-msg__bubble" data-removed={removed}>
        {removed ? (
          <span className="hm-msg__removed">{t("chat.messages.removed")}</span>
        ) : body.kind === "pending" ? (
          <span className="hm-msg__decrypting">{t("chat.e2ee.decrypting")}</span>
        ) : body.kind === "undecryptable" ? (
          // A real MLS condition, not a failure to report: a message sent
          // before this device joined the group cannot be opened by it, and
          // never will be. Saying so is the honest rendering.
          <span className="hm-msg__undecryptable">{t("chat.e2ee.cannotDecrypt")}</span>
        ) : (
          <>
            {/* Decrypted text goes through the same sanitizing markdown path
                as any other message body — decryption is not a reason to
                trust the author. */}
            <MessageContent content={body.text} resolveMention={resolveMention} />
            {edited ? (
              <span className="hm-msg__edited">{t("chat.messages.edited")}</span>
            ) : null}
            <AttachmentCards message={message} />
          </>
        )}
      </div>

      {removed ? null : (
        <div className="hm-msg__actions">
          {own && (body.kind === "plaintext" || body.kind === "decrypted") ? (
            <button
              type="button"
              className="hm-msg__action"
              aria-label={t("chat.messages.actions.edit")}
              onClick={() => {
                setDraft(readableText);
                setEditing(true);
              }}
            >
              <PencilIcon size={15} strokeWidth={1.85} />
            </button>
          ) : null}
          <button
            type="button"
            className="hm-msg__action"
            aria-label={t("chat.messages.actions.copyLink")}
            onClick={() => {
              // Clipboard access can be denied, or missing entirely outside a
              // secure context; a failed copy must not break the message.
              try {
                void navigator.clipboard
                  .writeText(messageLink(channelId, message.id))
                  .catch((error: unknown) => {
                    console.warn("Could not copy the message link:", error);
                  });
              } catch (error) {
                console.warn("Clipboard is unavailable:", error);
              }
            }}
          >
            <LinkIcon size={15} strokeWidth={1.85} />
          </button>
          {own || canModerate ? (
            <button
              type="button"
              className="hm-msg__action hm-msg__action--danger"
              aria-label={t("chat.messages.actions.delete")}
              onClick={() => {
                onDelete(message.id);
              }}
            >
              <TrashIcon size={15} strokeWidth={1.85} />
            </button>
          ) : null}
        </div>
      )}
    </div>
  );
});

interface PendingBubbleProps {
  pending: PendingMessage;
  first: boolean;
}

/**
 * An unconfirmed message. While a request is in flight it looks like any own
 * bubble; once the attempt has failed it takes the dashed treatment and the
 * group carries a "Waiting to send" marker — kept, never dropped.
 */
export function PendingBubble({ pending, first }: PendingBubbleProps) {
  const queued = pending.status === "queued";
  return (
    <div className="hm-msg" data-first={first}>
      <div className="hm-msg__bubble" data-queued={queued}>
        {/* An empty body is legal here: a message may carry files and no
            text, and drawing an empty paragraph for one would be a bubble
            that says nothing while the cards below say everything. */}
        {pending.content === "" ? null : queued ? (
          <span className="hm-msg__queued-body" dir="auto">
            {pending.content}
          </span>
        ) : (
          <div className="hm-md" dir="auto">
            <p>{pending.content}</p>
          </div>
        )}
        <AttachmentList attachments={pending.attachments} />
      </div>
    </div>
  );
}
