import type { Channel, ConnectionState, Message, Presence, UserSummary } from "./types";

/**
 * Client half of docs/api/ws-protocol.md.
 *
 * Responsibilities, and only these: open the socket, run the `hello`
 * handshake with per-channel resume cursors, answer heartbeats, forward
 * frames, and reconnect with exponential backoff plus full jitter. It owns no
 * chat state — the store upserts by message id, and the socket is only a fast
 * delivery path (§4). Sending, editing, deleting and read positions are REST.
 */

const PROTOCOL_VERSION = 1;

/** ws-protocol.md §8. */
export const CLOSE_NORMAL = 1000;
export const CLOSE_PROTOCOL_ERROR = 4400;
export const CLOSE_SESSION_REVOKED = 4401;
export const CLOSE_HEARTBEAT_TIMEOUT = 4408;

const DEFAULT_HEARTBEAT_SECONDS = 30;
/** ws-protocol.md §6: two missed pings (~75 s) is a dead socket. */
const HEARTBEAT_MISS_FACTOR = 2.5;
/**
 * The attempt number at which full jitter already saturates the 30 s ceiling.
 * A 4400 is a client bug (ws-protocol.md §8): it must not be blind-retried, so
 * the next attempt starts at the far end of the backoff rather than at zero.
 */
const SATURATED_ATTEMPT = 5;

interface ResumeCursor {
  chan: string;
  seq: number;
}

export interface HelloOkData {
  protocol_version: number;
  user_id: string;
  session_family_id: string;
  heartbeat_interval_seconds: number;
  max_frame_bytes: number;
  resumed: ResumeCursor[];
  /** Channels whose replay buffer could not satisfy the resume — backfill over REST. */
  resync: string[];
}

export type ServerFrame =
  | { type: "hello_ok"; data: HelloOkData }
  | { type: "subscribed" | "unsubscribed"; chan: string }
  | { type: "message_created" | "message_updated" | "message_deleted"; chan: string; seq?: number; message: Message }
  | { type: "channel_created" | "channel_updated"; chan?: string; seq?: number; channel: Channel }
  | { type: "member_added"; chan: string; seq?: number; user: UserSummary }
  | { type: "read_position"; chan: string; messageId: string }
  | { type: "typing"; chan: string; userId: string }
  | { type: "presence"; chan: string; userId: string; state: Presence }
  | { type: "resync"; chan: string }
  | { type: "error"; code: string; message: string };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readString(source: Record<string, unknown>, key: string): string | null {
  const value = source[key];
  return typeof value === "string" ? value : null;
}

function readResumeCursors(value: unknown): ResumeCursor[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const cursors: ResumeCursor[] = [];
  for (const entry of value) {
    if (isRecord(entry) && typeof entry.chan === "string" && typeof entry.seq === "number") {
      cursors.push({ chan: entry.chan, seq: entry.seq });
    }
  }
  return cursors;
}

/**
 * Parses one inbound text frame.
 *
 * Returns null for anything this client does not recognise. That is the
 * protocol's rule, not laziness: an unknown `type` is ignored and the socket
 * stays open, which is what lets a newer server talk to an older client.
 */
export function parseServerFrame(raw: string): ServerFrame | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isRecord(parsed)) {
    return null;
  }
  const type = readString(parsed, "type");
  const data = isRecord(parsed.data) ? parsed.data : {};
  const chan = readString(parsed, "chan");
  const seq = typeof parsed.seq === "number" ? parsed.seq : undefined;

  switch (type) {
    case "hello_ok":
      return {
        type: "hello_ok",
        data: {
          protocol_version:
            typeof data.protocol_version === "number" ? data.protocol_version : PROTOCOL_VERSION,
          user_id: readString(data, "user_id") ?? "",
          session_family_id: readString(data, "session_family_id") ?? "",
          heartbeat_interval_seconds:
            typeof data.heartbeat_interval_seconds === "number"
              ? data.heartbeat_interval_seconds
              : DEFAULT_HEARTBEAT_SECONDS,
          max_frame_bytes: typeof data.max_frame_bytes === "number" ? data.max_frame_bytes : 65536,
          resumed: readResumeCursors(data.resumed),
          resync: Array.isArray(data.resync)
            ? data.resync.filter((entry): entry is string => typeof entry === "string")
            : [],
        },
      };
    case "subscribed":
    case "unsubscribed":
      return chan === null ? null : { type, chan };
    case "message_created":
    case "message_updated":
    case "message_deleted": {
      const message = data.message;
      if (chan === null || !isRecord(message) || typeof message.id !== "string") {
        return null;
      }
      return { type, chan, ...(seq === undefined ? {} : { seq }), message: message as unknown as Message };
    }
    case "channel_created":
    case "channel_updated": {
      const channel = data.channel;
      if (!isRecord(channel) || typeof channel.id !== "string") {
        return null;
      }
      return {
        type,
        ...(chan === null ? {} : { chan }),
        ...(seq === undefined ? {} : { seq }),
        channel: channel as unknown as Channel,
      };
    }
    case "member_added": {
      const user = data.user;
      if (chan === null || !isRecord(user) || typeof user.id !== "string") {
        return null;
      }
      return { type, chan, ...(seq === undefined ? {} : { seq }), user: user as unknown as UserSummary };
    }
    case "read_position": {
      const target = chan ?? readString(data, "chan");
      const messageId = readString(data, "message_id");
      return target === null || messageId === null
        ? null
        : { type, chan: target, messageId };
    }
    case "typing": {
      const userId = readString(data, "user_id");
      return chan === null || userId === null ? null : { type, chan, userId };
    }
    case "presence": {
      const userId = readString(data, "user_id");
      const state = readString(data, "state");
      if (chan === null || userId === null || state === null) {
        return null;
      }
      if (state !== "online" && state !== "away" && state !== "offline") {
        return null;
      }
      return { type, chan, userId, state };
    }
    case "resync": {
      const target = chan ?? readString(data, "chan");
      return target === null ? null : { type, chan: target };
    }
    case "error":
      return {
        type: "error",
        code: readString(data, "code") ?? "unknown",
        message: readString(data, "message") ?? "",
      };
    default:
      // Includes ping/pong, which are handled before parsing reaches here.
      return null;
  }
}

/** Exponential backoff with full jitter: 1 s base, 30 s cap (ws-protocol.md §8). */
export function fullJitterDelay(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(30_000, 1000 * 2 ** Math.max(0, attempt));
  return Math.round(random() * ceiling);
}

export interface RealtimeOptions {
  url: string;
  /** Recognised frames, in arrival order. */
  onFrame: (frame: ServerFrame) => void;
  /** Every connection-state change the banner and composer react to. */
  onStatus: (state: ConnectionState) => void;
  /** A channel whose replay buffer fell short — backfill it over REST. */
  onResync: (channelId: string) => void;
  /** Overridable for tests; production uses the platform WebSocket. */
  socketFactory?: (url: string) => WebSocket;
  /** Overridable for tests; production uses full-jitter exponential backoff. */
  retryDelayMs?: (attempt: number) => number;
}

export class RealtimeClient {
  private readonly options: RealtimeOptions;
  private socket: WebSocket | null = null;
  private readonly seqByChannel = new Map<string, number>();
  private readonly subscriptions = new Set<string>();
  private attempt = 0;
  private lastConnectedAt: string | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private countdownTimer: ReturnType<typeof setInterval> | null = null;
  private watchdogTimer: ReturnType<typeof setTimeout> | null = null;
  private heartbeatSeconds = DEFAULT_HEARTBEAT_SECONDS;
  private stopped = false;
  private correlation = 0;

  constructor(options: RealtimeOptions) {
    this.options = options;
  }

  /** Opens the socket, or does nothing when one is already open or opening. */
  connect(): void {
    if (this.stopped || this.socket !== null) {
      return;
    }
    this.clearRetry();
    this.options.onStatus(
      this.lastConnectedAt === null
        ? { status: "connecting" }
        : { status: "reconnecting", lastConnectedAt: this.lastConnectedAt },
    );

    const create = this.options.socketFactory ?? ((url: string) => new WebSocket(url));
    let socket: WebSocket;
    try {
      socket = create(this.options.url);
    } catch (error) {
      console.warn("Realtime socket could not be created:", error);
      this.scheduleRetry();
      return;
    }
    this.socket = socket;
    socket.addEventListener("open", this.handleOpen);
    socket.addEventListener("message", this.handleMessage);
    socket.addEventListener("close", this.handleClose);
    socket.addEventListener("error", this.handleError);
  }

  /** Closes for good: no further attempts until a new client is created. */
  close(): void {
    this.stopped = true;
    this.clearRetry();
    this.clearWatchdog();
    this.detach(CLOSE_NORMAL);
    this.options.onStatus({ status: "closed", reason: "normal" });
  }

  subscribe(channelId: string): void {
    this.subscriptions.add(channelId);
    this.send({ type: "subscribe", chan: channelId });
  }

  unsubscribe(channelId: string): void {
    this.subscriptions.delete(channelId);
    // Nothing will resume a channel this client no longer follows, and a map
    // that only ever grows is a slow leak for a long-lived tab.
    this.seqByChannel.delete(channelId);
    this.send({ type: "unsubscribe", chan: channelId });
  }

  /** Highest processed seq per channel, replayed as the resume cursor. */
  private recordSeq(chan: string | undefined, seq: number | undefined): void {
    if (chan === undefined || seq === undefined) {
      return;
    }
    const known = this.seqByChannel.get(chan) ?? 0;
    if (seq > known) {
      this.seqByChannel.set(chan, seq);
    }
  }

  private send(frame: { type: string; chan?: string; data?: Record<string, unknown> }): void {
    const socket = this.socket;
    if (socket?.readyState !== WebSocket.OPEN) {
      return;
    }
    this.correlation += 1;
    socket.send(
      JSON.stringify({
        type: frame.type,
        id: `c${String(this.correlation)}`,
        ...(frame.chan === undefined ? {} : { chan: frame.chan }),
        ts: new Date().toISOString(),
        data: frame.data ?? {},
      }),
    );
  }

  private readonly handleOpen = (): void => {
    // The backoff is NOT reset here: a transport that opens and is then closed
    // by the server (4400, 4429) would otherwise retry from zero forever. It
    // resets on hello_ok, the point at which the connection actually worked.
    const resume = [...this.seqByChannel.entries()].map(([chan, seq]) => ({ chan, seq }));
    this.send({
      type: "hello",
      data: {
        protocol_version: PROTOCOL_VERSION,
        ...(resume.length === 0 ? {} : { resume }),
      },
    });
  };

  private readonly handleMessage = (event: MessageEvent<unknown>): void => {
    if (typeof event.data !== "string") {
      // Binary frames are a protocol error; ignore rather than misread them.
      return;
    }
    this.armWatchdog();

    // ping/pong short-circuit: heartbeats are transport, not chat events.
    if (event.data.includes('"ping"')) {
      const probe: unknown = safeParse(event.data);
      if (isRecord(probe) && probe.type === "ping") {
        this.send({ type: "pong" });
        return;
      }
    }

    const frame = parseServerFrame(event.data);
    if (frame === null) {
      return;
    }

    if (frame.type === "hello_ok") {
      // The handshake completed, so this connection is genuinely good.
      this.attempt = 0;
      this.heartbeatSeconds = frame.data.heartbeat_interval_seconds;
      this.lastConnectedAt = new Date().toISOString();
      this.armWatchdog();
      this.options.onStatus({ status: "online" });
      for (const channelId of this.subscriptions) {
        this.send({ type: "subscribe", chan: channelId });
      }
      for (const channelId of frame.data.resync) {
        this.seqByChannel.delete(channelId);
        this.options.onResync(channelId);
      }
      this.options.onFrame(frame);
      return;
    }

    if (frame.type === "resync") {
      this.seqByChannel.delete(frame.chan);
      this.options.onResync(frame.chan);
      return;
    }

    // Handled first, recorded second: a seq marked processed before the
    // handler ran would silently open a resume gap if the handler threw.
    this.options.onFrame(frame);
    if ("seq" in frame) {
      this.recordSeq(frame.chan, frame.seq);
    }
  };

  private readonly handleError = (): void => {
    // "error" always precedes "close" on a failed socket; the close handler
    // owns the reconnect decision, so there is nothing to do here but keep
    // the event from surfacing as an unhandled one.
  };

  private readonly handleClose = (event: CloseEvent): void => {
    this.detach();
    this.clearWatchdog();
    if (this.stopped) {
      return;
    }
    if (event.code === CLOSE_SESSION_REVOKED) {
      // §7: the session family is gone. Retrying is useless and rate-limited.
      this.stopped = true;
      this.options.onStatus({ status: "closed", reason: "revoked" });
      return;
    }
    if (event.code === CLOSE_NORMAL) {
      this.stopped = true;
      this.options.onStatus({ status: "closed", reason: "normal" });
      return;
    }
    if (event.code === CLOSE_PROTOCOL_ERROR) {
      // §8: a client bug, never blind-retried. Log it, and jump the backoff to
      // its ceiling so a persistent one costs the server nothing.
      console.warn("Realtime socket closed on a protocol error (4400).");
      this.attempt = Math.max(this.attempt, SATURATED_ATTEMPT);
    }
    this.scheduleRetry();
  };

  private detach(closeCode?: number): void {
    const socket = this.socket;
    this.socket = null;
    if (socket === null) {
      return;
    }
    socket.removeEventListener("open", this.handleOpen);
    socket.removeEventListener("message", this.handleMessage);
    socket.removeEventListener("close", this.handleClose);
    socket.removeEventListener("error", this.handleError);
    if (closeCode !== undefined && socket.readyState <= WebSocket.OPEN) {
      socket.close(closeCode);
    }
  }

  /**
   * A socket through a dead proxy can look open for minutes. If nothing
   * arrives within two and a half heartbeat intervals, drop it and reconnect
   * rather than trusting the silence.
   */
  private armWatchdog(): void {
    this.clearWatchdog();
    this.watchdogTimer = setTimeout(() => {
      this.detach(CLOSE_HEARTBEAT_TIMEOUT);
      if (!this.stopped) {
        this.scheduleRetry();
      }
    }, this.heartbeatSeconds * HEARTBEAT_MISS_FACTOR * 1000);
  }

  private clearWatchdog(): void {
    if (this.watchdogTimer !== null) {
      clearTimeout(this.watchdogTimer);
      this.watchdogTimer = null;
    }
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    if (this.countdownTimer !== null) {
      clearInterval(this.countdownTimer);
      this.countdownTimer = null;
    }
  }

  /**
   * Waits out the backoff, publishing the countdown the offline banner draws
   * ("No connection. Retrying in 8 s.") — the client owns the schedule and
   * shows it rather than hiding it.
   */
  private scheduleRetry(): void {
    this.clearRetry();
    const delay = (this.options.retryDelayMs ?? fullJitterDelay)(this.attempt);
    this.attempt += 1;

    let remaining = Math.max(1, Math.ceil(delay / 1000));
    this.options.onStatus({
      status: "offline",
      retryInSeconds: remaining,
      lastConnectedAt: this.lastConnectedAt,
    });

    if (delay >= 1000) {
      this.countdownTimer = setInterval(() => {
        remaining -= 1;
        if (remaining <= 0) {
          return;
        }
        this.options.onStatus({
          status: "offline",
          retryInSeconds: remaining,
          lastConnectedAt: this.lastConnectedAt,
        });
      }, 1000);
    }

    this.retryTimer = setTimeout(() => {
      this.clearRetry();
      this.connect();
    }, delay);
  }
}

function safeParse(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

/** Same-origin WebSocket URL for the contract's upgrade endpoint. */
export function realtimeUrl(location: Location = window.location): string {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}/api/v1/ws`;
}
