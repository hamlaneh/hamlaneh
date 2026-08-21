import { dayKey } from "./format";
import type { Message, PendingMessage, UserSummary } from "./types";

/**
 * Turns a channel's history into the rows the message list draws: day
 * separators, the unread divider, and runs of bubbles grouped under one
 * avatar, name and timestamp.
 *
 * Grouping rule (CHAT_HANDOFF.md): consecutive messages from one author within
 * five minutes share a meta row. A day separator or the unread divider always
 * breaks a run — the artboards redraw the avatar after both.
 */

export const GROUP_WINDOW_MS = 5 * 60 * 1000;

export type TimelineEntry =
  | { kind: "message"; id: string; message: Message }
  | { kind: "pending"; id: string; pending: PendingMessage };

export interface TimelineGroup {
  kind: "group";
  key: string;
  author: UserSummary;
  /** Own messages sit on the logical end side in brand.soft. */
  own: boolean;
  /** The meta row's timestamp — the first message of the run. */
  createdAt: string;
  entries: TimelineEntry[];
}

export type TimelineItem =
  | { kind: "day"; key: string; iso: string }
  | { kind: "unread"; key: string }
  | TimelineGroup;

interface BuildTimelineInput {
  messages: readonly Message[];
  pending: readonly PendingMessage[];
  currentUser: UserSummary;
  /** Id of the first unread message; the divider is drawn immediately before it. */
  dividerBeforeId: string | null;
}

interface Candidate {
  entry: TimelineEntry;
  author: UserSummary;
  own: boolean;
  createdAt: string;
  /** The message id the unread divider may be anchored to (never a pending one). */
  anchorId: string | null;
}

function candidates(input: BuildTimelineInput): Candidate[] {
  const rows: Candidate[] = input.messages.map((message) => ({
    entry: { kind: "message", id: message.id, message },
    author: message.author,
    own: message.author.id === input.currentUser.id,
    createdAt: message.created_at,
    anchorId: message.id,
  }));
  for (const pending of input.pending) {
    rows.push({
      entry: { kind: "pending", id: pending.clientMsgId, pending },
      author: input.currentUser,
      own: true,
      createdAt: pending.createdAt,
      anchorId: null,
    });
  }
  return rows;
}

function startsNewGroup(previous: Candidate, current: Candidate): boolean {
  if (previous.author.id !== current.author.id) {
    return true;
  }
  const gap = new Date(current.createdAt).getTime() - new Date(previous.createdAt).getTime();
  return Number.isNaN(gap) || gap > GROUP_WINDOW_MS;
}

export function buildTimeline(input: BuildTimelineInput): TimelineItem[] {
  const items: TimelineItem[] = [];
  let group: TimelineGroup | null = null;
  let previous: Candidate | null = null;
  let previousDay: string | null = null;

  for (const candidate of candidates(input)) {
    let broken = false;

    const day = dayKey(candidate.createdAt);
    if (day !== previousDay) {
      items.push({ kind: "day", key: `day-${day}`, iso: candidate.createdAt });
      previousDay = day;
      broken = true;
    }

    if (candidate.anchorId !== null && candidate.anchorId === input.dividerBeforeId) {
      items.push({ kind: "unread", key: `unread-${candidate.anchorId}` });
      broken = true;
    }

    if (group === null || broken || previous === null || startsNewGroup(previous, candidate)) {
      group = {
        kind: "group",
        key: `group-${candidate.entry.id}`,
        author: candidate.author,
        own: candidate.own,
        createdAt: candidate.createdAt,
        entries: [],
      };
      items.push(group);
    }

    group.entries.push(candidate.entry);
    previous = candidate;
  }

  return items;
}
