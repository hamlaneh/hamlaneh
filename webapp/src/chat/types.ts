import type { components } from "../api/schema";

/**
 * Contract shapes the chat shell works with. Aliases only — the generated
 * schema (npm run api:gen) stays the single source of truth.
 */
export type Channel = components["schemas"]["Channel"];
export type ChannelRef = components["schemas"]["ChannelRef"];
export type Message = components["schemas"]["Message"];
export type MessagePage = components["schemas"]["MessagePage"];
export type UserSummary = components["schemas"]["UserSummary"];
export type Presence = components["schemas"]["Presence"];
export type Attachment = components["schemas"]["Attachment"];
export type LinkPreview = components["schemas"]["LinkPreview"];
export type SearchPage = components["schemas"]["SearchPage"];
export type SearchResult = components["schemas"]["SearchResult"];
export type User = components["schemas"]["User"];

/**
 * A message the user composed that the server has not confirmed yet.
 *
 * `clientMsgId` is the contract's idempotency key: generated once, reused
 * verbatim on every retry (ws-protocol.md §5). A fresh id on retry is exactly
 * the duplicate-message bug the key exists to prevent.
 */
export interface PendingMessage {
  clientMsgId: string;
  content: string;
  /**
   * Files already uploaded and waiting to be attached by the send. Kept on
   * the pending message rather than only in the composer because the offline
   * queue replays from here — dropping them would send the text alone once
   * the connection returned, which is the bug the queue exists to prevent.
   */
  attachments: Attachment[];
  createdAt: string;
  /** "sending" — a request is in flight. "queued" — waiting for the connection. */
  status: "sending" | "queued";
}

/** What the connection banner draws, and what the composer disables itself on. */
export type ConnectionState =
  | { status: "connecting" }
  | { status: "online" }
  /** An attempt is in flight after a drop — the spinner banner. */
  | { status: "reconnecting"; lastConnectedAt: string | null }
  /** Waiting out the backoff — the banner counts this down. */
  | { status: "offline"; retryInSeconds: number; lastConnectedAt: string | null }
  /** Session revoked or a deliberate close: no further attempts. */
  | { status: "closed"; reason: "revoked" | "normal" };
