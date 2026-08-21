import { ws } from "msw";

import { CHAT_USERS } from "./chat";

/**
 * Mock of the realtime endpoint in docs/api/ws-protocol.md: the `hello`
 * handshake, subscribe acknowledgements and the app-level heartbeat pair.
 *
 * Nothing here replays history — the mock server has no replay buffer, so a
 * resume is answered with an empty `resumed` list, which is the contract's
 * normal "backfill over REST" path rather than a failure.
 */

/** Matches any host: the client builds the URL from window.location. */
export const realtimeLink = ws.link("*/api/v1/ws");

interface ClientFrame {
  type?: unknown;
  id?: unknown;
  chan?: unknown;
}

function reply(type: string, frame: ClientFrame, extra: Record<string, unknown> = {}): string {
  return JSON.stringify({
    type,
    ...(typeof frame.id === "string" ? { id: frame.id } : {}),
    ...(typeof frame.chan === "string" ? { chan: frame.chan } : {}),
    ts: new Date().toISOString(),
    data: extra,
  });
}

export const realtimeHandler = realtimeLink.addEventListener("connection", ({ client }) => {
  client.addEventListener("message", (event) => {
    if (typeof event.data !== "string") {
      return;
    }
    let frame: ClientFrame;
    try {
      frame = JSON.parse(event.data) as ClientFrame;
    } catch {
      return;
    }

    switch (frame.type) {
      case "hello":
        client.send(
          reply("hello_ok", frame, {
            protocol_version: 1,
            user_id: CHAT_USERS.me.id,
            session_family_id: "00000000-0000-4000-8000-00000000f001",
            heartbeat_interval_seconds: 30,
            max_frame_bytes: 65536,
            resumed: [],
            resync: [],
          }),
        );
        break;
      case "subscribe":
        client.send(reply("subscribed", frame));
        break;
      case "unsubscribe":
        client.send(reply("unsubscribed", frame));
        break;
      case "ping":
        client.send(reply("pong", frame));
        break;
      default:
        // Unknown types are ignored and the socket stays open (§2).
        break;
    }
  });
});

/** Pushes a server event to every connected client — used by tests. */
export function emitRealtime(frame: Record<string, unknown>): void {
  realtimeLink.broadcast(JSON.stringify({ ts: new Date().toISOString(), data: {}, ...frame }));
}

/** Drops every socket with the given close code — used by tests. */
export function dropRealtimeSockets(code: number): void {
  for (const client of realtimeLink.clients) {
    client.close(code);
  }
}
