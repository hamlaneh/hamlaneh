import { useCallback, useEffect, useMemo, useReducer, useRef } from "react";

import { api } from "../api/client";
import { RealtimeClient, realtimeUrl } from "./realtime";
import type { RealtimeOptions, ServerFrame } from "./realtime";
import { chatReducer, emptyView, initialChatState } from "./store";
import type { ChannelView, ChatState, SearchKind } from "./store";
import type { Channel, Message, UserSummary } from "./types";
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
  sendMessage: (content: string) => void;
  editMessage: (messageId: string, content: string) => Promise<boolean>;
  deleteMessage: (messageId: string) => Promise<boolean>;
  loadOlder: () => void;
  markRead: () => void;
  runSearch: (query: string, kind: SearchKind) => void;
  closeSearch: () => void;
  /** Dismisses the "Back online" banner once its window has elapsed. */
  settleConnection: () => void;
  createChannel: (slug: string, kind: "public" | "private") => Promise<Channel | null>;
  openDirectMessage: (userId: string) => Promise<Channel | null>;
  inviteMember: (userId: string) => Promise<boolean>;
  setTopic: (topic: string) => Promise<boolean>;
}

interface UseChatInput {
  currentUser: UserSummary;
  channelId: string | undefined;
  focusMessageId: string | undefined;
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

  /* ── realtime ───────────────────────────────────────────────────── */

  const clientRef = useRef<RealtimeClient | null>(null);
  const backfillRef = useLatest(backfill);

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
        default:
          // subscribed / unsubscribed / typing / member_added / error carry
          // nothing this slice draws. Typing has a protocol but no designed
          // UI yet (ws-protocol.md §4).
          break;
      }
    },
    [currentUserId],
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
      const page = needsLoad
        ? await loadHistory(channelId, {
            mode: "replace",
            ...(focusMessageId === undefined ? {} : { around: focusMessageId }),
          })
        : null;
      const newest = (page?.messages ?? stateRef.current.views[channelId]?.messages ?? []).at(-1);
      if (newest !== undefined) {
        await markReadFor(channelId, newest.id);
      }
    })();

    return () => {
      client?.unsubscribe(channelId);
      dispatch({ type: "channel/leave", channelId });
    };
  }, [channelId, focusMessageId, loadHistory, markReadFor, stateRef]);

  /* ── sending ────────────────────────────────────────────────────── */

  const attemptSend = useCallback(
    async (target: string, clientMsgId: string, content: string) => {
      dispatch({ type: "pending/sending", channelId: target, clientMsgId });
      try {
        const { data } = await api.POST("/api/v1/channels/{channelId}/messages", {
          params: { path: { channelId: target } },
          // The idempotency key is reused verbatim on every retry: a resend
          // returns the message that already exists instead of a duplicate.
          body: { client_msg_id: clientMsgId, content },
        });
        if (data === undefined) {
          dispatch({ type: "pending/queue", channelId: target, clientMsgId });
          return;
        }
        dispatch({ type: "message/upsert", channelId: target, message: data, currentUserId });
        await markReadFor(target, data.id);
      } catch (error) {
        // Kept, not dropped: the design's dashed "Waiting to send" treatment.
        console.warn("Message send failed; it stays queued:", error);
        dispatch({ type: "pending/queue", channelId: target, clientMsgId });
      }
    },
    [currentUserId, markReadFor],
  );

  const sendMessage = useCallback(
    (content: string) => {
      const target = channelId;
      const trimmed = content.trim();
      if (target === undefined || trimmed === "") {
        return;
      }
      const clientMsgId = newUuid();
      dispatch({
        type: "pending/add",
        channelId: target,
        pending: {
          clientMsgId,
          content: trimmed,
          createdAt: new Date().toISOString(),
          status: "sending",
        },
      });
      void attemptSend(target, clientMsgId, trimmed);
    },
    [attemptSend, channelId],
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
          void attemptSend(target, pending.clientMsgId, pending.content);
        }
      }
    }
  }, [state.connection, attemptSend, stateRef]);

  /* ── message actions ────────────────────────────────────────────── */

  const editMessage = useCallback(
    async (messageId: string, content: string) => {
      const target = channelId;
      const trimmed = content.trim();
      if (target === undefined || trimmed === "") {
        return false;
      }
      try {
        const { data } = await api.PATCH("/api/v1/channels/{channelId}/messages/{messageId}", {
          params: { path: { channelId: target, messageId } },
          body: { content: trimmed },
        });
        if (data === undefined) {
          return false;
        }
        dispatch({ type: "message/upsert", channelId: target, message: data, currentUserId });
        return true;
      } catch (error) {
        console.warn("Could not edit the message:", error);
        return false;
      }
    },
    [channelId, currentUserId],
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
        dispatch({ type: "message/upsert", channelId: target, message: removed, currentUserId });
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

  const createChannel = useCallback(async (slug: string, kind: "public" | "private") => {
    try {
      const { data } = await api.POST("/api/v1/channels", { body: { slug, kind } });
      if (data === undefined) {
        return null;
      }
      dispatch({ type: "channel/upsert", channel: data });
      return data;
    } catch (error) {
      console.warn("Could not create the channel:", error);
      return null;
    }
  }, []);

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
  };
}
