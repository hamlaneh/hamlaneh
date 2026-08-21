import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CLOSE_HEARTBEAT_TIMEOUT,
  CLOSE_SESSION_REVOKED,
  fullJitterDelay,
  parseServerFrame,
  RealtimeClient,
} from "./realtime";
import type { ConnectionState } from "./types";

/**
 * A WebSocket stand-in the test drives directly. The client only ever uses
 * addEventListener/removeEventListener/send/close/readyState, so nothing here
 * is a guess about the platform API.
 */
class FakeSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;

  readyState = FakeSocket.CONNECTING;
  readonly sent: string[] = [];
  private readonly listeners = new Map<string, Set<(event: unknown) => void>>();

  addEventListener(type: string, listener: (event: unknown) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  removeEventListener(type: string, listener: (event: unknown) => void): void {
    this.listeners.get(type)?.delete(listener);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.readyState = 3;
  }

  emit(type: string, event: unknown): void {
    for (const listener of [...(this.listeners.get(type) ?? [])]) {
      listener(event);
    }
  }

  open(): void {
    this.readyState = FakeSocket.OPEN;
    this.emit("open", {});
  }

  deliver(frame: Record<string, unknown>): void {
    this.emit("message", { data: JSON.stringify(frame) });
  }

  drop(code: number): void {
    this.readyState = 3;
    this.emit("close", { code });
  }
}

interface Harness {
  client: RealtimeClient;
  sockets: FakeSocket[];
  statuses: ConnectionState[];
  frames: string[];
  resyncs: string[];
}

function harness(): Harness {
  const sockets: FakeSocket[] = [];
  const statuses: ConnectionState[] = [];
  const frames: string[] = [];
  const resyncs: string[] = [];
  const client = new RealtimeClient({
    url: "ws://localhost/api/v1/ws",
    socketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket as unknown as WebSocket;
    },
    retryDelayMs: () => 0,
    onFrame: (frame) => frames.push(frame.type),
    onStatus: (status) => statuses.push(status),
    onResync: (channelId) => resyncs.push(channelId),
  });
  return { client, sockets, statuses, frames, resyncs };
}

function helloOk(extra: Record<string, unknown> = {}) {
  return {
    type: "hello_ok",
    ts: "2026-08-21T09:00:00Z",
    data: {
      protocol_version: 1,
      user_id: "u1",
      session_family_id: "f1",
      heartbeat_interval_seconds: 30,
      max_frame_bytes: 65536,
      resumed: [],
      resync: [],
      ...extra,
    },
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("RealtimeClient handshake", () => {
  it("sends hello as its first frame and reports online on hello_ok", () => {
    const { client, sockets, statuses } = harness();
    client.connect();
    expect(statuses[0]).toEqual({ status: "connecting" });

    const socket = sockets[0];
    socket?.open();
    const first: unknown = JSON.parse(socket?.sent[0] ?? "{}");
    expect(first).toMatchObject({ type: "hello", data: { protocol_version: 1 } });

    socket?.deliver(helloOk());
    expect(statuses.at(-1)).toEqual({ status: "online" });
    client.close();
  });

  it("answers a server ping with a pong", () => {
    const { client, sockets } = harness();
    client.connect();
    const socket = sockets[0];
    socket?.open();
    socket?.deliver(helloOk());
    socket?.deliver({ type: "ping", ts: "2026-08-21T09:00:30Z", data: {} });

    const types = (socket?.sent ?? []).map((raw) => (JSON.parse(raw) as { type: string }).type);
    expect(types).toContain("pong");
    client.close();
  });

  it("resumes with the highest processed seq per channel", () => {
    const { client, sockets } = harness();
    client.connect();
    const first = sockets[0];
    first?.open();
    first?.deliver(helloOk());
    first?.deliver({
      type: "message_created",
      chan: "chan-a",
      seq: 418,
      ts: "2026-08-21T09:01:00Z",
      data: { message: { id: "m1" } },
    });

    vi.useFakeTimers();
    first?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);
    const second = sockets[1];
    second?.open();

    const hello = JSON.parse(second?.sent[0] ?? "{}") as {
      data: { resume?: { chan: string; seq: number }[] };
    };
    expect(hello.data.resume).toEqual([{ chan: "chan-a", seq: 418 }]);
    client.close();
  });

  it("reports a channel the server could not replay so it is backfilled over REST", () => {
    const { client, sockets, resyncs } = harness();
    client.connect();
    const socket = sockets[0];
    socket?.open();
    socket?.deliver(helloOk({ resync: ["chan-b"] }));

    expect(resyncs).toEqual(["chan-b"]);
    client.close();
  });
});

describe("RealtimeClient reconnect", () => {
  it("counts down the backoff and then reconnects", () => {
    vi.useFakeTimers();
    const sockets: FakeSocket[] = [];
    const statuses: ConnectionState[] = [];
    const client = new RealtimeClient({
      url: "ws://localhost/api/v1/ws",
      socketFactory: () => {
        const socket = new FakeSocket();
        sockets.push(socket);
        return socket as unknown as WebSocket;
      },
      retryDelayMs: () => 3000,
      onFrame: () => undefined,
      onStatus: (status) => statuses.push(status),
      onResync: () => undefined,
    });

    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);

    expect(statuses.at(-1)).toMatchObject({ status: "offline", retryInSeconds: 3 });
    vi.advanceTimersByTime(1000);
    expect(statuses.at(-1)).toMatchObject({ status: "offline", retryInSeconds: 2 });

    vi.advanceTimersByTime(2000);
    // A retry after a previous success is "reconnecting", and it remembers when.
    expect(statuses.at(-1)).toMatchObject({ status: "reconnecting" });
    expect(sockets).toHaveLength(2);
    client.close();
  });

  it("stops for good when the session family is revoked", () => {
    vi.useFakeTimers();
    const { client, sockets, statuses } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    sockets[0]?.drop(CLOSE_SESSION_REVOKED);

    expect(statuses.at(-1)).toEqual({ status: "closed", reason: "revoked" });
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });
});

describe("fullJitterDelay", () => {
  it("stays within the exponential ceiling and the 30 s cap", () => {
    expect(fullJitterDelay(0, () => 1)).toBe(1000);
    expect(fullJitterDelay(3, () => 1)).toBe(8000);
    expect(fullJitterDelay(20, () => 1)).toBe(30_000);
    expect(fullJitterDelay(5, () => 0)).toBe(0);
  });
});

describe("parseServerFrame", () => {
  it("ignores an unrecognised type rather than treating it as fatal", () => {
    expect(parseServerFrame(JSON.stringify({ type: "something_new", data: {} }))).toBeNull();
  });

  it("ignores malformed JSON", () => {
    expect(parseServerFrame("{not json")).toBeNull();
  });

  it("requires chan on a channel-scoped event", () => {
    expect(
      parseServerFrame(JSON.stringify({ type: "message_created", data: { message: { id: "m" } } })),
    ).toBeNull();
  });

  it("reads a presence event only for the three known states", () => {
    expect(
      parseServerFrame(
        JSON.stringify({ type: "presence", chan: "c", data: { user_id: "u", state: "away" } }),
      ),
    ).toEqual({ type: "presence", chan: "c", userId: "u", state: "away" });
    expect(
      parseServerFrame(
        JSON.stringify({ type: "presence", chan: "c", data: { user_id: "u", state: "busy" } }),
      ),
    ).toBeNull();
  });
});
