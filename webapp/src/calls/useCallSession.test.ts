import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { MediaKey } from "../mls/types";
import type { CallKeyState } from "./e2ee";
import { CallEncryptionUnsupportedError } from "./media";
import type { MediaConnect, MediaParticipant, MediaSession } from "./media";
import type { MintTicket } from "./useCallSession";
import { useCallSession } from "./useCallSession";

/**
 * The call session's encryption behaviour, at the seam.
 *
 * `livekit-client` never loads here: `MediaConnect` is the boundary the whole
 * call layer is built around, so these drive a recording double and ask the
 * questions ADR 009 makes security properties — was a key passed at all, was
 * it rotated when the epoch moved, and did this device stop publishing when a
 * human owed a decision.
 */

function keyFor(epoch: number): MediaKey {
  return { epoch, secret: new Uint8Array(32).fill(epoch) };
}

const TICKET = "fixture-call-ticket-not-a-real-one";

function localParticipant(overrides: Partial<MediaParticipant> = {}): MediaParticipant {
  return {
    identity: "me",
    name: "Me",
    isLocal: true,
    speaking: false,
    micEnabled: false,
    cameraEnabled: false,
    screenSharing: false,
    camera: null,
    screen: null,
    microphone: null,
    ...overrides,
  };
}

function fakeMedia(options: { failWith?: Error } = {}) {
  let local = localParticipant();
  const record = {
    connections: [] as (number | null)[],
    rekeyed: [] as number[],
    published: [] as string[],
  };

  const session: MediaSession = {
    participants: () => [local],
    subscribe: () => () => undefined,
    setMicrophoneEnabled: (enabled) => {
      record.published.push(`microphone:${String(enabled)}`);
      local = { ...local, micEnabled: enabled };
      return Promise.resolve();
    },
    setCameraEnabled: (enabled) => {
      record.published.push(`camera:${String(enabled)}`);
      local = { ...local, cameraEnabled: enabled };
      return Promise.resolve();
    },
    setScreenShareEnabled: (enabled) => {
      record.published.push(`screen:${String(enabled)}`);
      local = { ...local, screenSharing: enabled };
      return Promise.resolve();
    },
    setKey: (key) => {
      record.rekeyed.push(key.epoch);
      return Promise.resolve();
    },
    disconnect: () => Promise.resolve(),
  };

  const connect: MediaConnect = (_url, _token, key) => {
    record.connections.push(key?.epoch ?? null);
    if (options.failWith !== undefined) {
      return Promise.reject(options.failWith);
    }
    return Promise.resolve(session);
  };

  return { connect, record, session };
}

/**
 * Drives the hook with a resolver whose answer a test can change mid-call,
 * which is the whole point: an epoch moves and a warning appears while the
 * session stays up.
 */
function setup(
  connect: MediaConnect,
  first: CallKeyState = { kind: "plain" },
  mintTicket: MintTicket = () => Promise.resolve({ token: TICKET, status: 200 }),
) {
  let keys = first;
  const resolve = () => keys;
  const rendered = renderHook(() => useCallSession(connect, mintTicket, resolve));
  return {
    ...rendered,
    /** What encryption now says — the next render picks it up. */
    async say(next: CallKeyState) {
      keys = next;
      await act(async () => {
        rendered.rerender();
        await Promise.resolve();
      });
    },
  };
}

describe("useCallSession and media encryption", () => {
  it("connects without a key when nothing resolves one — the conference path", async () => {
    const media = fakeMedia();
    const { result } = renderHook(() => useCallSession(media.connect, () =>
      Promise.resolve({ token: TICKET, status: 200 }),
    ));

    act(() => {
      result.current.join("Roya");
    });
    await waitFor(() => {
      expect(result.current.status).toBe("connected");
    });

    expect(media.record.connections).toEqual([null]);
    expect(result.current.encrypted).toBe(false);
  });

  it("refuses an encrypted conversation it cannot key, without spending a ticket", async () => {
    const media = fakeMedia();
    const mintTicket = vi.fn<MintTicket>(() => Promise.resolve({ token: TICKET, status: 200 }));
    const { result } = setup(media.connect, { kind: "refused" }, mintTicket);

    act(() => {
      result.current.join("channel-id");
    });

    // Nothing at all happened: no ticket, no connection, no unencrypted
    // retry — and the error says which refusal it was.
    expect(mintTicket).not.toHaveBeenCalled();
    expect(media.record.connections).toEqual([]);
    expect(result.current.status).toBe("idle");
    expect(result.current.encrypted).toBe(false);
    expect(result.current.errorKey).toBe("calls.error.encryption");
  });

  it("says so honestly when the browser cannot encrypt, and does not retry in the clear", async () => {
    const media = fakeMedia({ failWith: new CallEncryptionUnsupportedError() });
    const { result } = setup(media.connect, { kind: "keyed", key: keyFor(1), publishBlocked: false });

    act(() => {
      result.current.join("channel-id");
    });
    await waitFor(() => {
      expect(result.current.errorKey).toBe("calls.error.encryptionUnsupported");
    });

    // One attempt, and it carried a key. A second connection — or a first one
    // with no key — would be the silent downgrade.
    expect(media.record.connections).toEqual([1]);
    expect(result.current.status).toBe("idle");
  });

  it("hands the key to connect, then rotates once per epoch and no more", async () => {
    const media = fakeMedia();
    const call = setup(media.connect, { kind: "keyed", key: keyFor(3), publishBlocked: false });

    act(() => {
      call.result.current.join("channel-id");
    });
    await waitFor(() => {
      expect(call.result.current.status).toBe("connected");
    });

    // The session was born keyed, so nothing rotates at join.
    expect(media.record.connections).toEqual([3]);
    expect(media.record.rekeyed).toEqual([]);
    expect(call.result.current.encrypted).toBe(true);

    // A commit merged: the same epoch resolved again changes nothing.
    await call.say({ kind: "keyed", key: keyFor(3), publishBlocked: false });
    expect(media.record.rekeyed).toEqual([]);

    await call.say({ kind: "keyed", key: keyFor(4), publishBlocked: false });
    await waitFor(() => {
      expect(media.record.rekeyed).toEqual([4]);
    });

    await call.say({ kind: "keyed", key: keyFor(5), publishBlocked: false });
    await waitFor(() => {
      expect(media.record.rekeyed).toEqual([4, 5]);
    });
  });

  it("catches up when a commit lands between the click and the connection", async () => {
    // The join race: `connect` carries the epoch that was current when it was
    // called, and a commit can merge while it is in flight. Without a rotation
    // once the session exists, this call would stay sealed under an epoch
    // everybody else has left — publishing noise, for as long as it lasts.
    let release = () => undefined as void;
    const opened = new Promise<void>((resolve) => {
      release = () => {
        resolve();
      };
    });
    const media = fakeMedia();
    const slow: MediaConnect = async (url, token, key) => {
      await opened;
      return media.connect(url, token, key);
    };
    const call = setup(slow, { kind: "keyed", key: keyFor(9), publishBlocked: false });

    act(() => {
      call.result.current.join("channel-id");
    });
    await call.say({ kind: "keyed", key: keyFor(10), publishBlocked: false });
    await act(async () => {
      release();
      await opened;
    });
    await waitFor(() => {
      expect(call.result.current.status).toBe("connected");
    });

    expect(media.record.connections).toEqual([9]);
    await waitFor(() => {
      expect(media.record.rekeyed).toEqual([10]);
    });
  });

  it("publishes nothing on joining a conversation that needs attention", async () => {
    const media = fakeMedia();
    const call = setup(media.connect, { kind: "keyed", key: keyFor(1), publishBlocked: true });

    act(() => {
      call.result.current.join("channel-id");
    });
    await waitFor(() => {
      expect(call.result.current.status).toBe("connected");
    });

    // Joined and listening — the call is heard — but the microphone that
    // every other join publishes was never turned on.
    expect(media.record.connections).toEqual([1]);
    expect(media.record.published).toEqual([]);
    expect(call.result.current.publishBlocked).toBe(true);

    // Exit taken: verified or accepted, either way the same state clears.
    await call.say({ kind: "keyed", key: keyFor(1), publishBlocked: false });
    await waitFor(() => {
      expect(media.record.published).toEqual(["microphone:true"]);
    });
    expect(call.result.current.publishBlocked).toBe(false);
  });

  it("stops publishing mid-call and restores exactly what was on", async () => {
    const media = fakeMedia();
    const call = setup(media.connect, { kind: "keyed", key: keyFor(1), publishBlocked: false });

    act(() => {
      call.result.current.join("channel-id");
    });
    await waitFor(() => {
      expect(call.result.current.micEnabled).toBe(true);
    });
    act(() => {
      call.result.current.toggleCamera();
    });
    await waitFor(() => {
      expect(call.result.current.cameraEnabled).toBe(true);
    });
    media.record.published.length = 0;

    // Somebody's keys changed while the call is up.
    await call.say({ kind: "keyed", key: keyFor(1), publishBlocked: true });
    await waitFor(() => {
      expect(media.record.published).toEqual([
        "microphone:false",
        "camera:false",
        "screen:false",
      ]);
    });
    expect(call.result.current.publishBlocked).toBe(true);

    // The controls are refused while it stands — the gate is in the session,
    // not only in the disabled buttons drawn over it.
    media.record.published.length = 0;
    act(() => {
      call.result.current.toggleMicrophone();
    });
    expect(media.record.published).toEqual([]);

    // And the exit brings back the microphone and the camera that were on,
    // and not the screen share that never was.
    await call.say({ kind: "keyed", key: keyFor(1), publishBlocked: false });
    await waitFor(() => {
      expect(media.record.published).toEqual(["microphone:true", "camera:true"]);
    });
  });

  it("stops publishing when the key goes away mid-call", async () => {
    const media = fakeMedia();
    const call = setup(media.connect, { kind: "keyed", key: keyFor(1), publishBlocked: false });

    act(() => {
      call.result.current.join("channel-id");
    });
    await waitFor(() => {
      expect(call.result.current.micEnabled).toBe(true);
    });
    media.record.published.length = 0;

    // Evicted from the group: this device cannot derive what the others are
    // now using, so anything it sealed would be noise to them.
    await call.say({ kind: "refused" });
    await waitFor(() => {
      expect(media.record.published).toContain("microphone:false");
    });
    expect(call.result.current.publishBlocked).toBe(true);
  });
});
