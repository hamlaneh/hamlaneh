import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import type { MediaConnect, MediaEvent, MediaParticipant, MediaSession } from "../../calls/media";
import { isolateAuto, isolateLtr } from "../../i18n/bidi";
import i18n from "../../i18n";
import { InstanceProvider } from "../../instance/InstanceProvider";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import { CHAT_CHANNELS, CHAT_USERS, setMockCall } from "../../mocks/chat";
import { FIXTURE_ADMIN, resetMockAuth, setMockInstance } from "../../mocks/handlers";
import { server } from "../../mocks/node";
import { emitRealtime } from "../../mocks/ws";
import { ChatApp } from "../../screens/ChatApp";

/**
 * The call UI, against the contract and the mock server.
 *
 * Where the mocking line is drawn, and why there: everything up to and
 * including HTTP is real — `POST /call/token` and `GET /call` go through MSW,
 * and the three WebSocket events arrive over the mock socket. What is replaced
 * is the *media client*, at the `MediaConnect` seam, because `livekit-client`
 * opens real WebRTC and jsdom has no `RTCPeerConnection` to open it with.
 * Faking the network instead would mean faking SDP, ICE and DTLS to learn
 * nothing about this code.
 */

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(async () => {
  server.resetHandlers();
  resetMockAuth();
  await i18n.changeLanguage("en");
});

afterAll(() => {
  server.close();
});

const FAST_RETRY = { retryDelayMs: () => 5 };

function tile(overrides: Pick<MediaParticipant, "identity" | "name"> & Partial<MediaParticipant>) {
  return {
    isLocal: false,
    speaking: false,
    micEnabled: true,
    cameraEnabled: false,
    screenSharing: false,
    camera: null,
    screen: null,
    microphone: null,
    ...overrides,
  } satisfies MediaParticipant;
}

const ME = tile({ identity: CHAT_USERS.me.id, name: CHAT_USERS.me.display_name, isLocal: true });

/**
 * A media client that connects to nothing. It records what it was asked to
 * connect to and what it was asked to publish, and lets a test move the room's
 * participants the way the real one would on a `changed` event.
 */
function fakeMedia(initial: MediaParticipant[] = [ME]) {
  let participants = initial;
  const listeners = new Set<(event: MediaEvent) => void>();
  const record = {
    connections: [] as { url: string; token: string }[],
    published: [] as string[],
    disconnects: 0,
  };

  const patchLocal = (patch: Partial<MediaParticipant>) => {
    participants = participants.map((entry) => (entry.isLocal ? { ...entry, ...patch } : entry));
  };
  const emit = (event: MediaEvent) => {
    for (const listener of [...listeners]) {
      listener(event);
    }
  };

  const session: MediaSession = {
    participants: () => participants,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setMicrophoneEnabled: (enabled) => {
      record.published.push(`microphone:${String(enabled)}`);
      patchLocal({ micEnabled: enabled });
      return Promise.resolve();
    },
    setCameraEnabled: (enabled) => {
      record.published.push(`camera:${String(enabled)}`);
      patchLocal({ cameraEnabled: enabled });
      return Promise.resolve();
    },
    setScreenShareEnabled: (enabled) => {
      record.published.push(`screen:${String(enabled)}`);
      patchLocal({ screenSharing: enabled });
      return Promise.resolve();
    },
    disconnect: () => {
      record.disconnects += 1;
      listeners.clear();
      return Promise.resolve();
    },
  };

  const connect: MediaConnect = (url, token) => {
    record.connections.push({ url, token });
    return Promise.resolve(session);
  };

  return {
    connect,
    record,
    /** What the room looks like after somebody joined, muted, or turned a camera on. */
    replace(next: MediaParticipant[]) {
      act(() => {
        participants = next;
        emit("changed");
      });
    },
    /** The media server restarted: every call ends, and nobody chose that. */
    endRoom() {
      act(() => {
        emit("closed");
      });
    },
  };
}

function renderChat(path: string, media?: MediaConnect) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <InstanceProvider>
        <ChatApp
          currentUser={FIXTURE_ADMIN}
          onLogout={() => undefined}
          realtime={FAST_RETRY}
          {...(media === undefined ? {} : { media })}
        />
      </InstanceProvider>
    </MemoryRouter>,
  );
}

function strip(): HTMLElement {
  return screen.getByRole("region", { name: en.calls.strip.label });
}

function participantCount(count: number): string {
  return en.calls.strip.inProgress.replace("{{count}}", String(count));
}

/** The in-call surface for `#deploys`, named after the conversation it is for. */
const DEPLOYS_CALL = en.calls.view.heading.replace("{{channel}}", isolateLtr("#deploys"));

const NASRIN_IN_CALL = {
  user: CHAT_USERS.nasrin,
  joined_at: "2026-08-27T09:00:00.000Z",
};

describe("the instance gate", () => {
  it("draws no call surface at all when no media server is configured", async () => {
    setMockInstance({ calls: false });
    // A call really is running: the gate is the instance document, not the
    // absence of a call, so this must still draw nothing.
    setMockCall(CHAT_CHANNELS.deploys, { active: true, participants: [NASRIN_IN_CALL] });
    const media = fakeMedia();
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, media.connect);

    await screen.findByText("Rolling to canary in ten minutes.");
    expect(screen.queryByRole("region", { name: en.calls.strip.label })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.calls.start })).not.toBeInTheDocument();

    // And the DM ring stays shut too: an event still arrives, because the
    // gateway does not know what this client can draw.
    emitRealtime({
      type: "call_started",
      chan: CHAT_CHANNELS.dmParisa,
      data: { started_by: CHAT_USERS.parisa, participants: [] },
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: en.calls.ring.label })).not.toBeInTheDocument();
    });
  });

  it("offers a way into a call when a media server is configured", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);
    expect(await screen.findByRole("button", { name: en.calls.start })).toBeInTheDocument();
  });
});

describe("reconciliation", () => {
  /**
   * The rule from ws-protocol.md §5, and the reason this test exists: the call
   * events carry no sequence number and are never replayed, so a client that
   * treated them as the truth would show nothing at all here — it joined the
   * conversation after the only event was sent.
   */
  it("shows a call that was already running when the channel was opened, with no event involved", async () => {
    setMockCall(CHAT_CHANNELS.deploys, { active: true, participants: [NASRIN_IN_CALL] });
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);

    // The strip is always mounted, so waiting for the region would prove
    // nothing: what has to arrive is the call it learned about from REST.
    expect(await screen.findByText(participantCount(1))).toBeInTheDocument();
    const banner = strip();
    expect(
      within(banner).getByText(isolateAuto(CHAT_USERS.nasrin.display_name)),
    ).toBeInTheDocument();
    expect(within(banner).getByRole("button", { name: en.calls.join })).toBeInTheDocument();
  });

  it("corrects a stale hint: the read wins over the event that preceded it", async () => {
    renderChat(`/c/${CHAT_CHANNELS.designTokens}`, fakeMedia().connect);
    await screen.findByRole("button", { name: en.calls.start });

    // A call the client is told about, and which is over by the time it opens
    // the channel — the exact shape of a banner for a call nobody is in.
    emitRealtime({
      type: "call_started",
      chan: CHAT_CHANNELS.deploys,
      data: { started_by: CHAT_USERS.nasrin, participants: [NASRIN_IN_CALL] },
    });

    const user = userEvent.setup({ delay: null });
    await user.click(screen.getByRole("link", { name: /deploys/ }));

    // The mock server never had a call in #deploys, so the read says so.
    await waitFor(() => {
      expect(within(strip()).getByRole("button", { name: en.calls.start })).toBeInTheDocument();
    });
    expect(screen.queryByText(participantCount(1))).not.toBeInTheDocument();
  });
});

describe("the three events", () => {
  it("paints the banner on call_started and clears it on call_ended", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);
    await screen.findByRole("button", { name: en.calls.start });

    emitRealtime({
      type: "call_started",
      chan: CHAT_CHANNELS.deploys,
      data: { started_by: CHAT_USERS.nasrin, participants: [NASRIN_IN_CALL] },
    });

    expect(await screen.findByText(participantCount(1))).toBeInTheDocument();
    expect(within(strip()).getByRole("button", { name: en.calls.join })).toBeInTheDocument();
    // Not a DM, so nothing rings — the ring is the 1:1 affordance (ADR 005).
    expect(screen.queryByRole("region", { name: en.calls.ring.label })).not.toBeInTheDocument();

    emitRealtime({
      type: "call_updated",
      chan: CHAT_CHANNELS.deploys,
      data: {
        participants: [NASRIN_IN_CALL, { user: CHAT_USERS.omid, joined_at: "2026-08-27T09:01:00Z" }],
      },
    });
    expect(await screen.findByText(participantCount(2))).toBeInTheDocument();

    emitRealtime({ type: "call_ended", chan: CHAT_CHANNELS.deploys });

    await waitFor(() => {
      expect(within(strip()).getByRole("button", { name: en.calls.start })).toBeInTheDocument();
    });
    expect(screen.queryByText(participantCount(2))).not.toBeInTheDocument();
  });
});

describe("the direct-message ring", () => {
  it("rings on a call in a direct message and dismisses", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);
    await screen.findByRole("button", { name: en.calls.start });

    emitRealtime({
      type: "call_started",
      chan: CHAT_CHANNELS.dmParisa,
      data: { started_by: CHAT_USERS.parisa, participants: [] },
    });

    const ring = await screen.findByRole("region", { name: en.calls.ring.label });
    expect(
      within(ring).getByText(
        en.calls.ring.from.replace("{{name}}", isolateAuto(CHAT_USERS.parisa.display_name)),
      ),
    ).toBeInTheDocument();

    const user = userEvent.setup({ delay: null });
    await user.click(within(ring).getByRole("button", { name: en.calls.ring.dismiss }));

    // Dismissing dismisses the toast, and that is the whole of it: no decline,
    // no busy, no missed-call message (ADR 005).
    expect(screen.queryByRole("region", { name: en.calls.ring.label })).not.toBeInTheDocument();
  });

  it("does not ring at somebody who is already in the call", async () => {
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);
    await screen.findByRole("button", { name: en.calls.start });

    emitRealtime({
      type: "call_started",
      chan: CHAT_CHANNELS.dmParisa,
      data: {
        started_by: CHAT_USERS.me,
        participants: [{ user: CHAT_USERS.me, joined_at: "2026-08-27T09:00:00Z" }],
      },
    });

    await waitFor(() => {
      expect(screen.queryByRole("region", { name: en.calls.ring.label })).not.toBeInTheDocument();
    });
  });
});

describe("joining", () => {
  it("asks for a ticket, connects to the same-origin signal endpoint, and publishes", async () => {
    const media = fakeMedia();
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, media.connect);
    const user = userEvent.setup({ delay: null });

    await user.click(await screen.findByRole("button", { name: en.calls.start }));

    const view = await screen.findByRole("region", { name: DEPLOYS_CALL });

    // The media server's address is in no response: it is derived from the
    // page's own origin, scheme and all, and carries no path — the client
    // appends /rtc itself (ADR 005).
    expect(media.record.connections).toEqual([
      { url: `ws://${window.location.host}`, token: "fixture-call-ticket-not-a-real-one" },
    ]);
    // Microphone published, camera left alone: there is no prejoin screen to
    // check a camera against yet.
    expect(media.record.published).toEqual(["microphone:true"]);

    // Joined, so the reader is no longer somebody the banner is for.
    expect(screen.queryByRole("region", { name: en.calls.strip.label })).not.toBeInTheDocument();

    await user.click(within(view).getByRole("button", { name: en.calls.control.camera }));
    expect(media.record.published).toEqual(["microphone:true", "camera:true"]);
    await waitFor(() => {
      expect(within(view).getByRole("button", { name: en.calls.control.camera })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
    });

    await user.click(within(view).getByRole("button", { name: en.calls.control.screenShare }));
    expect(media.record.published).toEqual(["microphone:true", "camera:true", "screen:true"]);

    await user.click(within(view).getByRole("button", { name: en.calls.control.leave }));
    expect(media.record.disconnects).toBe(1);
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: DEPLOYS_CALL })).not.toBeInTheDocument();
    });
    // And the strip is back, because this reader is once again somebody it is
    // for — with no call left running, it offers to start one.
    expect(within(strip()).getByRole("button", { name: en.calls.start })).toBeInTheDocument();
  });

  it("draws a camera-off tile as a name and says so, and says when a tile is muted or speaking", async () => {
    const media = fakeMedia();
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, media.connect);
    const user = userEvent.setup({ delay: null });
    await user.click(await screen.findByRole("button", { name: en.calls.start }));
    await screen.findByRole("list", { name: en.calls.view.participants });

    media.replace([
      { ...ME, micEnabled: true },
      tile({
        identity: CHAT_USERS.nasrin.id,
        name: CHAT_USERS.nasrin.display_name,
        speaking: true,
      }),
      tile({ identity: CHAT_USERS.omid.id, name: CHAT_USERS.omid.display_name, micEnabled: false }),
    ]);

    const tiles = within(
      await screen.findByRole("list", { name: en.calls.view.participants }),
    ).getAllByRole("listitem");
    const [, nasrin, omid] = tiles;
    if (nasrin === undefined || omid === undefined) {
      throw new Error("the call should have three tiles");
    }

    // The state most tiles are in, and the one the brief says to get right:
    // no video element at all, a name, and a plain statement of why.
    expect(nasrin.querySelector("video")).toBeNull();
    expect(
      within(nasrin).getByText(isolateAuto(CHAT_USERS.nasrin.display_name)),
    ).toBeInTheDocument();
    expect(within(nasrin).getByText(en.calls.tile.cameraOff)).toBeInTheDocument();
    expect(within(nasrin).getByText(en.calls.tile.speaking)).toBeInTheDocument();
    expect(within(nasrin).queryByText(en.calls.tile.muted)).not.toBeInTheDocument();

    expect(within(omid).getByText(en.calls.tile.muted)).toBeInTheDocument();
  });

  it("says the call ended when the room closes under it", async () => {
    const media = fakeMedia();
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, media.connect);
    const user = userEvent.setup({ delay: null });
    await user.click(await screen.findByRole("button", { name: en.calls.start }));
    await screen.findByRole("list", { name: en.calls.view.participants });

    // A media-server restart ends every call (ADR 005) — a real state, not an
    // edge case, and not something this person did.
    media.endRoom();

    expect(await screen.findByRole("alert")).toHaveTextContent(en.calls.error.ended);
    await user.click(screen.getByRole("button", { name: en.calls.dismiss }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("Persian", () => {
  it("keeps app-generated counts in ASCII digits", async () => {
    await i18n.changeLanguage("fa");
    setMockCall(CHAT_CHANNELS.deploys, {
      active: true,
      participants: [NASRIN_IN_CALL, { user: CHAT_USERS.omid, joined_at: "2026-08-27T09:01:00Z" }],
    });
    renderChat(`/c/${CHAT_CHANNELS.deploys}`, fakeMedia().connect);

    // The count is written with ASCII digits, which is what finding this
    // exact string proves; the negative check catches the other direction.
    expect(
      await screen.findByText(fa.calls.strip.inProgress.replace("{{count}}", "2")),
    ).toBeInTheDocument();
    const banner = screen.getByRole("region", { name: fa.calls.strip.label });
    expect(within(banner).queryByText(/[۰-۹]/u)).not.toBeInTheDocument();
  });
});
