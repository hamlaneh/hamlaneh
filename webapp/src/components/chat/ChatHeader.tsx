import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { formatCount } from "../../chat/format";
import { PRESENCE_LABEL_KEY } from "../../chat/presence";
import type { Channel } from "../../chat/types";
import { EllipsisVerticalIcon, MenuIcon, SearchIcon, UsersIcon } from "../icons";
import { CHANNEL_MENU_ID } from "./plumbing/overlay";

interface ChatHeaderProps {
  channel: Channel | undefined;
  title: string;
  query: string;
  onQueryChange: (query: string) => void;
  onSubmitQuery: () => void;
  searchOpen: boolean;
  onToggleSearch: () => void;
  onOpenDrawer: () => void;
  onToggleChannelMenu: () => void;
  /**
   * Whether the channel-actions trigger exists at all. It renders only where
   * there is something behind it; otherwise it is absent from the DOM — not
   * disabled, and never a popover that exists to say nothing is available
   * (chat-addendum-menu-components -> 03).
   */
  channelActions: boolean;
  channelMenu: ReactNode;
}


/**
 * Channel name, topic, member count and search. Below 900px the same header
 * carries the drawer and search toggles the mobile artboard draws instead of
 * the topic, member count and inline field.
 */
export function ChatHeader({
  channel,
  title,
  query,
  onQueryChange,
  onSubmitQuery,
  searchOpen,
  onToggleSearch,
  onOpenDrawer,
  onToggleChannelMenu,
  channelActions,
  channelMenu,
}: ChatHeaderProps) {
  const { t, i18n } = useTranslation();

  const isDm = channel?.kind === "dm";
  const peer = channel?.dm_peer;
  const memberCount = channel === undefined ? 0 : channel.member_count;
  const subtitle =
    channel === undefined
      ? ""
      : isDm
        ? `${t("chat.header.directMessage")} · ${t(
            PRESENCE_LABEL_KEY[peer?.presence ?? "offline"],
          )}`
        : channel.topic === ""
          ? t("chat.header.noTopic")
          : channel.topic;

  return (
    <header className="hm-chat-header" data-search-open={searchOpen}>
      <button
        type="button"
        className="hm-icon-button hm-chat-header__drawer-toggle"
        onClick={onOpenDrawer}
        aria-label={t("chat.header.openChannels")}
      >
        <MenuIcon size={20} strokeWidth={1.85} />
      </button>

      <div className="hm-chat-header__title">
        {/* A channel name is an LTR run even in Persian; a person's name is not. */}
        <h1 className="hm-chat-header__name" dir={isDm ? "auto" : "ltr"}>
          {title}
        </h1>
        <p className="hm-chat-header__topic">{subtitle}</p>
        <p className="hm-chat-header__mobile-meta">
          {t("chat.header.memberCount", { count: formatCount(memberCount, i18n.language) })}
        </p>
      </div>

      {channel === undefined ? null : (
        <p className="hm-chat-header__members">
          <UsersIcon size={16} />
          <span>{formatCount(memberCount, i18n.language)}</span>
          <span className="hm-visually-hidden">{t("chat.header.membersLabel")}</span>
        </p>
      )}

      <form
        className="hm-chat-header__search"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          onSubmitQuery();
        }}
      >
        <span className="hm-chat-header__search-icon">
          <SearchIcon size={16} />
        </span>
        <input
          className="hm-search-input"
          type="search"
          value={query}
          onChange={(event) => {
            onQueryChange(event.target.value);
          }}
          placeholder={t("chat.header.searchPlaceholder")}
          aria-label={t("chat.header.searchPlaceholder")}
        />
      </form>

      <button
        type="button"
        className="hm-icon-button hm-chat-header__search-toggle"
        onClick={onToggleSearch}
        aria-label={t("chat.header.search")}
      >
        <SearchIcon size={19} strokeWidth={1.85} />
      </button>

      {channelActions ? (
        <button
          type="button"
          className="hm-icon-button hm-chat-header__actions"
          onClick={onToggleChannelMenu}
          aria-label={t("chat.header.channelActions")}
          aria-haspopup="dialog"
          aria-expanded={channelMenu !== null}
          aria-controls={CHANNEL_MENU_ID}
        >
          <EllipsisVerticalIcon size={18} strokeWidth={2} />
        </button>
      ) : null}
      {channelMenu}
    </header>
  );
}
