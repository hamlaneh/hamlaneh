import { act, renderHook, waitFor } from "@testing-library/react";
import { delay, http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { resetMockAuth } from "../mocks/handlers";
import { server } from "../mocks/node";
import type { MlsState } from "../mls/types";
import { initialMlsState } from "../mls/types";
import type { MlsController } from "../mls/useMls";
import { useMls } from "../mls/useMls";
import type { Channel, UserSummary } from "./types";
import { useChat } from "./useChat";

/**
 * The ordering the mocks hid, and the render churn that rode along with it.
 *
 * Both were found by the real stack and neither could have been found here
 * before: every other test in this suite gets the channel list back before the
 * first effect settles, because MSW answers instantly. On a real network the
 * route names a channel before `GET /channels` has answered, and that gap is
 * the whole bug — so the list is deliberately slow here.
 */

const ME: UserSummary = { id: "u-me", username: "me", display_name: "Me" };
const CHANNEL_ID = "00000000-0000-4000-8000-0000000000e1";

const ENCRYPTED_CHANNEL: Channel = {
  id: CHANNEL_ID,
  kind: "private",
  e2ee: true,
  slug: "sealed",
  topic: "",
  member_count: 2,
  unread_count: 0,
  mention_count: 0,
  created_at: "2026-08-29T09:00:00.000Z",
  created_by: ME.id,
};

/**
 * A socket that answers its own handshake.
 *
 * The suite's WebSocket mock would do, but this hook is being tested for
 * *ordering*, and a transport that reaches `online` in a known number of
 * microtasks is the only way to say "once, not once per render" and mean it.
 */
class SelfAnsweringSocket {
  static readonly OPEN = 1;
  readyState = 0;
  private readonly listeners = new Map<string, Set<(event: unknown) => void>>();
  /**
   * The server half of the heartbeat, which is not optional here. The client
   * drops a socket that has been silent for two and a half intervals — 75 s —
   * so without this every test that advances further than that would be
   * measuring a reconnect loop rather than whatever it set out to measure.
   */
  private heartbeat: ReturnType<typeof setInterval> | null = null;

  constructor() {
    queueMicrotask(() => {
      this.readyState = SelfAnsweringSocket.OPEN;
      this.emit("open", {});
    });
  }

  addEventListener(type: string, listener: (event: unknown) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  removeEventListener(type: string, listener: (event: unknown) => void): void {
    this.listeners.get(type)?.delete(listener);
  }

  send(data: string): void {
    const frame = JSON.parse(data) as { type?: string };
    if (frame.type !== "hello") {
      return;
    }
    queueMicrotask(() => {
      this.emit("message", {
        data: JSON.stringify({
          type: "hello_ok",
          ts: new Date().toISOString(),
          data: {
            protocol_version: 1,
            user_id: ME.id,
            session_family_id: "00000000-0000-4000-8000-00000000f001",
            heartbeat_interval_seconds: 30,
            max_frame_bytes: 65536,
            resumed: [],
            resync: [],
          },
        }),
      });
      this.heartbeat = setInterval(() => {
        this.emit("message", { data: JSON.stringify({ type: "ping" }) });
      }, 30_000);
    });
  }

  close(): void {
    this.readyState = 3;
    if (this.heartbeat !== null) {
      clearInterval(this.heartbeat);
      this.heartbeat = null;
    }
  }

  /** Pushes one server frame at the client, exactly as the wire would. */
  deliver(frame: unknown): void {
    this.emit("message", { data: JSON.stringify(frame) });
  }

  private emit(type: string, event: unknown): void {
    for (const listener of [...(this.listeners.get(type) ?? [])]) {
      listener(event);
    }
  }
}

/** How many times a spy has been called, without fighting the mock types. */
function calls(spy: unknown): number {
  return (spy as { mock: { calls: unknown[] } }).mock.calls.length;
}

/** Every method a spy; the hook only ever calls, never reads a result. */
function spyController(channels: MlsState["channels"] = {}): MlsController {
  return {
    state: { ...initialMlsState, channels },
    openChannel: vi.fn(),
    syncChannel: vi.fn(),
    syncWelcomes: vi.fn(),
    memberAdded: vi.fn(),
    memberRemoved: vi.fn(),
    encrypt: vi.fn(() => Promise.resolve(null)),
    rememberSent: vi.fn(),
    decryptAll: vi.fn(),
    bodyOf: vi.fn(() => ({ kind: "plaintext", text: "" }) as const),
  };
}

/** The channel list, answered only after the hook has already mounted. */
function slowChannelList(channels: Channel[], ms = 40) {
  server.use(
    http.get("/api/v1/channels", async () => {
      await delay(ms);
      return HttpResponse.json({ channels });
    }),
    http.get("/api/v1/channels/:channelId/messages", () =>
      HttpResponse.json({ messages: [] }),
    ),
    http.put("/api/v1/channels/:channelId/read", () => new HttpResponse(null, { status: 204 })),
  );
}

/** The socket the hook is currently holding, for tests that push a frame. */
let liveSocket: SelfAnsweringSocket | null = null;

/**
 * Hoisted, and it has to be: the realtime overrides are effect dependencies,
 * so a fresh object per render tears the socket down and rebuilds it, which
 * dispatches a connection status, which renders again. Building it inline was
 * an infinite loop that ran the worker out of memory.
 */
const REALTIME = {
  url: "ws://localhost/api/v1/ws",
  socketFactory: () => {
    liveSocket = new SelfAnsweringSocket();
    return liveSocket as unknown as WebSocket;
  },
  retryDelayMs: () => 10_000,
};

/**
 * Renders through a props object so a test can hand the hook a new MLS state
 * mid-flight. The spies are shared across controllers on purpose: a status
 * change must be visible as continuing counts, not as a fresh set of mocks.
 */
function renderChat(mls: MlsController) {
  return renderHook(
    ({ controller }: { controller: MlsController }) =>
      useChat({
        currentUser: ME,
        channelId: CHANNEL_ID,
        focusMessageId: undefined,
        callsEnabled: false,
        mls: controller,
        realtime: REALTIME,
      }),
    { initialProps: { controller: mls } },
  );
}

/** The same spies, reporting a different channel state. */
function withStatus(mls: MlsController, channels: MlsState["channels"]): MlsController {
  return { ...mls, state: { ...mls.state, channels } };
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
  resetMockAuth();
});

afterAll(() => {
  server.close();
});

describe("opening an encrypted channel on a cold load", () => {
  it("bootstraps the group once the channel list arrives, not before", async () => {
    const mls = spyController();
    slowChannelList([ENCRYPTED_CHANNEL]);

    const { unmount } = renderChat(mls);

    // The effect that used to do this ran at mount, asked a ref whether the
    // channel was encrypted, got "no" from a list that had not arrived, and
    // never ran again — the callback it asked is stable, so the list landing
    // re-rendered but re-triggered nothing. The screen then sat on "setting
    // up encryption" forever with not one MLS request for the channel.
    await waitFor(() => {
      expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
    });
    unmount();
  });

  it("leaves a plaintext channel alone", async () => {
    const mls = spyController();
    slowChannelList([{ ...ENCRYPTED_CHANNEL, e2ee: false }]);

    const { unmount } = renderChat(mls);

    await waitFor(() => {
      expect(mls.decryptAll).toHaveBeenCalled();
    });
    expect(mls.openChannel).not.toHaveBeenCalled();
    expect(mls.syncWelcomes).not.toHaveBeenCalled();
    unmount();
  });

  it("fetches the welcome list once, not once per render", async () => {
    const mls = spyController();
    slowChannelList([ENCRYPTED_CHANNEL]);

    const { rerender, unmount } = renderChat(mls);

    await waitFor(() => {
      expect(mls.syncWelcomes).toHaveBeenCalled();
    });
    // Renders the hook cannot control: the list, the socket, the history page,
    // a parent re-rendering for its own reasons. None of them is a reason to
    // refetch — the trace that found this showed four fetches for one page
    // load, one per render. The same controller each time, so what is being
    // re-rendered is genuinely nothing.
    rerender({ controller: mls });
    rerender({ controller: mls });
    rerender({ controller: mls });
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(mls.syncWelcomes).toHaveBeenCalledTimes(1);
    unmount();
  });
});

describe("a group that could not be finished in one go", () => {
  /**
   * The second half of the same real-stack failure, and the one that cost the
   * e2e its first green run: the client that creates the group cannot add a
   * member who has never opened the app, because that member has published no
   * key packages — and no event ever tells the group that a device appeared.
   * Whoever is outside stays outside until somebody asks again.
   */
  it("re-reconciles while somebody still cannot be added", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const mls = spyController({
        [CHANNEL_ID]: { status: "incomplete", unreachableUserIds: ["u-newcomer"] },
      });
      slowChannelList([ENCRYPTED_CHANNEL]);

      const { unmount } = renderChat(mls);
      await waitFor(() => {
        expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
      });
      // One sync already happened when the socket came up — the retry has to
      // be counted on top of it, not confused with it.
      const before = calls(mls.syncChannel);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      // syncChannel, not openChannel: this client can already send, and
      // going back through "opening" would disable its composer on a timer.
      // Five seconds is the *ceiling* of the first wait rather than its
      // length — full jitter rolls somewhere inside it — so this window
      // always contains the first ask and may contain the second.
      expect(mls.syncChannel).toHaveBeenCalledWith(CHANNEL_ID);
      expect(calls(mls.syncChannel)).toBeGreaterThan(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  /**
   * The third state, and the one that had no way out at all. `failed` is what
   * a directory page that 5xx'd or a spent commit-retry budget leaves behind —
   * transient causes with permanent-looking consequences, because until this
   * only a commit nudge, a member event, a reconnect or a reopen re-drove it.
   */
  it("re-drives a channel whose setup failed outright", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const mls = spyController({ [CHANNEL_ID]: { status: "failed" } });
      slowChannelList([ENCRYPTED_CHANNEL]);

      const { unmount } = renderChat(mls);
      await waitFor(() => {
        expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
      });
      const before = calls(mls.syncChannel);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(calls(mls.syncChannel)).toBeGreaterThan(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("re-asks for its welcome while it waits, since nothing else can arrive", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const mls = spyController({ [CHANNEL_ID]: { status: "waiting" } });
      slowChannelList([ENCRYPTED_CHANNEL]);

      const { unmount } = renderChat(mls);
      await waitFor(() => {
        expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
      });
      const before = calls(mls.syncWelcomes);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      // The Welcome list, not the group: only a Welcome can move this state,
      // and the nudge that announces one reaches sockets — so one sent while
      // this client was reconnecting is simply gone.
      expect(calls(mls.syncWelcomes)).toBeGreaterThan(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("never starts asking about a group that is already whole", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const mls = spyController({ [CHANNEL_ID]: { status: "ready" } });
      slowChannelList([ENCRYPTED_CHANNEL]);

      const { unmount } = renderChat(mls);
      await waitFor(() => {
        expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
      });

      const before = calls(mls.syncChannel);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      // Four retry windows have passed and nothing asked again.
      expect(calls(mls.syncChannel)).toBe(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

/**
 * The wait between asks, which is the whole cost of the retry.
 *
 * Every test here rolls the jitter at 1 so each wait is exactly its ceiling.
 * That makes the growth readable as a count of asks in a window; the jitter
 * itself is `fullJitterDelay`'s to prove, and realtime.test.ts does.
 */
describe("the retry's backoff", () => {
  const INCOMPLETE: MlsState["channels"] = {
    [CHANNEL_ID]: { status: "incomplete", unreachableUserIds: ["u-newcomer"] },
  };

  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.spyOn(Math, "random").mockReturnValue(1);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  /** Renders, and waits until the channel's group setup has been driven once. */
  async function renderStuck(channels: MlsState["channels"]) {
    const mls = spyController(channels);
    slowChannelList([ENCRYPTED_CHANNEL]);
    const rendered = renderChat(mls);
    await waitFor(() => {
      expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
    });
    return { mls, ...rendered };
  }

  it("widens the wait instead of asking every five seconds for weeks", async () => {
    const { mls, unmount } = await renderStuck(INCOMPLETE);

    const before = calls(mls.syncChannel);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(155_000);
    });
    // 5 + 10 + 20 + 40 + 80 s. The flat interval this replaced asked
    // thirty-one times in the same window — and kept that rate up for as long
    // as the condition lasted, which for one invited person who never signs in
    // is weeks, across every online member's client.
    expect(calls(mls.syncChannel) - before).toBe(5);

    // Half an hour more, with the doubling now into its ceiling: ten asks at
    // three minutes each. Uncapped, the same window holds three.
    const atCeiling = calls(mls.syncChannel);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_800_000);
    });
    expect(calls(mls.syncChannel) - atCeiling).toBeGreaterThanOrEqual(9);
    unmount();
  });

  it("starts the wait over when the state actually moves", async () => {
    const { mls, rerender, unmount } = await renderStuck({
      [CHANNEL_ID]: { status: "failed" },
    });

    // 5 + 10 + 20 s of asking: the next one would be 40 s out.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(35_000);
    });
    rerender({ controller: withStatus(mls, INCOMPLETE) });

    const before = calls(mls.syncChannel);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    // A different condition is a different question, so it is asked at the
    // floor. Carrying the old attempt count over — by holding it outside the
    // effect — would leave this window silent.
    expect(calls(mls.syncChannel)).toBeGreaterThan(before);
    unmount();
  });

  it("starts the wait over when a commit says something has changed", async () => {
    const { mls, unmount } = await renderStuck(INCOMPLETE);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(35_000);
    });
    expect(liveSocket).not.toBeNull();
    await act(async () => {
      liveSocket?.deliver({ type: "mls_commit", chan: CHANNEL_ID, data: { epoch: 2 } });
      await Promise.resolve();
    });

    // Counted after the nudge's own immediate sync, so what is measured is the
    // timer and not the frame.
    const before = calls(mls.syncChannel);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    // Backing off is for a cause that has not changed. This one just did.
    expect(calls(mls.syncChannel)).toBeGreaterThan(before);
    unmount();
  });

  it("stops asking the moment the state leaves the retried set", async () => {
    const { mls, rerender, unmount } = await renderStuck(INCOMPLETE);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(calls(mls.syncChannel)).toBeGreaterThan(0);
    rerender({ controller: withStatus(mls, { [CHANNEL_ID]: { status: "ready" } }) });

    const before = calls(mls.syncChannel);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(600_000);
    });
    expect(calls(mls.syncChannel)).toBe(before);
    unmount();
  });
});

describe("the controller itself", () => {
  it("keeps its identity across renders", () => {
    // The other half of the refetch loop: a fresh object literal here re-fired
    // every effect that depends on the controller, once per render.
    const { result, rerender } = renderHook(() => useMls(ME.id));
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});
