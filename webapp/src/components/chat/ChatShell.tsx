import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";

import { isolateAuto, isolateLtr } from "../../chat/format";
import { PRESENCE_LABEL_KEY } from "../../chat/presence";
import type { SearchKind } from "../../chat/store";
import type { Presence, User, UserSummary } from "../../chat/types";
import { useChat } from "../../chat/useChat";
import type { RealtimeOverrides } from "../../chat/useChat";
import { isUuid } from "../../chat/uuid";
import { ChatHeader } from "./ChatHeader";
import { Composer } from "./Composer";
import { ConnectionBanner } from "./ConnectionBanner";
import { EmptyChannel } from "./EmptyChannel";
import { MessageList } from "./MessageList";
import { SearchResultsPanel } from "./SearchResultsPanel";
import { Sidebar } from "./Sidebar";
import { AccountMenu } from "./plumbing/AccountMenu";
import { ChannelMenu } from "./plumbing/ChannelMenu";
import { CreateChannelDialog } from "./plumbing/CreateChannelDialog";
import { PeoplePicker } from "./plumbing/PeoplePicker";
import { SettingsPanel } from "../settings/SettingsPanel";

export interface ChatShellProps {
  currentUser: User;
  organizationName?: string | undefined;
  onLogout: () => void;
  /** Test seam only — production leaves the realtime client on its defaults. */
  realtime?: RealtimeOverrides;
}

type Overlay = "none" | "createChannel" | "invite" | "newDm" | "account" | "channelMenu";

function summarize(user: User): UserSummary {
  return { id: user.id, username: user.username, display_name: user.display_name };
}

/**
 * The chat shell: sidebar, channel header, message list, composer, connection
 * banner and the conditional search column — the nine artboards, in one
 * component tree that mirrors from `dir` alone.
 */
export function ChatShell({
  currentUser,
  organizationName,
  onLogout,
  realtime,
}: ChatShellProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const params = useParams<{ channelId?: string; messageId?: string }>();

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [mobileSearchOpen, setMobileSearchOpen] = useState(false);
  const [overlay, setOverlay] = useState<Overlay>("none");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [query, setQuery] = useState("");
  // Settings float over the chat and hand focus back to the gear on Escape.
  const settingsButtonRef = useRef<HTMLButtonElement>(null);

  const me = useMemo(() => summarize(currentUser), [currentUser]);

  /* The permalink id comes from the address bar. It reaches a request path and
   * a DOM lookup, so anything that is not a uuid is discarded here rather than
   * carried into either. */
  const focusMessageId =
    params.messageId !== undefined && isUuid(params.messageId) ? params.messageId : undefined;

  const chat = useChat({
    currentUser: me,
    channelId: params.channelId,
    focusMessageId,
    ...(realtime === undefined ? {} : { realtime }),
  });

  const { state, activeChannel, view, markRead } = chat;

  /* Landing on "/" opens the first conversation, which is what the sidebar's
   * own order says is first. */
  useEffect(() => {
    if (params.channelId !== undefined || state.channelsStatus !== "ready") {
      return;
    }
    const first = state.channels[0];
    if (first !== undefined) {
      void navigate(`/c/${first.id}`, { replace: true });
    }
  }, [params.channelId, state.channelsStatus, state.channels, navigate]);

  /* A revoked session cannot be retried (ws-protocol.md §7) — go back to
   * sign-in rather than sitting on a dead shell. */
  useEffect(() => {
    if (state.connection.status === "closed" && state.connection.reason === "revoked") {
      onLogout();
    }
  }, [state.connection, onLogout]);

  /* Reading a channel while it is open keeps the read position current; the
   * divider itself stays where entry put it. */
  const newestId = view.messages.at(-1)?.id ?? null;
  const lastMarked = useRef<string | null>(null);
  useEffect(() => {
    if (newestId !== null && newestId !== lastMarked.current) {
      lastMarked.current = newestId;
      markRead();
    }
  }, [newestId, markRead]);

  const connection = state.connection;
  const disconnected =
    connection.status === "offline" ||
    connection.status === "reconnecting" ||
    connection.status === "closed";
  // No further attempt is scheduled, so nothing is "waiting for the
  // connection to return" — the composer has to say something else.
  const givenUp = connection.status === "closed";
  // The only presence this client can honestly assert about its own user is
  // whether its socket is up. Away is a server signal that does not exist yet.
  const myPresence: Presence = connection.status === "online" ? "online" : "offline";

  const isDm = activeChannel?.kind === "dm";
  const channelTitle =
    activeChannel === undefined
      ? ""
      : isDm
        ? (activeChannel.dm_peer?.display_name ?? "")
        : `#${activeChannel.slug ?? ""}`;
  // Interpolated into "Message {{target}}": a slug is an LTR run, a person's
  // name follows its own script.
  const composerTarget = isDm ? isolateAuto(channelTitle) : isolateLtr(channelTitle);

  const closeOverlay = () => {
    setOverlay("none");
  };

  const runSearch = (kind: SearchKind) => {
    chat.runSearch(query, kind);
  };

  return (
    <>
      {/* The chat behind the panel is inert, not merely dimmed — the settings
          handoff's accessibility note. */}
      <div className="hm-chat" inert={settingsOpen}>
      <Sidebar
        channels={state.channels}
        currentUser={currentUser}
        presence={myPresence}
        presenceLabel={t(PRESENCE_LABEL_KEY[myPresence])}
        organizationName={organizationName ?? t("app.name")}
        open={drawerOpen}
        onDismiss={() => {
          setDrawerOpen(false);
        }}
        onCreateChannel={() => {
          setOverlay("createChannel");
        }}
        onNewDirectMessage={() => {
          setOverlay("newDm");
        }}
        onToggleAccountMenu={() => {
          setOverlay(overlay === "account" ? "none" : "account");
        }}
        onOpenSettings={() => {
          closeOverlay();
          setSettingsOpen(true);
        }}
        settingsButtonRef={settingsButtonRef}
        accountMenu={
          overlay === "account" ? (
            <AccountMenu user={currentUser} onLogout={onLogout} onClose={closeOverlay} />
          ) : null
        }
      />

      {drawerOpen ? (
        <button
          type="button"
          className="hm-chat__scrim"
          aria-label={t("chat.sidebar.close")}
          onClick={() => {
            setDrawerOpen(false);
          }}
        />
      ) : null}

      <main className="hm-chat__main">
        <ChatHeader
          channel={activeChannel}
          title={channelTitle}
          query={query}
          onQueryChange={setQuery}
          onSubmitQuery={() => {
            runSearch(state.search.status === "closed" ? "messages" : state.search.kind);
          }}
          searchOpen={mobileSearchOpen}
          onToggleSearch={() => {
            setMobileSearchOpen((open) => !open);
          }}
          onOpenDrawer={() => {
            setDrawerOpen(true);
          }}
          onToggleChannelMenu={() => {
            setOverlay(overlay === "channelMenu" ? "none" : "channelMenu");
          }}
          channelMenu={
            overlay === "channelMenu" && activeChannel !== undefined ? (
              <ChannelMenu
                channel={activeChannel}
                onInvite={() => {
                  setOverlay("invite");
                }}
                onSetTopic={chat.setTopic}
                onClose={closeOverlay}
              />
            ) : null
          }
        />

        <ConnectionBanner
          connection={connection}
          justReconnected={state.justReconnected}
          onSettled={chat.settleConnection}
        />

        {activeChannel === undefined ? (
          <div className="hm-messages">
            <p className="hm-empty__body">
              {state.channelsStatus === "loading"
                ? t("common.loading")
                : t("chat.noConversations")}
            </p>
          </div>
        ) : view.status === "ready" &&
          view.messages.length === 0 &&
          view.pending.length === 0 ? (
          <div className="hm-messages">
            <EmptyChannel
              channel={activeChannel}
              createdByYou={activeChannel.created_by === currentUser.id}
              onInvite={() => {
                setOverlay("invite");
              }}
              onSetTopic={() => {
                setOverlay("channelMenu");
              }}
            />
          </div>
        ) : (
          <MessageList
            channel={activeChannel}
            channelId={activeChannel.id}
            messages={view.messages}
            pending={view.pending}
            currentUser={me}
            canModerate={currentUser.is_admin}
            resolveMention={chat.resolveMention}
            loading={view.status === "loading"}
            loadingOlder={view.loadingOlder}
            hasMoreOlder={view.beforeCursor !== null}
            dividerBeforeId={view.dividerBeforeId}
            focusMessageId={view.focusMessageId}
            dimmed={disconnected}
            onLoadOlder={chat.loadOlder}
            onEdit={chat.editMessage}
            onDelete={chat.deleteMessage}
          />
        )}

        {activeChannel === undefined ? null : (
          <Composer
            channelId={activeChannel.id}
            target={composerTarget}
            disabled={disconnected}
            disabledReason={
              givenUp
                ? t("chat.composer.closed")
                : disconnected
                  ? t("chat.composer.disconnected")
                  : null
            }
            onSend={chat.sendMessage}
          />
        )}
      </main>

      <SearchResultsPanel
        search={state.search}
        onClose={() => {
          chat.closeSearch();
          setMobileSearchOpen(false);
        }}
        onSelectKind={runSearch}
      />

      {overlay === "createChannel" ? (
        <CreateChannelDialog
          onCreate={async (slug, kind) => {
            const channel = await chat.createChannel(slug, kind);
            if (channel === null) {
              return false;
            }
            await navigate(`/c/${channel.id}`);
            return true;
          }}
          onClose={closeOverlay}
        />
      ) : null}

      {overlay === "invite" ? (
        <PeoplePicker
          title={t("chat.empty.invite")}
          actionLabel={t("chat.people.invite")}
          onPick={(user) => chat.inviteMember(user.id)}
          onClose={closeOverlay}
        />
      ) : null}

      {overlay === "newDm" ? (
        <PeoplePicker
          title={t("chat.sidebar.newDirectMessage")}
          actionLabel={t("chat.people.message")}
          onPick={async (user) => {
            const channel = await chat.openDirectMessage(user.id);
            if (channel === null) {
              return false;
            }
            await navigate(`/c/${channel.id}`);
            return true;
          }}
          onClose={closeOverlay}
        />
      ) : null}
      </div>

      {settingsOpen ? (
        <SettingsPanel
          restoreFocusRef={settingsButtonRef}
          onClose={() => {
            setSettingsOpen(false);
          }}
        />
      ) : null}
    </>
  );
}
