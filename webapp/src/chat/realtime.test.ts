import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CLOSE_HEARTBEAT_TIMEOUT,
  CLOSE_PROTOCOL_ERROR,
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
  /** The attempt number handed to the backoff, once per scheduled retry. */
  attempts: number[];
}

interface HarnessOptions {
  onFrame?: (type: string) => void;
}

function harness(options: HarnessOptions = {}): Harness {
  const sockets: FakeSocket[] = [];
  const statuses: ConnectionState[] = [];
  const frames: string[] = [];
  const resyncs: string[] = [];
  const attempts: number[] = [];
  const client = new RealtimeClient({
    url: "ws://localhost/api/v1/ws",
    socketFactory: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket as unknown as WebSocket;
    },
    retryDelayMs: (attempt) => {
      attempts.push(attempt);
      return 0;
    },
    onFrame: (frame) => {
      frames.push(frame.type);
      options.onFrame?.(frame.type);
    },
    onStatus: (status) => statuses.push(status),
    onResync: (channelId) => resyncs.push(channelId),
  });
  return { client, sockets, statuses, frames, resyncs, attempts };
}

/** The `type` of every frame the client has sent on a socket. */
function sentTypes(socket: FakeSocket | undefined): string[] {
  return (socket?.sent ?? []).map((raw) => (JSON.parse(raw) as { type: string }).type);
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

    expect(sentTypes(sockets[0])).toContain("pong");
    client.close();
  });

  it("re-subscribes every channel it was following on the new socket", () => {
    vi.useFakeTimers();
    const { client, sockets } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    client.subscribe("chan-a");
    client.subscribe("chan-b");

    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);
    sockets[1]?.open();
    sockets[1]?.deliver(helloOk());

    // hello first, then one subscribe per channel: a reconnect that forgot to
    // re-subscribe would look connected and deliver nothing.
    expect(sentTypes(sockets[1])).toEqual(["hello", "subscribe", "subscribe"]);
    const channels = (sockets[1]?.sent ?? [])
      .map((raw) => (JSON.parse(raw) as { chan?: string }).chan)
      .filter((chan) => chan !== undefined);
    expect(channels).toEqual(["chan-a", "chan-b"]);
    client.close();
  });

  it("stops resuming a channel it no longer follows", () => {
    vi.useFakeTimers();
    const { client, sockets } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    client.subscribe("chan-a");
    sockets[0]?.deliver({
      type: "message_created",
      chan: "chan-a",
      seq: 12,
      ts: "2026-08-21T09:01:00Z",
      data: { message: { id: "m1" } },
    });
    client.unsubscribe("chan-a");

    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);
    sockets[1]?.open();

    const hello = JSON.parse(sockets[1]?.sent[0] ?? "{}") as {
      data: { resume?: unknown };
    };
    expect(hello.data.resume).toBeUndefined();
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

  it("drops the resume cursor when a resync arrives mid-socket", () => {
    vi.useFakeTimers();
    const { client, sockets, resyncs } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    sockets[0]?.deliver({
      type: "message_created",
      chan: "chan-a",
      seq: 77,
      ts: "2026-08-21T09:01:00Z",
      data: { message: { id: "m1" } },
    });
    sockets[0]?.deliver({ type: "resync", chan: "chan-a", ts: "2026-08-21T09:02:00Z", data: {} });

    expect(resyncs).toEqual(["chan-a"]);
    // A resync means the buffer fell short, so resuming from the old seq
    // would silently skip whatever REST is about to fetch.
    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);
    sockets[1]?.open();
    const hello = JSON.parse(sockets[1]?.sent[0] ?? "{}") as { data: { resume?: unknown } };
    expect(hello.data.resume).toBeUndefined();
    client.close();
  });

  it("does not mark a seq processed when the handler throws", () => {
    vi.useFakeTimers();
    const { client, sockets } = harness({
      onFrame: (type) => {
        if (type === "message_created") {
          throw new Error("the reducer blew up");
        }
      },
    });
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    expect(() => {
      sockets[0]?.deliver({
        type: "message_created",
        chan: "chan-a",
        seq: 500,
        ts: "2026-08-21T09:01:00Z",
        data: { message: { id: "m1" } },
      });
    }).toThrow();

    // Resuming from 500 would tell the server the client has it, which it
    // does not — the frame never made it into the store.
    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);
    sockets[1]?.open();
    const hello = JSON.parse(sockets[1]?.sent[0] ?? "{}") as { data: { resume?: unknown } };
    expect(hello.data.resume).toBeUndefined();
    client.close();
  });
});

describe("RealtimeClient watchdog", () => {
  it("drops a socket that went quiet for two and a half heartbeats", () => {
    vi.useFakeTimers();
    const { client, sockets, statuses } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    expect(statuses.at(-1)).toEqual({ status: "online" });

    // A socket through a dead proxy stays "open" indefinitely; only silence
    // gives it away.
    vi.advanceTimersByTime(30 * 2.5 * 1000);
    expect(statuses.at(-1)).toMatchObject({ status: "offline" });

    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
    client.close();
  });

  it("is re-armed by every frame, so a live socket is never dropped", () => {
    vi.useFakeTimers();
    const { client, sockets, statuses } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());

    for (let tick = 0; tick < 4; tick += 1) {
      vi.advanceTimersByTime(30_000);
      sockets[0]?.deliver({ type: "ping", ts: "2026-08-21T09:00:30Z", data: {} });
    }

    expect(statuses.at(-1)).toEqual({ status: "online" });
    expect(sockets).toHaveLength(1);
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

  it("escalates the backoff while the handshake keeps failing", () => {
    vi.useFakeTimers();
    const { client, sockets, attempts } = harness();
    client.connect();

    // A transport that opens and is then closed before hello_ok is exactly
    // the 4429 / 4400 shape: resetting on "open" would retry from zero forever.
    for (let round = 0; round < 3; round += 1) {
      sockets[round]?.open();
      sockets[round]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
      vi.advanceTimersByTime(1);
    }

    expect(attempts).toEqual([0, 1, 2]);
    client.close();
  });

  it("resets the backoff only once the handshake has succeeded", () => {
    vi.useFakeTimers();
    const { client, sockets, attempts } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);

    sockets[1]?.open();
    sockets[1]?.deliver(helloOk());
    sockets[1]?.drop(CLOSE_HEARTBEAT_TIMEOUT);
    vi.advanceTimersByTime(1);

    expect(attempts).toEqual([0, 0]);
    client.close();
  });

  it("jumps the backoff to its ceiling on a protocol error rather than hammering", () => {
    vi.useFakeTimers();
    const { client, sockets, attempts } = harness();
    client.connect();
    sockets[0]?.open();
    sockets[0]?.deliver(helloOk());
    // 4400 is a client bug (ws-protocol.md §8): never blind-retried.
    sockets[0]?.drop(CLOSE_PROTOCOL_ERROR);

    expect(attempts).toEqual([5]);
    expect(fullJitterDelay(attempts[0] ?? 0, () => 1)).toBe(30_000);
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

  it("takes a caller's own base and cap", () => {
    // The MLS retry's shape (useChat): 5 s base, 3 min cap. Doubling from the
    // base has to reach the cap and stop there rather than overshoot it.
    expect(fullJitterDelay(0, () => 1, 5000, 180_000)).toBe(5000);
    expect(fullJitterDelay(3, () => 1, 5000, 180_000)).toBe(40_000);
    expect(fullJitterDelay(6, () => 1, 5000, 180_000)).toBe(180_000);
    expect(fullJitterDelay(99, () => 1, 5000, 180_000)).toBe(180_000);
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
