import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";

import { api } from "../api/client";
import type { MlsController } from "../mls/useMls";
import { fullJitterDelay, RealtimeClient, realtimeUrl } from "./realtime";
import type { RealtimeOptions, ServerFrame } from "./realtime";
import { chatReducer, emptyView, initialChatState } from "./store";
import type { ChannelView, ChatState, SearchKind } from "./store";
import type { Attachment, Channel, Message, UserSummary } from "./types";
import { newUuid } from "./uuid";

/** Test seam: production supplies neither, and gets the real socket and backoff. */
export type RealtimeOverrides = Partial<
  Pick<RealtimeOptions, "url" | "socketFactory" | "retryDelayMs">
>;

const HISTORY_PAGE_SIZE = 50;

/**
 * How soon an encrypted channel that could not finish its group setup asks
 * again, and how far apart those asks are allowed to drift.
 *
 * There is no event for the thing it is waiting on. A member who has never
 * opened the app has published no key packages, so the client bootstrapping
 * the group cannot add them — and nothing in the protocol tells the group
 * later that a device has appeared. Without this, the first person into a
 * channel creates a group of one and everybody invited before their first
 * sign-in stays outside it forever, which is exactly what the real stack did.
 * Five seconds is a conversation someone is looking at.
 *
 * The cap is what keeps that first number honest at the other end of the
 * scale. The wait has no deadline — one invited person who never signs in
 * holds a channel `incomplete` until they do, which can be weeks — and each
 * ask costs every online member's client a paged member-device read plus a
 * key-package claim per missing user. At five seconds flat that is a fixed
 * per-member load a single unanswered invitation can impose indefinitely, so
 * the interval doubles into a ceiling of a few minutes rather than staying
 * where a human's attention span put it.
 */
const MLS_RETRY_MS = 5_000;
const MLS_RETRY_CAP_MS = 180_000;
const SEARCH_PAGE_SIZE = 20;

export interface ChatController {
  state: ChatState;
  activeChannel: Channel | undefined;
  view: ChannelView;
  /** Everyone the shell can name: authors, members, DM peers and the caller. */
  resolveMention: (userId: string) => string | null;
  /** `content` may be empty exactly when `attachments` is not (openapi.yaml). */
  sendMessage: (content: string, attachments?: Attachment[]) => void;
  editMessage: (messageId: string, content: string) => Promise<boolean>;
  deleteMessage: (messageId: string) => Promise<boolean>;
  loadOlder: () => void;
  markRead: () => void;
  runSearch: (query: string, kind: SearchKind) => void;
  closeSearch: () => void;
  /** Dismisses the "Back online" banner once its window has elapsed. */
  settleConnection: () => void;
  /** `e2ee` is fixed at creation and never toggled (openapi.yaml). */
  createChannel: (
    slug: string,
    kind: "public" | "private",
    e2ee: boolean,
  ) => Promise<Channel | null>;
  /**
   * `e2ee` applies only when this opens a DM that did not exist; an existing
   * one comes back as it is, whatever is passed (openapi.yaml).
   */
  openDirectMessage: (userId: string, e2ee: boolean) => Promise<Channel | null>;
  inviteMember: (userId: string) => Promise<boolean>;
  setTopic: (topic: string) => Promise<boolean>;
  /** Closes the DM ring toast, and that is all it does (ADR 005). */
  dismissRing: () => void;
}

interface UseChatInput {
  currentUser: UserSummary;
  channelId: string | undefined;
  focusMessageId: string | undefined;
  /** `instance.calls` — with no media server there is nothing to reconcile. */
  callsEnabled: boolean;
  /**
   * The encryption layer. Plaintext channels never touch it; an e2ee channel
   * routes its send, its history and its membership events through it.
   */
  mls: MlsController;
  realtime?: RealtimeOverrides;
}

/**
 * Keeps a ref pointing at the newest value without writing it during render.
 * Long-lived subscribers (the socket, an in-flight request) read through this
 * instead of closing over a snapshot, so they never need to be torn down and
 * rebuilt just because state moved.
 */
function useLatest<T>(value: T) {
  const ref = useRef(value);
  useEffect(() => {
    ref.current = value;
  });
  return ref;
}

/**
 * Everything the chat shell does that is not rendering: history paging,
 * optimistic send with idempotent retry, read positions, search, and the
 * realtime socket that delivers other people's messages.
 */
export function useChat({
  currentUser,
  channelId,
  focusMessageId,
  callsEnabled,
  mls,
  realtime,
}: UseChatInput): ChatController {
  const [state, dispatch] = useReducer(chatReducer, initialChatState);

  // Effects read the latest state without re-subscribing on every change.
  const stateRef = useLatest(state);
  const currentUserId = currentUser.id;

  const view = state.views[channelId ?? ""] ?? emptyView();
  const activeChannel = state.channels.find((channel) => channel.id === channelId);

  /* ── history ────────────────────────────────────────────────────── */

  const loadHistory = useCallback(
    async (
      target: string,
      options: { around?: string; before?: string; after?: string; mode: "replace" | "older" | "newer" },
    ) => {
      dispatch({ type: "history/start", channelId: target, older: options.mode === "older" });
      try {
        const { data } = await api.GET("/api/v1/channels/{channelId}/messages", {
          params: {
            path: { channelId: target },
            query: {
              limit: HISTORY_PAGE_SIZE,
              ...(options.around === undefined ? {} : { around: options.around }),
              ...(options.before === undefined ? {} : { before: options.before }),
              ...(options.after === undefined ? {} : { after: options.after }),
            },
          },
        });
        if (data === undefined) {
          dispatch({ type: "history/failed", channelId: target });
          return null;
        }
        dispatch({
          type: "history/loaded",
          channelId: target,
          page: data,
          mode: options.mode,
          currentUserId,
          ...(options.mode === "replace"
            ? { focusMessageId: options.around ?? null }
            : {}),
        });
        return data;
      } catch (error) {
        console.warn("Could not load message history:", error);
        dispatch({ type: "history/failed", channelId: target });
        return null;
      }
    },
    [currentUserId],
  );

  const markReadFor = useCallback(async (target: string, messageId: string) => {
    try {
      await api.PUT("/api/v1/channels/{channelId}/read", {
        params: { path: { channelId: target } },
        body: { message_id: messageId },
      });
    } catch (error) {
      // A lost read position only means the divider reappears next visit.
      console.warn("Could not store the read position:", error);
    }
    dispatch({ type: "read/mark", channelId: target, messageId });
  }, []);

  /**
   * ws-protocol.md §5: a channel the replay buffer could not cover is
   * backfilled over REST until the page stops offering an after_cursor. This
   * is the normal path after any long disconnect, not an error path.
   */
  const backfill = useCallback(
    async (target: string) => {
      const start = stateRef.current.views[target]?.afterCursor ?? null;
      if (start === null) {
        await loadHistory(target, { mode: "replace" });
        return;
      }
      // Bounded so a server that keeps handing back a cursor cannot spin here.
      let cursor = start;
      for (let page = 0; page < 20; page += 1) {
        const loaded = await loadHistory(target, { after: cursor, mode: "newer" });
        const next = loaded?.after_cursor ?? null;
        if (next === null || next === cursor) {
          break;
        }
        cursor = next;
      }
    },
    [loadHistory, stateRef],
  );

  /* ── calls ──────────────────────────────────────────────────────── */

  /**
   * Reads what is actually happening in a channel's call.
   *
   * ws-protocol.md §5: `call_started`, `call_updated` and `call_ended` carry
   * no sequence number and are never replayed, so a client that trusted them
   * would paint a banner for a call that ended while it was disconnected. The
   * events say something changed; this says what is true.
   */
  const reconcileCall = useCallback(async (target: string) => {
    try {
      const { data } = await api.GET("/api/v1/channels/{channelId}/call", {
        params: { path: { channelId: target } },
      });
      if (data !== undefined) {
        dispatch({ type: "call/state", channelId: target, call: data });
      }
    } catch (error) {
      // Unreadable: the previous answer stands rather than being replaced by
      // a guess in either direction.
      console.warn("Could not read the channel's call state:", error);
    }
  }, []);

  /**
   * The reconciliation points, and there are exactly two: opening a channel,
   * and coming back from a disconnect. Both are this one effect — it re-runs
   * when the open channel changes, and again when the socket returns to
   * `online`, which is precisely "after a reconnect".
   *
   * Gated on `online` rather than firing regardless, because a client that
   * cannot hear `call_ended` cannot keep a banner honest either: the answer
   * would be stale from the moment it arrived.
   */
  const connectionStatus = state.connection.status;
  useEffect(() => {
    if (!callsEnabled || channelId === undefined || connectionStatus !== "online") {
      return;
    }
    void reconcileCall(channelId);
  }, [callsEnabled, channelId, connectionStatus, reconcileCall]);

  const dismissRing = useCallback(() => {
    dispatch({ type: "call/dismissRing" });
  }, []);

  /* ── realtime ───────────────────────────────────────────────────── */

  const clientRef = useRef<RealtimeClient | null>(null);
  const backfillRef = useLatest(backfill);

  const mlsRef = useLatest(mls);
  const channelIdRef = useLatest(channelId);

  /**
   * Counts the arrivals that are a fresh reason to re-drive the open channel's
   * group setup, rather than the retry timer merely expiring again. The retry
   * effect keys on it, so a nudge sends the backoff back to its floor.
   *
   * Scoped to the open channel, which is the only one that effect watches: an
   * unscoped counter would let a busy channel's commits reset a different
   * channel's backoff, and a session with one chatty conversation would then
   * poll every stuck one at the floor forever — the flat interval this
   * replaced, reached by a longer road.
   */
  const [mlsRetryNudge, setMlsRetryNudge] = useState(0);
  const nudgeMlsRetry = useCallback(
    (target: string) => {
      if (target === channelIdRef.current) {
        setMlsRetryNudge((count) => count + 1);
      }
    },
    [channelIdRef],
  );

  /** True only for channels the server has told us are encrypted. */
  const isE2ee = useCallback(
    (target: string) =>
      stateRef.current.channels.find((channel) => channel.id === target)?.e2ee === true,
    [stateRef],
  );

  const handleFrame = useCallback(
    (frame: ServerFrame) => {
      switch (frame.type) {
        case "message_created":
        case "message_updated":
        case "message_deleted":
          dispatch({
            type: "message/upsert",
            channelId: frame.chan,
            message: frame.message,
            currentUserId,
            created: frame.type === "message_created",
          });
          break;
        case "channel_created":
        case "channel_updated":
          dispatch({ type: "channel/upsert", channel: frame.channel });
          break;
        case "read_position":
          // Own-device sync: another tab moved the position, so clear here too.
          dispatch({ type: "read/mark", channelId: frame.chan, messageId: frame.messageId });
          break;
        case "presence":
          dispatch({ type: "presence/set", userId: frame.userId, state: frame.state });
          break;
        case "call_started":
          dispatch({
            type: "call/started",
            channelId: frame.chan,
            call: { active: true, participants: frame.participants },
            from: frame.startedBy,
            currentUserId,
          });
          break;
        case "call_updated":
          dispatch({
            type: "call/state",
            channelId: frame.chan,
            call: { active: true, participants: frame.participants },
          });
          break;
        case "call_ended":
          // `active: false` and nothing else — the contract says the other
          // fields are absent rather than stale.
          dispatch({ type: "call/state", channelId: frame.chan, call: { active: false } });
          break;
        case "mls_commit":
          // A nudge, not the truth: the commit itself comes over REST.
          mlsRef.current.syncChannel(frame.chan);
          nudgeMlsRetry(frame.chan);
          break;
        case "mls_welcome":
          // Deliberately not a retry nudge. This sync either moves the state —
          // in which case the timer is torn down anyway — or the Welcome named
          // some other device, which is not news about this one being stuck.
          mlsRef.current.syncWelcomes();
          break;
        case "member_added":
          // Somebody joined an encrypted conversation: every member's client
          // races to add their devices, and all but one lose harmlessly.
          if (isE2ee(frame.chan)) {
            mlsRef.current.memberAdded(frame.chan);
            nudgeMlsRetry(frame.chan);
          }
          break;
        case "member_removed":
          // The id on the frame is deliberately not passed on: who leaves the
          // tree is the directory's answer, not this frame's claim (ADR 007).
          if (isE2ee(frame.chan)) {
            mlsRef.current.memberRemoved(frame.chan);
            nudgeMlsRetry(frame.chan);
          }
          break;
        default:
          // subscribed / unsubscribed / typing / error carry nothing this
          // slice draws. Typing has a protocol but no designed UI yet
          // (ws-protocol.md §4).
          break;
      }
    },
    [currentUserId, isE2ee, mlsRef, nudgeMlsRetry],
  );

  const handleFrameRef = useLatest(handleFrame);

  const realtimeUrlOverride = realtime?.url;
  const socketFactory = realtime?.socketFactory;
  const retryDelayMs = realtime?.retryDelayMs;

  useEffect(() => {
    const client = new RealtimeClient({
      url: realtimeUrlOverride ?? realtimeUrl(),
      onFrame: (frame) => {
        handleFrameRef.current(frame);
      },
      onStatus: (connection) => {
        dispatch({ type: "connection/set", connection });
      },
      onResync: (target) => {
        void backfillRef.current(target);
      },
      ...(socketFactory === undefined ? {} : { socketFactory }),
      ...(retryDelayMs === undefined ? {} : { retryDelayMs }),
    });
    clientRef.current = client;
    client.connect();
    return () => {
      client.close();
      clientRef.current = null;
    };
  }, [realtimeUrlOverride, socketFactory, retryDelayMs, backfillRef, handleFrameRef]);

  /* ── channel list ───────────────────────────────────────────────── */

  useEffect(() => {
    // A mutable holder rather than a captured boolean: the flag is written by
    // the cleanup and read by the request that outlives it.
    const run = { cancelled: false };
    void (async () => {
      try {
        const { data } = await api.GET("/api/v1/channels", { params: { query: { limit: 200 } } });
        if (run.cancelled) {
          return;
        }
        if (data === undefined) {
          dispatch({ type: "channels/failed" });
          return;
        }
        dispatch({ type: "channels/loaded", channels: data.channels });
      } catch (error) {
        console.warn("Could not load the conversation list:", error);
        if (!run.cancelled) {
          dispatch({ type: "channels/failed" });
        }
      }
    })();
    return () => {
      run.cancelled = true;
    };
  }, []);

  /* ── entering a channel ─────────────────────────────────────────── */

  useEffect(() => {
    if (channelId === undefined) {
      return undefined;
    }
    const client = clientRef.current;
    client?.subscribe(channelId);

    void (async () => {
      const existing = stateRef.current.views[channelId];
      const needsLoad =
        existing === undefined ||
        existing.status === "idle" ||
        existing.status === "error" ||
        (focusMessageId !== undefined && existing.focusMessageId !== focusMessageId);
      let page = null;
      if (needsLoad) {
        page = await loadHistory(channelId, {
          mode: "replace",
          ...(focusMessageId === undefined ? {} : { around: focusMessageId }),
        });
      } else {
        // History is already here, so no `replace` page will arrive to place
        // the unread divider — place it from what is loaded.
        dispatch({ type: "channel/enter", channelId, currentUserId });
      }
      const newest = (page?.messages ?? stateRef.current.views[channelId]?.messages ?? []).at(-1);
      if (newest !== undefined) {
        await markReadFor(channelId, newest.id);
      }
    })();

    return () => {
      client?.unsubscribe(channelId);
      dispatch({ type: "channel/leave", channelId });
    };
  }, [channelId, currentUserId, focusMessageId, loadHistory, markReadFor, stateRef]);

  /*
   * Bootstrapping the channel's group: find or create it, catch up on the
   * commit log, add whoever is missing.
   *
   * Keyed on whether the open channel *is* encrypted, as a value read during
   * render — not on a callback that reads the channel list through a ref. The
   * ref version was a real bug: on a cold load the route names a channel
   * before `GET /channels` has answered, so the check ran against an empty
   * list, said "not encrypted", and never ran again — the callback is stable,
   * so the arrival of the list re-rendered but re-triggered nothing. The
   * screen sat on "setting up encryption" forever with not one MLS request
   * for the channel. As a value, the list arriving is the trigger.
   */
  const activeChannelIsE2ee = activeChannel?.e2ee === true;
  useEffect(() => {
    if (channelId === undefined || !activeChannelIsE2ee) {
      return;
    }
    mlsRef.current.openChannel(channelId);
  }, [activeChannelIsE2ee, channelId, mlsRef]);

  /*
   * The three states that resolve themselves only if somebody asks again.
   *
   * `incomplete` — this client is in the group and could not add everyone.
   * It re-reconciles, which claims key packages for whoever is still outside.
   *
   * `waiting` — the group exists and this device is not in it yet. Only a
   * Welcome can change that, so the Welcome list is what gets re-asked: the
   * `mls_welcome` nudge reaches sockets, and one sent while this client was
   * reconnecting is simply gone. Re-opening the channel would re-read the
   * group and learn nothing, which is a poll that cannot answer its own
   * question.
   *
   * `failed` — the group could not be reached or advanced: a directory page
   * that 5xx'd, a commit-retry budget spent against a member who kept winning
   * the race. Nothing sends in this channel, so leaving it out meant a
   * transient server hiccup was indistinguishable from a permanent one until
   * a commit nudge, a member event, a reconnect or a reopen happened along.
   *
   * `failed` alone is re-driven through openChannel rather than syncChannel,
   * because syncChannel is a no-op for a channel no group is known for — and
   * a directory read that threw before the group was ever read is precisely
   * the failure most worth retrying. Sending is already blocked in this
   * state, so the `opening` it passes through costs nothing.
   *
   * The retry is a growing wait rather than a fixed one because none of the
   * three has a deadline (see MLS_RETRY_CAP_MS), and it starts over — at the
   * floor — whenever something happens that is a genuinely new reason to
   * expect a different answer. That is what the dependency list is: the status
   * moving, the socket changing state, or a nudge naming this channel. Backing
   * off is for a condition that has not changed; a cause that just arrived
   * deserves the first attempt, not the twelfth one's wait.
   *
   * None of the three is polled for its own sake: all stop as soon as the
   * state leaves this set.
   */
  const channelMlsStatus =
    channelId === undefined ? undefined : mls.state.channels[channelId]?.status;
  useEffect(() => {
    if (
      channelId === undefined ||
      (channelMlsStatus !== "incomplete" &&
        channelMlsStatus !== "waiting" &&
        channelMlsStatus !== "failed")
    ) {
      return undefined;
    }
    let attempt = 0;
    let timer: ReturnType<typeof setTimeout> | null = null;
    // A self-rescheduling timeout, not an interval: the gap has to be read
    // fresh each time for it to be able to grow.
    const schedule = () => {
      timer = setTimeout(
        () => {
          if (channelMlsStatus === "waiting") {
            mlsRef.current.syncWelcomes();
          } else if (channelMlsStatus === "failed") {
            // openChannel, not syncChannel: syncChannel is a no-op for a
            // channel the service holds no group for, and the failure that
            // most needs re-driving is exactly that one — a directory read
            // that threw before any group was known. The usual objection to
            // openChannel does not apply here, because it is the composer it
            // would disable and `failed` has already disabled it.
            mlsRef.current.openChannel(channelId);
          } else {
            // Not openChannel: that would go back through `opening`, and an
            // `incomplete` client can already send — briefly disabling its
            // composer every few seconds would be a worse bug than the one
            // being fixed.
            mlsRef.current.syncChannel(channelId);
          }
          attempt += 1;
          schedule();
        },
        fullJitterDelay(attempt, Math.random, MLS_RETRY_MS, MLS_RETRY_CAP_MS),
      );
    };
    schedule();
    return () => {
      if (timer !== null) {
        clearTimeout(timer);
      }
    };
  }, [channelId, channelMlsStatus, connectionStatus, mlsRetryNudge, mlsRef]);

  /*
   * Decryption follows whatever is loaded, rather than being wired into every
   * path that loads something: history pages, live frames and the reconnect
   * backfill all end up in `view.messages`, and asking for the same message
   * twice is free (the service keeps what it has already opened).
   */
  useEffect(() => {
    if (channelId === undefined) {
      return;
    }
    mlsRef.current.decryptAll(channelId, view.messages);
  }, [channelId, mlsRef, view.messages]);

  /*
   * Reconnect: the Welcome list and the commit log are both durable where the
   * replay buffer is not, so a client that was away refetches both rather
   * than trusting that it heard every nudge.
   *
   * Gated on the session having an encrypted conversation at all. A Welcome
   * can only ever name a channel this person is a member of, so the channel
   * list is a complete answer — and without the gate every plaintext-only
   * session would open a keystore and fetch a 480 KB wasm chunk to learn that
   * it had nothing to decrypt. A channel created encrypted while the socket is
   * up arrives as `channel_created` and re-runs this.
   */
  const hasEncryptedChannel = state.channels.some((channel) => channel.e2ee);
  useEffect(() => {
    if (connectionStatus !== "online" || !hasEncryptedChannel) {
      return;
    }
    mlsRef.current.syncWelcomes();
    if (channelId !== undefined && activeChannelIsE2ee) {
      mlsRef.current.syncChannel(channelId);
    }
  }, [activeChannelIsE2ee, channelId, connectionStatus, hasEncryptedChannel, mlsRef]);

  /* ── sending ────────────────────────────────────────────────────── */

  const attemptSend = useCallback(
    async (
      target: string,
      clientMsgId: string,
      content: string,
      attachments: readonly Attachment[],
    ) => {
      dispatch({ type: "pending/sending", channelId: target, clientMsgId });
      try {
        // Encrypted inside the chain, not before it: the ratchet advances per
        // message, so the order messages are sealed in has to be the order
        // they are sent in.
        const encrypted = isE2ee(target) ? await mlsRef.current.encrypt(target, content) : null;
        if (isE2ee(target) && encrypted === null) {
          // No ciphertext, no send. Falling back to plaintext in a channel
          // the user was told is encrypted is the one thing this path must
          // never do; the message stays queued instead.
          dispatch({ type: "pending/queue", channelId: target, clientMsgId });
          return;
        }
        const { data } = await api.POST("/api/v1/channels/{channelId}/messages", {
          params: { path: { channelId: target } },
          // The idempotency key is reused verbatim on every retry: a resend
          // returns the message that already exists instead of a duplicate.
          // The ids are attached atomically with the send, so a message and
          // its files can never half-exist.
          body: {
            client_msg_id: clientMsgId,
            // Empty exactly when the ciphertext is present, so the server
            // stores nothing it can read (openapi.yaml -> SendMessageRequest).
            content: encrypted === null ? content : "",
            ...(encrypted === null ? {} : { mls: encrypted }),
            ...(attachments.length === 0
              ? {}
              : { attachment_ids: attachments.map((entry) => entry.id) }),
          },
        });
        if (data === undefined) {
          dispatch({ type: "pending/queue", channelId: target, clientMsgId });
          return;
        }
        if (encrypted !== null) {
          // MLS gives a sender no way to open its own application message —
          // the ratchet is per-sender — so the plaintext is kept here as the
          // message lands. It lives for this session only: after a reload
          // this device's own history reads as undecryptable, which is
          // honest but not final. A local plaintext store is its own slice.
          mlsRef.current.rememberSent(data.id, content);
        }
        dispatch({
          type: "message/upsert",
          channelId: target,
          message: data,
          currentUserId,
          created: true,
        });
        await markReadFor(target, data.id);
      } catch (error) {
        // Kept, not dropped: the design's dashed "Waiting to send" treatment.
        console.warn("Message send failed; it stays queued:", error);
        dispatch({ type: "pending/queue", channelId: target, clientMsgId });
      }
    },
    [currentUserId, isE2ee, markReadFor, mlsRef],
  );

  /**
   * One promise chain per channel, so a channel's sends leave in the order
   * they were composed.
   *
   * Firing them concurrently was a real bug, found end to end: three messages
   * typed quickly produced three POSTs in flight at once, the server stamped
   * created_at as each arrived, and history came back in an order the author
   * never wrote. Optimistic rendering hid it until a reload. The offline
   * queue made it worse, because that is precisely where several messages
   * pile up before being drained together.
   *
   * Per channel rather than globally: order only means anything inside one
   * conversation, and a slow send should not hold up a different one.
   * attemptSend resolves rather than rejects, but the chain is guarded anyway
   * — one rejection must not strand every later message behind it.
   */
  const sendChains = useRef(new Map<string, Promise<void>>());

  const enqueueSend = useCallback(
    (
      target: string,
      clientMsgId: string,
      content: string,
      attachments: readonly Attachment[],
    ) => {
      const chain = (sendChains.current.get(target) ?? Promise.resolve())
        .catch(() => undefined)
        .then(() => attemptSend(target, clientMsgId, content, attachments));
      sendChains.current.set(target, chain);
    },
    [attemptSend],
  );

  const sendMessage = useCallback(
    (content: string, attachments: Attachment[] = []) => {
      const target = channelId;
      const trimmed = content.trim();
      // Empty content is legal exactly when files ride along — an image with
      // no caption is an ordinary message, a message with neither is nothing
      // (openapi.yaml -> SendMessageRequest.content).
      if (target === undefined || (trimmed === "" && attachments.length === 0)) {
        return;
      }
      const clientMsgId = newUuid();
      dispatch({
        type: "pending/add",
        channelId: target,
        pending: {
          clientMsgId,
          content: trimmed,
          attachments,
          createdAt: new Date().toISOString(),
          status: "sending",
        },
      });
      enqueueSend(target, clientMsgId, trimmed, attachments);
    },
    [enqueueSend, channelId],
  );

  /**
   * Queued messages send themselves again when something that could have
   * blocked them changes.
   *
   * Losing the connection is not the only way to end up queued. A refused
   * request, a transport throw, and an encrypted channel whose group was not
   * usable yet all queue while the socket is perfectly online — and a drain
   * that only fires on an offline→online edge would never look at them again,
   * so the message would sit under "Waiting to send" until a reload dropped
   * it without a word. Retrying when the encryption state moves is what makes
   * the e2ee case recover; retrying while merely online is what makes the
   * other two recover, and a resend is free because `client_msg_id` makes the
   * server's answer a lookup rather than a second message.
   */
  useEffect(() => {
    if (state.connection.status !== "online") {
      return;
    }
    for (const [target, channelView] of Object.entries(stateRef.current.views)) {
      for (const pending of channelView.pending) {
        if (pending.status === "queued") {
          // Through the same chain: a drained queue must land in the order it
          // was composed, which is the whole reason the queue kept the
          // messages rather than dropping them.
          enqueueSend(target, pending.clientMsgId, pending.content, pending.attachments);
        }
      }
    }
  }, [state.connection, channelMlsStatus, enqueueSend, stateRef]);

  /* ── message actions ────────────────────────────────────────────── */

  const editMessage = useCallback(
    async (messageId: string, content: string) => {
      const target = channelId;
      const trimmed = content.trim();
      if (target === undefined || trimmed === "") {
        return false;
      }
      try {
        const encrypted = isE2ee(target) ? await mlsRef.current.encrypt(target, trimmed) : null;
        if (isE2ee(target) && encrypted === null) {
          return false;
        }
        const { data } = await api.PATCH("/api/v1/channels/{channelId}/messages/{messageId}", {
          params: { path: { channelId: target, messageId } },
          body: {
            content: encrypted === null ? trimmed : "",
            ...(encrypted === null ? {} : { mls: encrypted }),
          },
        });
        if (data === undefined) {
          return false;
        }
        if (encrypted !== null) {
          mlsRef.current.rememberSent(data.id, trimmed);
        }
        dispatch({
          type: "message/upsert",
          channelId: target,
          message: data,
          currentUserId,
          created: false,
        });
        return true;
      } catch (error) {
        console.warn("Could not edit the message:", error);
        return false;
      }
    },
    [channelId, currentUserId, isE2ee, mlsRef],
  );

  const deleteMessage = useCallback(
    async (messageId: string) => {
      const target = channelId;
      if (target === undefined) {
        return false;
      }
      const existing = stateRef.current.views[target]?.messages.find(
        (message) => message.id === messageId,
      );
      try {
        const { response } = await api.DELETE(
          "/api/v1/channels/{channelId}/messages/{messageId}",
          { params: { path: { channelId: target, messageId } } },
        );
        if (!response.ok) {
          return false;
        }
      } catch (error) {
        console.warn("Could not delete the message:", error);
        return false;
      }
      if (existing !== undefined) {
        // Soft delete: the row keeps its place with its content erased, which
        // is what the dashed placeholder draws. The message_deleted event
        // confirms the same shape.
        const removed: Message = {
          ...existing,
          content: "",
          deleted_at: new Date().toISOString(),
        };
        dispatch({
          type: "message/upsert",
          channelId: target,
          message: removed,
          currentUserId,
          created: false,
        });
      }
      return true;
    },
    [channelId, currentUserId, stateRef],
  );

  const loadOlder = useCallback(() => {
    const target = channelId;
    if (target === undefined) {
      return;
    }
    const current = stateRef.current.views[target];
    if (current === undefined || current.loadingOlder || current.beforeCursor === null) {
      return;
    }
    void loadHistory(target, { before: current.beforeCursor, mode: "older" });
  }, [channelId, loadHistory, stateRef]);

  const markRead = useCallback(() => {
    const target = channelId;
    if (target === undefined) {
      return;
    }
    const newest = stateRef.current.views[target]?.messages.at(-1);
    if (newest !== undefined) {
      void markReadFor(target, newest.id);
    }
  }, [channelId, markReadFor, stateRef]);

  /* ── search ─────────────────────────────────────────────────────── */

  const runSearch = useCallback((query: string, kind: SearchKind) => {
    const trimmed = query.trim();
    if (trimmed === "") {
      dispatch({ type: "search/close" });
      return;
    }
    dispatch({ type: "search/start", query: trimmed, kind });
    void (async () => {
      try {
        const { data } = await api.GET("/api/v1/search", {
          params: { query: { q: trimmed, kind, limit: SEARCH_PAGE_SIZE } },
        });
        if (data === undefined) {
          dispatch({ type: "search/failed", query: trimmed, kind });
          return;
        }
        dispatch({ type: "search/loaded", query: trimmed, kind, page: data });
      } catch (error) {
        console.warn("Search failed:", error);
        dispatch({ type: "search/failed", query: trimmed, kind });
      }
    })();
  }, []);

  const closeSearch = useCallback(() => {
    dispatch({ type: "search/close" });
  }, []);

  const settleConnection = useCallback(() => {
    dispatch({ type: "connection/settled" });
  }, []);

  /* ── channel management ─────────────────────────────────────────── */

  const createChannel = useCallback(
    async (slug: string, kind: "public" | "private", e2ee: boolean) => {
      try {
        const { data } = await api.POST("/api/v1/channels", { body: { slug, kind, e2ee } });
        if (data === undefined) {
          return null;
        }
        dispatch({ type: "channel/upsert", channel: data });
        return data;
      } catch (error) {
        console.warn("Could not create the channel:", error);
        return null;
      }
    },
    [],
  );

  const openDirectMessage = useCallback(async (userId: string, e2ee: boolean) => {
    try {
      const { data } = await api.POST("/api/v1/dms", { body: { user_id: userId, e2ee } });
      if (data === undefined) {
        return null;
      }
      dispatch({ type: "channel/upsert", channel: data });
      return data;
    } catch (error) {
      console.warn("Could not open the direct message:", error);
      return null;
    }
  }, []);

  const inviteMember = useCallback(
    async (userId: string) => {
      const target = channelId;
      if (target === undefined) {
        return false;
      }
      try {
        const { response } = await api.POST("/api/v1/channels/{channelId}/members", {
          params: { path: { channelId: target } },
          body: { user_id: userId },
        });
        return response.ok;
      } catch (error) {
        console.warn("Could not invite the user:", error);
        return false;
      }
    },
    [channelId],
  );

  const setTopic = useCallback(
    async (topic: string) => {
      const target = channelId;
      if (target === undefined) {
        return false;
      }
      try {
        const { data } = await api.PATCH("/api/v1/channels/{channelId}", {
          params: { path: { channelId: target } },
          body: { topic },
        });
        if (data === undefined) {
          return false;
        }
        dispatch({ type: "channel/upsert", channel: data });
        return true;
      } catch (error) {
        console.warn("Could not set the topic:", error);
        return false;
      }
    },
    [channelId],
  );

  /* ── mention directory ──────────────────────────────────────────── */

  /**
   * Names of the open channel's members, whether or not they have ever spoken.
   *
   * Authors and DM peers are not enough, and the gap is not cosmetic: the
   * encryption warnings name a *person* whose devices changed, and the person
   * most likely to trigger one is a member who has said nothing yet — a
   * colleague who was invited, opened the app, and registered a device. With
   * only authors to go on, that warning asked somebody to make a security
   * judgement about a UUID.
   */
  const [memberNames, setMemberNames] = useState<ReadonlyMap<string, string>>(new Map());
  useEffect(() => {
    if (channelId === undefined) {
      return undefined;
    }
    const controller = new AbortController();
    void (async () => {
      try {
        const { data } = await api.GET("/api/v1/channels/{channelId}/members", {
          params: { path: { channelId }, query: { limit: 100 } },
          signal: controller.signal,
        });
        if (data !== undefined) {
          setMemberNames(
            new Map(data.members.map((member) => [member.id, member.display_name])),
          );
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          // A name this could not fetch degrades to the generic placeholder,
          // never to an id — see resolveMention's callers.
          console.warn("Could not load member names:", error);
        }
      }
    })();
    return () => {
      controller.abort();
    };
  }, [channelId]);

  const directory = useMemo(() => {
    const names = new Map<string, string>([[currentUser.id, currentUser.display_name]]);
    for (const channel of state.channels) {
      if (channel.dm_peer !== undefined) {
        names.set(channel.dm_peer.id, channel.dm_peer.display_name);
      }
    }
    for (const [userId, displayName] of memberNames) {
      names.set(userId, displayName);
    }
    for (const channelView of Object.values(state.views)) {
      for (const message of channelView.messages) {
        names.set(message.author.id, message.author.display_name);
      }
    }
    return names;
  }, [currentUser, memberNames, state.channels, state.views]);

  const resolveMention = useCallback(
    (userId: string) => directory.get(userId) ?? null,
    [directory],
  );

  return {
    state,
    activeChannel,
    view,
    resolveMention,
    sendMessage,
    editMessage,
    deleteMessage,
    loadOlder,
    markRead,
    runSearch,
    closeSearch,
    settleConnection,
    createChannel,
    openDirectMessage,
    inviteMember,
    setTopic,
    dismissRing,
  };
}
