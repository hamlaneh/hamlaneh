import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";

import { isolateAuto, isolateLtr } from "../../i18n/bidi";
import type { MediaConnect } from "../../calls/media";
import { useCallSession } from "../../calls/useCallSession";
import { PRESENCE_LABEL_KEY } from "../../chat/presence";
import type { SearchKind } from "../../chat/store";
import type { Channel, Presence, User, UserSummary } from "../../chat/types";
import { useChat } from "../../chat/useChat";
import type { RealtimeOverrides } from "../../chat/useChat";
import { isUuid } from "../../chat/uuid";
import { useInstance } from "../../instance/instanceInfo";
import { MessageBodyProvider } from "../../mls/MessageBodyContext";
import { needsAttention } from "../../mls/types";
import { useMls } from "../../mls/useMls";
import { CallRing } from "../calls/CallRing";
import type { AwayCall } from "../calls/CallStrip";
import { CallStrip } from "../calls/CallStrip";
import { CallView } from "../calls/CallView";
import { ChatHeader } from "./ChatHeader";
import { Composer } from "./Composer";
import { ConnectionBanner } from "./ConnectionBanner";
import { E2eeNotice } from "./E2eeNotice";
import { EmptyChannel } from "./EmptyChannel";
import { MessageList } from "./MessageList";
import { SearchResultsPanel } from "./SearchResultsPanel";
import { Sidebar } from "./Sidebar";
import { AccountMenu } from "./plumbing/AccountMenu";
import { ChannelMenu } from "./plumbing/ChannelMenu";
import { CreateChannelDialog } from "./plumbing/CreateChannelDialog";
import { PeoplePicker } from "./plumbing/PeoplePicker";
import { VerificationSheet, VerificationWarning } from "./plumbing/Verification";
import { SettingsPanel } from "../settings/SettingsPanel";

export interface ChatShellProps {
  currentUser: User;
  organizationName?: string | undefined;
  onLogout: () => void;
  /** Test seam only — production leaves the realtime client on its defaults. */
  realtime?: RealtimeOverrides;
  /** Test seam only — production connects with `livekit-client`. */
  media?: MediaConnect;
}

type Overlay = "none" | "createChannel" | "invite" | "newDm" | "account" | "channelMenu";

function summarize(user: User): UserSummary {
  return { id: user.id, username: user.username, display_name: user.display_name };
}

/** What a conversation is called: the other person's name, or "#slug". */
function conversationTitle(channel: Channel | undefined): string {
  if (channel === undefined) {
    return "";
  }
  return channel.kind === "dm" ? (channel.dm_peer?.display_name ?? "") : `#${channel.slug ?? ""}`;
}

/**
 * The same title, isolated for interpolation into a sentence: a slug is an LTR
 * run, a person's name follows its own script.
 */
function isolatedTitle(channel: Channel | undefined): string {
  const title = conversationTitle(channel);
  return channel?.kind === "dm" ? isolateAuto(title) : isolateLtr(title);
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
  media,
}: ChatShellProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const params = useParams<{ channelId?: string; messageId?: string }>();
  const { info, loaded } = useInstance();
  /**
   * No media server, no call controls — the discipline `password_reset_available`
   * and `sso.enabled` already follow, rather than offering a door that goes
   * nowhere (openapi.yaml, InstanceInfo.calls).
   */
  const callsEnabled = loaded && info.calls === true;

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

  // Constructed for every session, but inert until an encrypted conversation
  // is opened: no wasm is fetched and no keystore is touched before then.
  const mls = useMls(currentUser.id);

  const chat = useChat({
    currentUser: me,
    channelId: params.channelId,
    focusMessageId,
    callsEnabled,
    mls,
    ...(realtime === undefined ? {} : { realtime }),
  });

  const call = useCallSession(media);

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

  const channelTitle = conversationTitle(activeChannel);
  /** Interpolated into "Message {{target}}". */
  const composerTarget = isolatedTitle(activeChannel);

  /* WHERE THE CALL SURFACE BELONGS.
   *
   * The call takes the upper region of its own conversation's pane, with the
   * messages continuing beneath it — a sibling of the message column, never an
   * overlay (CALLS_HANDOFF.md, "Leave, and where the call lives"). Reading
   * another channel does not end the session: the call collapses to the banner
   * strip carrying "Return to call", and that is the whole reason it is not an
   * overlay — a call somebody navigated away from must still be leavable, and
   * the way back is the only route to Leave.
   *
   * A call that has ended or failed has no conversation left to belong to: the
   * session drops its target the moment it does. Its last word is drawn
   * wherever the reader is, because acknowledging it is the whole interaction
   * and a sentence waiting on a channel nobody is looking at is not one. */
  const callTarget = call.target;
  const callChannel = state.channels.find((channel) => channel.id === callTarget);
  const inCall = callsEnabled && call.status !== "idle";
  const callOver = callsEnabled && call.status === "idle" && call.errorKey !== null;
  /** The call is in the conversation being read, so it takes the pane's top. */
  const callHere = (inCall && callTarget === params.channelId) || callOver;
  /** Still connected and no longer on screen: the strip becomes the way back. */
  const away: AwayCall | undefined =
    inCall && !callHere && callTarget !== null
      ? {
          title: isolatedTitle(callChannel),
          onReturn: () => {
            void navigate(`/c/${callTarget}`);
          },
        }
      : undefined;

  const ring = state.ring;
  const ringChannel = state.channels.find((channel) => channel.id === ring?.channelId);
  const ringCaller =
    ring?.from?.display_name ?? ringChannel?.dm_peer?.display_name ?? t("calls.ring.unknown");

  /* The DM picker's encryption choice. It lives here rather than inside the
   * picker so closing and reopening starts from the default rather than from
   * whatever the last attempt left behind. */
  const [newDmEncrypted, setNewDmEncrypted] = useState(false);

  const closeOverlay = () => {
    setOverlay("none");
    setNewDmEncrypted(false);
  };

  const runSearch = (kind: SearchKind) => {
    chat.runSearch(query, kind);
  };

  /*
   * An encrypted channel whose group is not usable yet must not accept a
   * message: the composer would take text the send path then refuses to put
   * anywhere, and a queued message nobody can read is worse than a disabled
   * field that says why.
   */
  const channelMls = activeChannel === undefined ? undefined : mls.state.channels[activeChannel.id];
  const encryptionNotReady =
    activeChannel?.e2ee === true &&
    (mls.state.device.status === "unavailable" ||
      channelMls === undefined ||
      channelMls.status === "opening" ||
      channelMls.status === "waiting" ||
      channelMls.status === "failed");

  /*
   * The second, independent reason a composer can be withheld (ADR 008). It is
   * NOT folded into `encryptionNotReady`: that one says the group is not usable
   * and the answer is to wait, while this one says the group is perfectly
   * usable and holds a key nobody here has accepted — and the answer is a
   * decision only a human can make. A conversation can be `ready` and blocked
   * at the same time, and the composer is replaced rather than merely disabled,
   * because there is something to do here and waiting is not it.
   */
  const channelVerification =
    activeChannel === undefined ? undefined : mls.state.verification[activeChannel.id];
  const verificationBlocked = activeChannel?.e2ee === true && needsAttention(channelVerification);

  /* The per-person sheet, opened from the warning. Null when closed. */
  const [verifyFor, setVerifyFor] = useState<string | null>(null);

  return (
    <MessageBodyProvider resolve={mls.bodyOf}>
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

        {/* The strip is for people not looking at the call: it is absent once
            this channel's call is the one drawn below, and it takes the
            collapsed form once the call is somewhere else. It outlives
            `activeChannel` in that second case, because a channel that has
            gone from the list is exactly when being stranded in an invisible
            call would be worst. */}
        {callsEnabled && !callHere && (activeChannel !== undefined || away !== undefined) ? (
          <CallStrip
            call={activeChannel === undefined ? undefined : state.calls[activeChannel.id]}
            busy={call.status === "connecting"}
            onJoin={() => {
              if (activeChannel !== undefined) {
                call.join(activeChannel.id);
              }
            }}
            away={away}
          />
        ) : null}

        {/* The call's own conversation, so the call sits above it and the
            messages carry on beneath — nothing is hidden behind a call, and
            people talk and type at once. */}
        {callHere ? (
          <CallView
            channelTitle={isolatedTitle(callChannel)}
            status={call.status}
            participants={call.participants}
            micEnabled={call.micEnabled}
            cameraEnabled={call.cameraEnabled}
            screenSharing={call.screenSharing}
            errorKey={call.errorKey}
            onToggleMicrophone={call.toggleMicrophone}
            onToggleCamera={call.toggleCamera}
            onToggleScreenShare={call.toggleScreenShare}
            onLeave={call.leave}
          />
        ) : null}

        {activeChannel === undefined ? null : (
          <E2eeNotice
            channel={activeChannel}
            device={mls.state.device}
            channelState={channelMls}
            resolveName={chat.resolveMention}
          />
        )}

        {activeChannel === undefined ? (
          <div className="hm-messages">
            {/* Four states, four sentences. Each of the last three used to
                fall into the empty-account invitation, which told someone
                with twenty channels that they had none.

                An outage is not an empty account, and no artboard draws a
                control to retry from, so the honest exit there is the reload
                the composer already asks for when its connection is gone.

                A URL naming a channel this reader cannot see is not an empty
                account either — it is a stale permalink or a revoked
                invitation. Its sentence says the conversation is unavailable
                *to them* and points at the list, which commits to nothing
                about whether the id names anything at all: a channel that
                exists and one that never did answer identically everywhere
                else, and the shell must not be the place that separates
                them. */}
            <p className="hm-empty__body">
              {state.channelsStatus === "loading"
                ? t("common.loading")
                : state.channelsStatus === "error"
                  ? t("chat.conversationsFailed")
                  : params.channelId !== undefined
                    ? t("chat.conversationUnavailable")
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

        {activeChannel === undefined ? null : verificationBlocked ? (
          /* Replaced, not disabled: reading and receiving carry on normally,
             and there is exactly one way back — a decision about the keys. */
          <VerificationWarning
            changed={(channelVerification?.changed ?? []).filter(
              (member) => member.userId !== currentUser.id,
            )}
            uncoveredLeaves={channelVerification?.uncoveredLeaves ?? 0}
            own={mls.state.ownDevices}
            resolveName={chat.resolveMention}
            onCompare={setVerifyFor}
            onAccept={(userId) => {
              void mls.acceptPeer(userId);
            }}
            onAcceptOwn={() => {
              void mls.acceptOwnDevices();
            }}
          />
        ) : (
          <Composer
            channelId={activeChannel.id}
            target={composerTarget}
            disabled={disconnected || encryptionNotReady}
            disabledReason={
              givenUp
                ? t("chat.composer.closed")
                : disconnected
                  ? t("chat.composer.disconnected")
                  : encryptionNotReady
                    ? t("chat.e2ee.composerNotReady")
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
          onCreate={async (slug, kind, e2ee) => {
            const channel = await chat.createChannel(slug, kind, e2ee);
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
          encryption={{ checked: newDmEncrypted, onChange: setNewDmEncrypted }}
          onPick={async (user) => {
            const channel = await chat.openDirectMessage(user.id, newDmEncrypted);
            if (channel === null) {
              return false;
            }
            await navigate(`/c/${channel.id}`);
            return true;
          }}
          onClose={closeOverlay}
        />
      ) : null}

      {verifyFor === null ? null : (
        <VerificationSheet
          /* Remounted per person, so the number on screen can never be the
             previous person's while the new one is still being worked out. */
          key={verifyFor}
          userId={verifyFor}
          name={chat.resolveMention(verifyFor) ?? verifyFor}
          level={mls.state.records[verifyFor]?.level ?? null}
          safetyNumberFor={mls.safetyNumberFor}
          onVerify={(userId) => {
            void mls.verifyPeer(userId);
          }}
          onAccept={(userId) => {
            void mls.acceptPeer(userId);
          }}
          onClose={() => {
            setVerifyFor(null);
          }}
        />
      )}
      </div>

      {/* Outside the chat container, so a ring is still answerable while the
          settings panel has the chat behind it inert. */}
      {callsEnabled && ring !== null ? (
        <CallRing
          callerName={ringCaller}
          onAccept={() => {
            chat.dismissRing();
            void navigate(`/c/${ring.channelId}`);
            call.join(ring.channelId);
          }}
          onDismiss={chat.dismissRing}
        />
      ) : null}

      {settingsOpen ? (
        <SettingsPanel
          restoreFocusRef={settingsButtonRef}
          onClose={() => {
            setSettingsOpen(false);
          }}
        />
      ) : null}
    </MessageBodyProvider>
  );
}
