import { describe, expect, it } from "vitest";

import { chatReducer, computeDividerAnchor, initialChatState } from "./store";
import type { ChatAction, ChatState } from "./store";
import type { Channel, Message, MessagePage, UserSummary } from "./types";

/**
 * The reducer owns both of ws-protocol.md §5's exactly-once invariants: an
 * upsert renders a message once however many times it arrives, and the unread
 * badge counts a message once however many times it arrives. The second one is
 * only visible here, which is why these are unit tests and not shell tests.
 */

const ME: UserSummary = { id: "u-me", username: "me", display_name: "Me" };
const OTHER: UserSummary = { id: "u-other", username: "nasrin", display_name: "Nasrin" };

const CHANNEL_ID = "c-deploys";

function channel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: CHANNEL_ID,
    kind: "public",
    slug: "deploys",
    topic: "",
    member_count: 3,
    unread_count: 0,
    mention_count: 0,
    created_at: "2026-08-01T09:00:00.000Z",
    created_by: OTHER.id,
    ...overrides,
  };
}

function message(id: string, author: UserSummary, createdAt: string): Message {
  return {
    id,
    channel_id: CHANNEL_ID,
    author,
    client_msg_id: `client-${id}`,
    content: id,
    created_at: createdAt,
    attachments: [],
  };
}

function page(messages: Message[]): MessagePage {
  return { messages };
}

function reduce(state: ChatState, ...actions: ChatAction[]): ChatState {
  return actions.reduce(chatReducer, state);
}

function view(state: ChatState) {
  const found = state.views[CHANNEL_ID];
  if (found === undefined) {
    throw new Error("the channel has no view");
  }
  return found;
}

function unreadCount(state: ChatState): number {
  return state.channels.find((entry) => entry.id === CHANNEL_ID)?.unread_count ?? -1;
}

function mentionCount(state: ChatState): number {
  return state.channels.find((entry) => entry.id === CHANNEL_ID)?.mention_count ?? -1;
}

function upsert(entry: Message, created: boolean): ChatAction {
  return {
    type: "message/upsert",
    channelId: CHANNEL_ID,
    message: entry,
    currentUserId: ME.id,
    created,
  };
}

describe("message/upsert", () => {
  const arrival = message("m1", OTHER, "2026-08-21T09:00:00.000Z");

  it("renders a duplicated arrival once", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(arrival, true),
      upsert(arrival, true),
    );

    expect(view(state).messages).toHaveLength(1);
  });

  it("counts a duplicated arrival once", () => {
    // ws-protocol.md §5 promises a duplicate WILL arrive — replay overlap, a
    // re-broadcast, a backfill crossing the replay window.
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(arrival, true),
      upsert(arrival, true),
    );

    expect(unreadCount(state)).toBe(1);
  });

  it("does not count an edit or a delete of an old message", () => {
    const start = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(arrival, true),
    );

    const edited = reduce(start, upsert({ ...arrival, content: "fixed a typo" }, false));
    expect(unreadCount(edited)).toBe(1);

    const removed = reduce(
      edited,
      upsert({ ...arrival, content: "", deleted_at: "2026-08-21T09:05:00.000Z" }, false),
    );
    expect(unreadCount(removed)).toBe(1);
  });

  it("raises the mention badge for a message carrying the caller's token", () => {
    // The server counted this one when it stored the message; no event carries
    // a fresh Channel, so a live arrival that is not read here shows as plain
    // unread until the next reload.
    const ping = {
      ...message("m3", OTHER, "2026-08-21T09:03:00.000Z"),
      content: `<@${ME.id}> can you look?`,
    };
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(ping, true),
    );

    expect(mentionCount(state)).toBe(1);
    expect(unreadCount(state)).toBe(1);
  });

  it("does not raise the mention badge for somebody else's mention", () => {
    const elsewhere = {
      ...message("m4", OTHER, "2026-08-21T09:04:00.000Z"),
      content: `<@${OTHER.id}> can you look?`,
    };
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(elsewhere, true),
    );

    expect(mentionCount(state)).toBe(0);
    expect(unreadCount(state)).toBe(1);
  });

  it("counts a duplicated mention once", () => {
    const ping = {
      ...message("m5", OTHER, "2026-08-21T09:05:00.000Z"),
      content: `<@${ME.id}> again`,
    };
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(ping, true),
      upsert(ping, true),
    );

    expect(mentionCount(state)).toBe(1);
  });

  it("does not count the caller's own message", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(message("mine", ME, "2026-08-21T09:01:00.000Z"), true),
    );

    expect(unreadCount(state)).toBe(0);
  });

  it("reconciles the optimistic bubble by client_msg_id", () => {
    const stored = message("m2", ME, "2026-08-21T09:02:00.000Z");
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      {
        type: "pending/add",
        channelId: CHANNEL_ID,
        pending: {
          clientMsgId: stored.client_msg_id,
          content: stored.content,
          attachments: [],
          createdAt: stored.created_at,
          status: "sending",
        },
      },
      upsert(stored, true),
    );

    expect(view(state).pending).toHaveLength(0);
    expect(view(state).messages).toHaveLength(1);
  });
});

describe("the unread divider", () => {
  const older = message("m-read", OTHER, "2026-08-21T09:00:00.000Z");
  const unread = message("m-unread", OTHER, "2026-08-21T09:10:00.000Z");
  const withUnread = channel({ unread_count: 1, last_read_message_id: older.id });

  const entryLoad: ChatAction = {
    type: "history/loaded",
    channelId: CHANNEL_ID,
    page: page([older, unread]),
    mode: "replace",
    currentUserId: ME.id,
  };

  it("is placed on entry, before the first message somebody else wrote", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      entryLoad,
    );

    expect(view(state).dividerBeforeId).toBe(unread.id);
    expect(view(state).dividerPlaced).toBe(true);
  });

  it("holds its position across a later page", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      entryLoad,
      {
        type: "history/loaded",
        channelId: CHANNEL_ID,
        page: page([message("m-newer", OTHER, "2026-08-21T09:20:00.000Z")]),
        mode: "newer",
        currentUserId: ME.id,
      },
    );

    expect(view(state).dividerBeforeId).toBe(unread.id);
  });

  it("survives a background backfill into a channel nobody is looking at", () => {
    // A resync for an unopened channel used to mark the divider "placed" at
    // nothing, and entering it afterwards ran no replace load to correct that.
    const backfilled = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      {
        type: "history/loaded",
        channelId: CHANNEL_ID,
        page: page([older, unread]),
        mode: "newer",
        currentUserId: ME.id,
      },
    );

    expect(backfilled.views[CHANNEL_ID]?.dividerPlaced).toBe(false);
    expect(backfilled.views[CHANNEL_ID]?.dividerBeforeId).toBeNull();

    const entered = reduce(backfilled, {
      type: "channel/enter",
      channelId: CHANNEL_ID,
      currentUserId: ME.id,
    });

    expect(view(entered).dividerBeforeId).toBe(unread.id);
    expect(view(entered).dividerPlaced).toBe(true);
  });

  it("is released on leaving so re-entry re-reads the position", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      entryLoad,
      { type: "channel/leave", channelId: CHANNEL_ID },
    );

    expect(view(state).dividerPlaced).toBe(false);
    expect(view(state).dividerBeforeId).toBeNull();
    expect(view(state).focusMessageId).toBeNull();
  });

  it("does not claim a place while the entry load is still in flight", () => {
    // Entering twice before the first page lands (a fast channel switch, or
    // React re-running the effect) must not mark the divider decided at
    // nothing — the load that is on its way is the one that decides.
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      { type: "history/start", channelId: CHANNEL_ID, older: false },
      { type: "channel/enter", channelId: CHANNEL_ID, currentUserId: ME.id },
      entryLoad,
    );

    expect(view(state).dividerBeforeId).toBe(unread.id);
  });

  it("does not re-place itself once entry has decided", () => {
    const state = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [withUnread] },
      entryLoad,
      { type: "channel/enter", channelId: CHANNEL_ID, currentUserId: ME.id },
    );

    expect(view(state).dividerBeforeId).toBe(unread.id);
  });
});

describe("computeDividerAnchor", () => {
  const read = message("a", OTHER, "2026-08-21T09:00:00.000Z");
  const mine = message("b", ME, "2026-08-21T09:01:00.000Z");
  const theirs = message("c", OTHER, "2026-08-21T09:02:00.000Z");

  it("returns null for a fully read channel", () => {
    expect(computeDividerAnchor([read, theirs], channel(), ME.id)).toBeNull();
  });

  it("returns null when the channel is not known", () => {
    expect(computeDividerAnchor([read, theirs], undefined, ME.id)).toBeNull();
  });

  it("skips the caller's own messages after the read position", () => {
    const anchor = computeDividerAnchor(
      [read, mine, theirs],
      channel({ unread_count: 2, last_read_message_id: read.id }),
      ME.id,
    );

    expect(anchor).toBe(theirs.id);
  });

  it("starts from the beginning when nothing has been read", () => {
    const anchor = computeDividerAnchor(
      [read, theirs],
      channel({ unread_count: 2, last_read_message_id: null }),
      ME.id,
    );

    expect(anchor).toBe(read.id);
  });

  it("returns null when everything after the read position is the caller's own", () => {
    const anchor = computeDividerAnchor(
      [read, mine],
      channel({ unread_count: 1, last_read_message_id: read.id }),
      ME.id,
    );

    expect(anchor).toBeNull();
  });
});

describe("history/loaded cursors", () => {
  it("keeps the other direction's cursor when only one end was fetched", () => {
    const entered = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      {
        type: "history/loaded",
        channelId: CHANNEL_ID,
        page: {
          messages: [message("m1", OTHER, "2026-08-21T09:00:00.000Z")],
          before_cursor: "older-handle",
          after_cursor: "newer-handle",
        },
        mode: "replace",
        currentUserId: ME.id,
      },
    );

    const olderPage = reduce(entered, {
      type: "history/loaded",
      channelId: CHANNEL_ID,
      page: page([message("m0", OTHER, "2026-08-21T08:00:00.000Z")]),
      mode: "older",
      currentUserId: ME.id,
    });

    // The older page carried no cursors, so the newer handle must survive it.
    expect(view(olderPage).afterCursor).toBe("newer-handle");
    expect(view(olderPage).beforeCursor).toBeNull();
    expect(view(olderPage).messages.map((entry) => entry.id)).toEqual(["m0", "m1"]);
  });
});

describe("connection", () => {
  it("only announces a reconnection after the connection was actually down", () => {
    const firstConnect = reduce(initialChatState, {
      type: "connection/set",
      connection: { status: "online" },
    });
    expect(firstConnect.justReconnected).toBe(false);

    const recovered = reduce(
      firstConnect,
      { type: "connection/set", connection: { status: "offline", retryInSeconds: 2, lastConnectedAt: null } },
      { type: "connection/set", connection: { status: "online" } },
    );
    expect(recovered.justReconnected).toBe(true);

    expect(reduce(recovered, { type: "connection/settled" }).justReconnected).toBe(false);
  });
});

describe("read/mark", () => {
  it("clears both counts and moves the stored position", () => {
    const state = reduce(
      initialChatState,
      {
        type: "channels/loaded",
        channels: [channel({ unread_count: 4, mention_count: 2 })],
      },
      { type: "read/mark", channelId: CHANNEL_ID, messageId: "m-newest" },
    );

    const updated = state.channels[0];
    expect(updated?.unread_count).toBe(0);
    expect(updated?.mention_count).toBe(0);
    expect(updated?.last_read_message_id).toBe("m-newest");
  });
});

describe("a link preview arriving by message_updated", () => {
  const plain = { ...message("m1", OTHER, "2026-08-21T09:00:00.000Z"), content: "https://x.test/a" };

  it("is kept when the enrichment lands, and dropped when the edit removes the URL", () => {
    const enriched = { ...plain, link_preview: { url: "https://x.test/a", title: "A" } };
    const withPreview = reduce(
      initialChatState,
      { type: "channels/loaded", channels: [channel()] },
      upsert(plain, true),
      // message_updated carries the whole message, so the upsert must replace
      // rather than merge field by field — otherwise a preview could never
      // arrive, and an edit could never take one away.
      upsert(enriched, false),
    );
    expect(view(withPreview).messages[0]?.link_preview?.title).toBe("A");

    const edited = reduce(withPreview, upsert({ ...plain, content: "never mind" }, false));
    expect(view(edited).messages[0]?.link_preview).toBeUndefined();
    // And the enrichment did not count as a new arrival.
    expect(unreadCount(edited)).toBe(1);
  });
});
