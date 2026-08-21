import type {
  Channel,
  ConnectionState,
  Message,
  MessagePage,
  PendingMessage,
  Presence,
  SearchPage,
} from "./types";

/**
 * All chat state, as one reducer.
 *
 * The two invariants worth stating out loud, both from ws-protocol.md §5:
 *  - messages are keyed by id and upserted, never appended blindly, so a
 *    resume replay overlapping a REST backfill still renders once;
 *  - an unconfirmed message is keyed by its `client_msg_id`, which is what
 *    reconciles the optimistic bubble with the stored message.
 */

export type SearchKind = "messages" | "files";

export type SearchState =
  | { status: "closed" }
  | { status: "loading"; query: string; kind: SearchKind }
  | { status: "ready"; query: string; kind: SearchKind; page: SearchPage }
  | { status: "error"; query: string; kind: SearchKind };

export interface ChannelView {
  status: "idle" | "loading" | "ready" | "error";
  messages: Message[];
  pending: PendingMessage[];
  /** Cursor for the next older page; null when the channel start is loaded. */
  beforeCursor: string | null;
  /** Cursor for the next newer page — the reconnect backfill handle. */
  afterCursor: string | null;
  loadingOlder: boolean;
  /** Frozen on entry: the divider holds its place until the channel is left. */
  dividerBeforeId: string | null;
  dividerPlaced: boolean;
  /** Permalink target, highlighted once its page is loaded. */
  focusMessageId: string | null;
}

export interface ChatState {
  channelsStatus: "loading" | "ready" | "error";
  channels: Channel[];
  views: Record<string, ChannelView>;
  connection: ConnectionState;
  /**
   * True from the moment a dropped connection comes back until the banner
   * dismisses itself. The transition is only visible to the reducer, which is
   * why "Back online" is decided here rather than in the banner.
   */
  justReconnected: boolean;
  search: SearchState;
}

export const initialChatState: ChatState = {
  channelsStatus: "loading",
  channels: [],
  views: {},
  connection: { status: "connecting" },
  justReconnected: false,
  search: { status: "closed" },
};

export function emptyView(): ChannelView {
  return {
    status: "idle",
    messages: [],
    pending: [],
    beforeCursor: null,
    afterCursor: null,
    loadingOlder: false,
    dividerBeforeId: null,
    dividerPlaced: false,
    focusMessageId: null,
  };
}

export type ChatAction =
  | { type: "channels/loaded"; channels: Channel[] }
  | { type: "channels/failed" }
  | { type: "channel/upsert"; channel: Channel }
  | { type: "presence/set"; userId: string; state: Presence }
  | { type: "history/start"; channelId: string; older: boolean }
  | {
      type: "history/loaded";
      channelId: string;
      page: MessagePage;
      mode: "replace" | "older" | "newer";
      currentUserId: string;
      focusMessageId?: string | null;
    }
  | { type: "history/failed"; channelId: string }
  | { type: "message/upsert"; channelId: string; message: Message; currentUserId: string }
  | { type: "pending/add"; channelId: string; pending: PendingMessage }
  | { type: "pending/queue"; channelId: string; clientMsgId: string }
  | { type: "pending/sending"; channelId: string; clientMsgId: string }
  | { type: "read/mark"; channelId: string; messageId: string }
  | { type: "channel/leave"; channelId: string }
  | { type: "connection/set"; connection: ConnectionState }
  | { type: "connection/settled" }
  | { type: "search/start"; query: string; kind: SearchKind }
  | { type: "search/loaded"; query: string; kind: SearchKind; page: SearchPage }
  | { type: "search/failed"; query: string; kind: SearchKind }
  | { type: "search/close" };

/** Ascending by (created_at, id) — the order every contract page is served in. */
function isBefore(left: Message, right: Message): boolean {
  return left.created_at === right.created_at
    ? left.id < right.id
    : left.created_at < right.created_at;
}

function mergeMessages(existing: readonly Message[], incoming: readonly Message[]): Message[] {
  const byId = new Map(existing.map((message) => [message.id, message]));
  for (const message of incoming) {
    byId.set(message.id, message);
  }
  const merged = [...byId.values()];
  merged.sort((left, right) => (isBefore(left, right) ? -1 : left.id === right.id ? 0 : 1));
  return merged;
}

/**
 * Where the "New messages" divider goes on entry: the first message after the
 * caller's read position that somebody else wrote. Null when nothing is
 * unread — the divider is never drawn over a fully-read channel.
 */
export function computeDividerAnchor(
  messages: readonly Message[],
  channel: Channel | undefined,
  currentUserId: string,
): string | null {
  if (channel === undefined || channel.unread_count === 0) {
    return null;
  }
  const readIndex =
    channel.last_read_message_id === undefined || channel.last_read_message_id === null
      ? -1
      : messages.findIndex((message) => message.id === channel.last_read_message_id);
  for (let index = readIndex + 1; index < messages.length; index += 1) {
    const message = messages[index];
    if (message !== undefined && message.author.id !== currentUserId) {
      return message.id;
    }
  }
  return null;
}

function withView(
  state: ChatState,
  channelId: string,
  update: (view: ChannelView) => ChannelView,
): ChatState {
  const view = state.views[channelId] ?? emptyView();
  return { ...state, views: { ...state.views, [channelId]: update(view) } };
}

function replaceChannel(channels: readonly Channel[], next: Channel): Channel[] {
  const index = channels.findIndex((channel) => channel.id === next.id);
  if (index === -1) {
    return [...channels, next];
  }
  const copy = [...channels];
  copy[index] = next;
  return copy;
}

export function chatReducer(state: ChatState, action: ChatAction): ChatState {
  switch (action.type) {
    case "channels/loaded":
      return { ...state, channelsStatus: "ready", channels: action.channels };

    case "channels/failed":
      return { ...state, channelsStatus: "error" };

    case "channel/upsert":
      return { ...state, channels: replaceChannel(state.channels, action.channel) };

    case "presence/set":
      // Presence is DM-scoped by the protocol, and the design draws it in
      // exactly two places: the DM rows and the DM header.
      return {
        ...state,
        channels: state.channels.map((channel) =>
          channel.kind === "dm" && channel.dm_peer?.id === action.userId
            ? { ...channel, dm_peer: { ...channel.dm_peer, presence: action.state } }
            : channel,
        ),
      };

    case "history/start":
      return withView(state, action.channelId, (view) => ({
        ...view,
        status: action.older ? view.status : "loading",
        loadingOlder: action.older,
      }));

    case "history/loaded": {
      const channel = state.channels.find((entry) => entry.id === action.channelId);
      return withView(state, action.channelId, (view) => {
        const messages =
          action.mode === "replace"
            ? mergeMessages([], action.page.messages)
            : mergeMessages(view.messages, action.page.messages);
        const placed = view.dividerPlaced || action.mode !== "replace";
        return {
          ...view,
          status: "ready",
          loadingOlder: false,
          messages,
          // A page fetched with `before` only refreshes the older handle; a
          // page fetched with `after` only the newer one. Replacing both from
          // a partial page would lose the other direction's cursor.
          beforeCursor:
            action.mode === "newer" ? view.beforeCursor : (action.page.before_cursor ?? null),
          afterCursor:
            action.mode === "older" ? view.afterCursor : (action.page.after_cursor ?? null),
          dividerBeforeId: placed
            ? view.dividerBeforeId
            : computeDividerAnchor(messages, channel, action.currentUserId),
          dividerPlaced: true,
          focusMessageId:
            action.focusMessageId === undefined ? view.focusMessageId : action.focusMessageId,
        };
      });
    }

    case "history/failed":
      return withView(state, action.channelId, (view) => ({
        ...view,
        status: view.status === "ready" ? "ready" : "error",
        loadingOlder: false,
      }));

    case "message/upsert": {
      const next = withView(state, action.channelId, (view) => ({
        ...view,
        messages: mergeMessages(view.messages, [action.message]),
        // Reconciliation: the optimistic bubble and the stored message are the
        // same message, joined by the idempotency key.
        pending: view.pending.filter(
          (entry) => entry.clientMsgId !== action.message.client_msg_id,
        ),
      }));
      if (action.message.author.id === action.currentUserId) {
        return next;
      }
      // Somebody else's message in a channel we are not reading raises the
      // sidebar count; the active channel clears it through read/mark.
      return {
        ...next,
        channels: next.channels.map((channel) =>
          channel.id === action.channelId
            ? { ...channel, unread_count: channel.unread_count + 1 }
            : channel,
        ),
      };
    }

    case "pending/add":
      return withView(state, action.channelId, (view) => ({
        ...view,
        pending: [...view.pending, action.pending],
      }));

    case "pending/queue":
    case "pending/sending":
      return withView(state, action.channelId, (view) => ({
        ...view,
        pending: view.pending.map((entry) =>
          entry.clientMsgId === action.clientMsgId
            ? { ...entry, status: action.type === "pending/queue" ? "queued" : "sending" }
            : entry,
        ),
      }));

    case "read/mark":
      return {
        ...state,
        channels: state.channels.map((channel) =>
          channel.id === action.channelId
            ? {
                ...channel,
                unread_count: 0,
                mention_count: 0,
                last_read_message_id: action.messageId,
              }
            : channel,
        ),
      };

    case "channel/leave":
      // The divider is placed on entry and released on exit, so re-entering
      // a channel re-reads the position instead of freezing yesterday's.
      return withView(state, action.channelId, (view) => ({
        ...view,
        dividerBeforeId: null,
        dividerPlaced: false,
        focusMessageId: null,
      }));

    case "connection/set": {
      const wasDown =
        state.connection.status === "reconnecting" || state.connection.status === "offline";
      const online = action.connection.status === "online";
      return {
        ...state,
        connection: action.connection,
        justReconnected: online && (wasDown || state.justReconnected),
      };
    }

    case "connection/settled":
      // The "Back online" banner auto-dismisses after 3 s; the reconnecting
      // one never does.
      return { ...state, justReconnected: false };

    case "search/start":
      return { ...state, search: { status: "loading", query: action.query, kind: action.kind } };

    case "search/loaded":
      return {
        ...state,
        search: { status: "ready", query: action.query, kind: action.kind, page: action.page },
      };

    case "search/failed":
      return { ...state, search: { status: "error", query: action.query, kind: action.kind } };

    case "search/close":
      return { ...state, search: { status: "closed" } };
  }
}
