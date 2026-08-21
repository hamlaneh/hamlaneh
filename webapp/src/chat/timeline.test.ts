import { describe, expect, it } from "vitest";

import { buildTimeline } from "./timeline";
import type { Message, PendingMessage, UserSummary } from "./types";

const me: UserSummary = { id: "u-me", username: "me", display_name: "Me" };
const other: UserSummary = { id: "u-other", username: "other", display_name: "Nasrin" };

function message(id: string, author: UserSummary, createdAt: string): Message {
  return {
    id,
    channel_id: "c1",
    author,
    client_msg_id: `client-${id}`,
    content: id,
    created_at: createdAt,
    attachments: [],
  };
}

function groups(items: ReturnType<typeof buildTimeline>) {
  return items.filter((item) => item.kind === "group");
}

describe("buildTimeline", () => {
  it("groups consecutive messages from one author within five minutes", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00Z"),
        message("b", other, "2026-08-21T09:04:00Z"),
        message("c", other, "2026-08-21T09:04:30Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    const runs = groups(items);
    expect(runs).toHaveLength(1);
    expect(runs[0]?.entries).toHaveLength(3);
    // The meta row carries the first message of the run.
    expect(runs[0]?.createdAt).toBe("2026-08-21T09:00:00Z");
  });

  it("breaks a run once five minutes have passed", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00Z"),
        message("b", other, "2026-08-21T09:06:00Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    expect(groups(items)).toHaveLength(2);
  });

  it("keeps a run together at exactly five minutes", () => {
    // The rule is "within five minutes", so the boundary itself groups.
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00Z"),
        message("b", other, "2026-08-21T09:05:00Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    expect(groups(items)).toHaveLength(1);
  });

  it("breaks a run one millisecond past five minutes", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00.000Z"),
        message("b", other, "2026-08-21T09:05:00.001Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    expect(groups(items)).toHaveLength(2);
  });

  it("breaks a run when the author changes", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00Z"),
        message("b", me, "2026-08-21T09:00:30Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    const runs = groups(items);
    expect(runs).toHaveLength(2);
    expect(runs[0]?.own).toBe(false);
    expect(runs[1]?.own).toBe(true);
  });

  it("starts a day with a separator and breaks the run across it", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-20T23:59:00Z"),
        message("b", other, "2026-08-20T23:59:30Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: null,
    });

    expect(items[0]?.kind).toBe("day");
    // Both messages fall on the same local day, so there is exactly one.
    expect(items.filter((item) => item.kind === "day")).toHaveLength(1);
  });

  it("places the unread divider before its anchor and breaks the run there", () => {
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-21T09:00:00Z"),
        message("b", other, "2026-08-21T09:01:00Z"),
      ],
      pending: [],
      currentUser: me,
      dividerBeforeId: "b",
    });

    const kinds = items.map((item) => item.kind);
    expect(kinds).toEqual(["day", "group", "unread", "group"]);
  });

  it("carries unconfirmed messages at the end, attributed to the caller", () => {
    const pending: PendingMessage = {
      clientMsgId: "p1",
      content: "queued",
      createdAt: "2026-08-21T09:10:00Z",
      status: "queued",
    };
    const items = buildTimeline({
      messages: [message("a", other, "2026-08-21T09:00:00Z")],
      pending: [pending],
      currentUser: me,
      dividerBeforeId: null,
    });

    const runs = groups(items);
    expect(runs).toHaveLength(2);
    expect(runs[1]?.own).toBe(true);
    expect(runs[1]?.entries[0]).toEqual({ kind: "pending", id: "p1", pending });
  });

  it("does not open a backwards-dated day for a message queued across midnight", () => {
    // Unconfirmed messages are carried at the end whatever their timestamp, so
    // one composed before midnight must not re-open yesterday below today.
    // Midday instants exactly a day apart, so the two messages land on
    // different local days whatever the runner's time zone is.
    const pending: PendingMessage = {
      clientMsgId: "p1",
      content: "queued last night",
      createdAt: "2026-08-20T12:30:00Z",
      status: "queued",
    };
    const items = buildTimeline({
      messages: [
        message("a", other, "2026-08-20T12:00:00Z"),
        message("b", other, "2026-08-21T12:00:00Z"),
      ],
      pending: [pending],
      currentUser: me,
      dividerBeforeId: null,
    });

    const days = items.filter((item) => item.kind === "day");
    expect(days).toHaveLength(2);
    // ...and the queued message is still the last thing drawn.
    expect(items.at(-1)).toMatchObject({ kind: "group", own: true });
  });
});
