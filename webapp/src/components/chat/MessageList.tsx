import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { daySeparatorLabel, formatTime, isolateAuto, isolateLtr } from "../../chat/format";
import type { MentionResolver } from "../../chat/mentions";
import { buildTimeline } from "../../chat/timeline";
import type { TimelineGroup } from "../../chat/timeline";
import type { Channel, Message, PendingMessage, UserSummary } from "../../chat/types";
import { ClockIcon, LoaderCircleIcon } from "../icons";
import { Avatar } from "./Avatar";
import { MessageBubble, PendingBubble } from "./MessageBubble";

interface MessageListProps {
  channel: Channel | undefined;
  channelId: string;
  messages: readonly Message[];
  pending: readonly PendingMessage[];
  currentUser: UserSummary;
  canModerate: boolean;
  resolveMention: MentionResolver;
  loading: boolean;
  loadingOlder: boolean;
  hasMoreOlder: boolean;
  dividerBeforeId: string | null;
  focusMessageId: string | null;
  /** Dimmed while the socket is down — the banner is the live layer. */
  dimmed: boolean;
  onLoadOlder: () => void;
  onEdit: (messageId: string, content: string) => Promise<boolean>;
  onDelete: (messageId: string) => Promise<boolean>;
}

/** How close to the bottom still counts as "following the conversation". */
const FOLLOW_THRESHOLD_PX = 120;

function DaySeparator({ iso }: { iso: string }) {
  const { t, i18n } = useTranslation();
  const label = daySeparatorLabel(iso, i18n.language);
  const text =
    label.kind === "today"
      ? t("chat.messages.today")
      : label.kind === "yesterday"
        ? t("chat.messages.yesterday")
        : label.text;
  return (
    <div className="hm-separator">
      <span className="hm-separator__rule" />
      <span className="hm-separator__label">{text}</span>
      <span className="hm-separator__rule" />
    </div>
  );
}

function UnreadDivider() {
  const { t } = useTranslation();
  return (
    <div className="hm-divider">
      <span className="hm-divider__rule" />
      <span className="hm-divider__label">{t("chat.messages.newMessages")}</span>
      <span className="hm-divider__rule" />
    </div>
  );
}

function HistorySkeleton() {
  return (
    <>
      {[0, 1, 2].map((index) => (
        <div className="hm-msg-group" key={index} data-own={index === 1}>
          <div className="hm-msg-group__meta">
            {index === 1 ? null : <span className="hm-skeleton hm-skeleton--avatar" />}
            <span className="hm-skeleton hm-skeleton--name" />
          </div>
          <div className="hm-msg">
            <div
              className="hm-skeleton hm-skeleton--bubble"
              style={{ inlineSize: `${String(250 + index * 60)}px` }}
            />
          </div>
        </div>
      ))}
    </>
  );
}

interface GroupProps extends Pick<
  MessageListProps,
  "channelId" | "canModerate" | "resolveMention" | "onEdit"
> {
  group: TimelineGroup;
  onRequestDelete: (messageId: string) => void;
}

function MessageGroupView({
  group,
  channelId,
  canModerate,
  resolveMention,
  onEdit,
  onRequestDelete,
}: GroupProps) {
  const { t, i18n } = useTranslation();
  const queued = group.entries.some(
    (entry) => entry.kind === "pending" && entry.pending.status === "queued",
  );

  return (
    <div className="hm-msg-group" data-own={group.own} data-live="true">
      <div className="hm-msg-group__meta">
        {group.own ? null : (
          <Avatar
            userId={group.author.id}
            displayName={group.author.display_name}
            size={26}
            typeSize={10}
          />
        )}
        <span className="hm-msg-group__author">{group.author.display_name}</span>
        {queued ? (
          <span className="hm-msg-group__queued">
            <ClockIcon size={12} strokeWidth={2} />
            {t("chat.messages.waitingToSend")}
          </span>
        ) : (
          <time className="hm-msg-group__time" dateTime={group.createdAt}>
            {formatTime(group.createdAt, i18n.language)}
          </time>
        )}
      </div>
      {group.entries.map((entry, index) =>
        entry.kind === "message" ? (
          <MessageBubble
            key={entry.id}
            message={entry.message}
            first={index === 0}
            own={group.own}
            canModerate={canModerate}
            channelId={channelId}
            resolveMention={resolveMention}
            onEdit={onEdit}
            onDelete={onRequestDelete}
          />
        ) : (
          <PendingBubble key={entry.id} pending={entry.pending} first={index === 0} />
        ),
      )}
    </div>
  );
}

/**
 * The conversation: day separators, the unread divider, and grouped runs of
 * bubbles. A labelled `log` region, so arrivals are announced politely and
 * nothing steals focus from the composer.
 */
export function MessageList({
  channel,
  channelId,
  messages,
  pending,
  currentUser,
  canModerate,
  resolveMention,
  loading,
  loadingOlder,
  hasMoreOlder,
  dividerBeforeId,
  focusMessageId,
  dimmed,
  onLoadOlder,
  onEdit,
  onDelete,
}: MessageListProps) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);

  const items = buildTimeline({ messages, pending, currentUser, dividerBeforeId });
  const lastKey = `${channelId}:${String(messages.length)}:${String(pending.length)}`;

  /* Follow the conversation: jump to the newest message on entry, and keep
   * following only while the reader is already near the bottom. */
  const previousChannel = useRef<string | null>(null);
  useEffect(() => {
    const element = scrollRef.current;
    if (element === null) {
      return;
    }
    const entered = previousChannel.current !== channelId;
    previousChannel.current = channelId;
    const distance = element.scrollHeight - element.scrollTop - element.clientHeight;
    if (entered || distance < FOLLOW_THRESHOLD_PX) {
      element.scrollTop = element.scrollHeight;
    }
  }, [channelId, lastKey]);

  /* A permalink lands on its message rather than at the bottom. */
  useEffect(() => {
    if (focusMessageId === null) {
      return;
    }
    const target = scrollRef.current?.querySelector(`[data-message-id="${focusMessageId}"]`);
    target?.scrollIntoView({ block: "center" });
  }, [focusMessageId, lastKey]);

  /* Scrollback: the top sentinel asks for the previous page as it comes into
   * view, which keyboard scrolling triggers exactly like the wheel does. */
  useEffect(() => {
    const sentinel = sentinelRef.current;
    if (sentinel === null || !hasMoreOlder || typeof IntersectionObserver === "undefined") {
      return undefined;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        onLoadOlder();
      }
    });
    observer.observe(sentinel);
    return () => {
      observer.disconnect();
    };
  }, [hasMoreOlder, onLoadOlder, channelId]);

  // The name as it is spoken about: "#deploys", or the person for a DM.
  const channelName =
    channel === undefined
      ? ""
      : channel.kind === "dm"
        ? isolateAuto(channel.dm_peer?.display_name ?? "")
        : isolateLtr(`#${channel.slug ?? ""}`);

  return (
    <div
      className="hm-messages"
      ref={scrollRef}
      role="log"
      aria-label={t("chat.messages.listLabel", { channel: channelName })}
      data-dimmed={dimmed}
    >
      {hasMoreOlder ? <div ref={sentinelRef} data-testid="hm-history-sentinel" /> : null}

      {loadingOlder || loading ? (
        <>
          <p className="hm-history-loader">
            <LoaderCircleIcon size={16} strokeWidth={2} className="hm-spin" />
            {t("chat.messages.loadingOlder")}
          </p>
          <HistorySkeleton />
        </>
      ) : null}

      {items.map((item) =>
        item.kind === "day" ? (
          <DaySeparator key={item.key} iso={item.iso} />
        ) : item.kind === "unread" ? (
          <UnreadDivider key={item.key} />
        ) : (
          <MessageGroupView
            key={item.key}
            group={item}
            channelId={channelId}
            canModerate={canModerate}
            resolveMention={resolveMention}
            onEdit={onEdit}
            onRequestDelete={setConfirmDeleteId}
          />
        ),
      )}

      {confirmDeleteId === null ? null : (
        <DeleteConfirmation
          channelName={channelName}
          onCancel={() => {
            setConfirmDeleteId(null);
          }}
          onConfirm={() => {
            const target = confirmDeleteId;
            setConfirmDeleteId(null);
            void onDelete(target);
          }}
        />
      )}
    </div>
  );
}

interface DeleteConfirmationProps {
  channelName: string;
  onCancel: () => void;
  onConfirm: () => void;
}

/** Delete is the only accent-red control in the set, and it always confirms. */
function DeleteConfirmation({ channelName, onCancel, onConfirm }: DeleteConfirmationProps) {
  const { t } = useTranslation();
  const confirmRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    confirmRef.current?.focus();
  }, []);

  return (
    <div className="hm-confirm-layer">
      <div
        className="hm-confirm"
        role="dialog"
        aria-modal="true"
        aria-labelledby="hm-delete-title"
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            onCancel();
          }
        }}
      >
        <h2 className="hm-confirm__title" id="hm-delete-title">
          {t("chat.messages.deleteConfirm.title")}
        </h2>
        <p className="hm-confirm__body">
          {t("chat.messages.deleteConfirm.body", { channel: channelName })}
        </p>
        <div className="hm-confirm__actions">
          <button
            ref={confirmRef}
            type="button"
            className="hm-compact-button hm-compact-button--danger"
            onClick={onConfirm}
          >
            {t("chat.messages.deleteConfirm.confirm")}
          </button>
          <button type="button" className="hm-compact-button" onClick={onCancel}>
            {t("chat.messages.deleteConfirm.cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}
