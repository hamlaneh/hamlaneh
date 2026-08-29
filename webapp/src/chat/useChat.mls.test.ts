import { act, renderHook, waitFor } from "@testing-library/react";
import { delay, http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";

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
    });
  }

  close(): void {
    this.readyState = 3;
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

/**
 * Hoisted, and it has to be: the realtime overrides are effect dependencies,
 * so a fresh object per render tears the socket down and rebuilds it, which
 * dispatches a connection status, which renders again. Building it inline was
 * an infinite loop that ran the worker out of memory.
 */
const REALTIME = {
  url: "ws://localhost/api/v1/ws",
  socketFactory: () => new SelfAnsweringSocket() as unknown as WebSocket,
  retryDelayMs: () => 10_000,
};

function renderChat(mls: MlsController) {
  return renderHook(() =>
    useChat({
      currentUser: ME,
      channelId: CHANNEL_ID,
      focusMessageId: undefined,
      callsEnabled: false,
      mls,
      realtime: REALTIME,
    }),
  );
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
    // load, one per render.
    rerender();
    rerender();
    rerender();
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
      expect(mls.syncChannel).toHaveBeenCalledWith(CHANNEL_ID);
      expect(calls(mls.syncChannel)).toBeGreaterThan(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("re-asks whether it has been added yet while it waits", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const mls = spyController({ [CHANNEL_ID]: { status: "waiting" } });
      slowChannelList([ENCRYPTED_CHANNEL]);

      const { unmount } = renderChat(mls);
      await waitFor(() => {
        expect(mls.openChannel).toHaveBeenCalledWith(CHANNEL_ID);
      });
      const before = calls(mls.openChannel);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      // A Welcome nudge reaches sockets, so one sent while this client was
      // reconnecting is simply gone; asking again is how it finds out.
      expect(calls(mls.openChannel)).toBeGreaterThan(before);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops asking once the group is whole", async () => {
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
