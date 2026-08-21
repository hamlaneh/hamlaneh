import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router";

import { formatCount } from "../../chat/format";
import { PRESENCE_LABEL_KEY } from "../../chat/presence";
import type { Channel, User } from "../../chat/types";
import { HashIcon, LockIcon, NestMark, PlusIcon, SettingsIcon, XIcon } from "../icons";
import { Avatar } from "./Avatar";

interface SidebarProps {
  channels: readonly Channel[];
  currentUser: User;
  organizationName: string;
  open: boolean;
  onDismiss: () => void;
  onCreateChannel: () => void;
  onNewDirectMessage: () => void;
  onToggleAccountMenu: () => void;
  accountMenu: ReactNode;
}

/** Persian and English both sort by the label the row draws. */
function byLabel(left: string, right: string): number {
  return left.localeCompare(right);
}

function channelLabel(channel: Channel): string {
  return channel.slug ?? channel.dm_peer?.display_name ?? "";
}

interface ConversationRowProps {
  channel: Channel;
  label: string;
  onDismiss: () => void;
  children: ReactNode;
}

function ConversationRow({ channel, label, onDismiss, children }: ConversationRowProps) {
  const { t, i18n } = useTranslation();
  const mention = channel.mention_count > 0;
  const unread = channel.unread_count > 0 || mention;

  return (
    <li>
      <NavLink className="hm-row" to={`/c/${channel.id}`} data-unread={unread} onClick={onDismiss}>
        {children}
        {/* A channel slug is an isolated LTR run inside Persian; a person's
            name follows its own direction. */}
        <span className="hm-row__name" dir={channel.kind === "dm" ? "auto" : "ltr"}>
          {label}
        </span>
        {mention ? (
          <span className="hm-badge hm-badge--mention">
            {`@${formatCount(channel.mention_count, i18n.language)}`}
            <span className="hm-visually-hidden"> {t("chat.sidebar.mentionsLabel")}</span>
          </span>
        ) : channel.unread_count > 0 ? (
          <span className="hm-badge hm-badge--unread">
            {formatCount(channel.unread_count, i18n.language)}
            <span className="hm-visually-hidden"> {t("chat.sidebar.unreadLabel")}</span>
          </span>
        ) : null}
      </NavLink>
    </li>
  );
}

/**
 * Channels, direct messages and the current user. Below 900px the same markup
 * becomes a drawer over a scrim; nothing about its structure changes.
 */
export function Sidebar({
  channels,
  currentUser,
  organizationName,
  open,
  onDismiss,
  onCreateChannel,
  onNewDirectMessage,
  onToggleAccountMenu,
  accountMenu,
}: SidebarProps) {
  const { t } = useTranslation();

  const rooms = channels
    .filter((channel) => channel.kind !== "dm")
    .sort((left, right) => byLabel(channelLabel(left), channelLabel(right)));
  const directMessages = channels
    .filter((channel) => channel.kind === "dm")
    .sort((left, right) => byLabel(channelLabel(left), channelLabel(right)));

  return (
    <nav className="hm-sidebar" aria-label={t("chat.sidebar.label")} data-open={open}>
      <div className="hm-sidebar__org">
        <NestMark size={26} />
        <span className="hm-sidebar__org-name">{organizationName}</span>
        {/* Drawer-only; the desktop sidebar has nothing to close. */}
        <button
          type="button"
          className="hm-icon-button hm-sidebar__close"
          onClick={onDismiss}
          aria-label={t("chat.sidebar.close")}
        >
          <XIcon size={18} strokeWidth={1.85} />
        </button>
      </div>

      <div className="hm-sidebar__nav">
        <section className="hm-sidebar__section" aria-labelledby="hm-channels-title">
          <div className="hm-sidebar__section-header">
            <h2 className="hm-sidebar__section-title" id="hm-channels-title">
              {t("chat.sidebar.channels")}
            </h2>
            <button
              type="button"
              className="hm-sidebar__add"
              onClick={onCreateChannel}
              aria-label={t("chat.sidebar.createChannel")}
            >
              <PlusIcon size={15} strokeWidth={2} />
            </button>
          </div>
          <ul className="hm-sidebar__list">
            {rooms.map((channel) => (
              <ConversationRow
                key={channel.id}
                channel={channel}
                label={channelLabel(channel)}
                onDismiss={onDismiss}
              >
                {channel.kind === "private" ? (
                  <LockIcon size={15} strokeWidth={1.85} className="hm-row__icon" />
                ) : (
                  <HashIcon size={15} strokeWidth={1.85} className="hm-row__icon" />
                )}
              </ConversationRow>
            ))}
          </ul>
        </section>

        <section className="hm-sidebar__section" aria-labelledby="hm-dms-title">
          <div className="hm-sidebar__section-header">
            <h2 className="hm-sidebar__section-title" id="hm-dms-title">
              {t("chat.sidebar.directMessages")}
            </h2>
            <button
              type="button"
              className="hm-sidebar__add"
              onClick={onNewDirectMessage}
              aria-label={t("chat.sidebar.newDirectMessage")}
            >
              <PlusIcon size={15} strokeWidth={2} />
            </button>
          </div>
          <ul className="hm-sidebar__list">
            {directMessages.map((channel) => {
              const peer = channel.dm_peer;
              return (
                <ConversationRow
                  key={channel.id}
                  channel={channel}
                  label={channelLabel(channel)}
                  onDismiss={onDismiss}
                >
                  {peer === undefined ? null : (
                    <Avatar
                      userId={peer.id}
                      displayName={peer.display_name}
                      size={26}
                      typeSize={10}
                      presence={peer.presence ?? "offline"}
                      presenceLabel={t(PRESENCE_LABEL_KEY[peer.presence ?? "offline"])}
                    />
                  )}
                </ConversationRow>
              );
            })}
          </ul>
        </section>
      </div>

      <div className="hm-sidebar__footer">
        <Avatar
          userId={currentUser.id}
          displayName={currentUser.display_name}
          size={32}
          typeSize={13}
          presence="online"
          presenceLabel={null}
        />
        <div className="hm-sidebar__me">
          <span className="hm-sidebar__me-name">{currentUser.display_name}</span>
          {/* The visible label that keeps the dot from carrying state alone. */}
          <span className="hm-sidebar__me-presence">{t("chat.presence.online")}</span>
        </div>
        <button
          type="button"
          className="hm-icon-button"
          onClick={onToggleAccountMenu}
          aria-label={t("chat.footer.account")}
        >
          <SettingsIcon size={17} />
        </button>
        {accountMenu}
      </div>
    </nav>
  );
}
