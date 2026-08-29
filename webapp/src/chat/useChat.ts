import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";

import { api } from "../api/client";
import type { MlsController } from "../mls/useMls";
import { RealtimeClient, realtimeUrl } from "./realtime";
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
  openDirectMessage: (userId: string) => Promise<Channel | null>;
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
          break;
        case "mls_welcome":
          mlsRef.current.syncWelcomes();
          break;
        case "member_added":
          // Somebody joined an encrypted conversation: every member's client
          // races to add their devices, and all but one lose harmlessly.
          if (isE2ee(frame.chan)) {
            mlsRef.current.memberAdded(frame.chan);
          }
          break;
        case "member_removed":
          if (isE2ee(frame.chan)) {
            mlsRef.current.memberRemoved(frame.chan, frame.user.id);
          }
          break;
        default:
          // subscribed / unsubscribed / typing / error carry nothing this
          // slice draws. Typing has a protocol but no designed UI yet
          // (ws-protocol.md §4).
          break;
      }
    },
    [currentUserId, isE2ee, mlsRef],
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
    if (isE2ee(channelId)) {
      // Find or create the group, catch up on the log, add whoever is
      // missing. Everything else about this channel behaves as usual.
      mlsRef.current.openChannel(channelId);
    }

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
  }, [channelId, currentUserId, focusMessageId, isE2ee, loadHistory, markReadFor, mlsRef, stateRef]);

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
    mls.decryptAll(channelId, view.messages);
  }, [channelId, mls, view.messages]);

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
    mls.syncWelcomes();
    if (channelId !== undefined && isE2ee(channelId)) {
      mls.syncChannel(channelId);
    }
  }, [channelId, connectionStatus, hasEncryptedChannel, isE2ee, mls]);

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

  /** Queued messages send themselves when the connection returns. */
  const wasOnline = useRef(false);
  useEffect(() => {
    const online = state.connection.status === "online";
    const regained = online && !wasOnline.current;
    wasOnline.current = online;
    if (!regained) {
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
  }, [state.connection, enqueueSend, stateRef]);

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

  const openDirectMessage = useCallback(async (userId: string) => {
    try {
      const { data } = await api.POST("/api/v1/dms", { body: { user_id: userId } });
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

  const directory = useMemo(() => {
    const names = new Map<string, string>([[currentUser.id, currentUser.display_name]]);
    for (const channel of state.channels) {
      if (channel.dm_peer !== undefined) {
        names.set(channel.dm_peer.id, channel.dm_peer.display_name);
      }
    }
    for (const channelView of Object.values(state.views)) {
      for (const message of channelView.messages) {
        names.set(message.author.id, message.author.display_name);
      }
    }
    return names;
  }, [currentUser, state.channels, state.views]);

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
