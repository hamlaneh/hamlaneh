import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import type {
  MediaConnect,
  MediaParticipant,
  MediaSession,
} from "../calls/media";
import { isolateAuto } from "../i18n/bidi";
// Initialises i18next, which App does transitively; rendering a screen on its
// own does not, and an uninitialised t() returns the key.
import "../i18n";
import en from "../locales/en/common.json";
import { server } from "../mocks/node";
import { MeetGuestScreen } from "./MeetGuestScreen";

/** Obviously fake, and shaped like what `session.NewToken` mints. */
const TOKEN = "fixture-conference-link-not-a-real-one";
const TICKET = "fixture-conference-ticket-not-a-real-one";

const TITLE = "Thursday planning";

/**
 * A media client that connects to nothing, recording what it was asked to
 * connect to. Small on purpose: this screen's question is the ticket and the
 * page around it, not the tiles — `components/calls/Calls.test.tsx` already
 * holds the grid to its own behaviour.
 */
function fakeMedia(participants: MediaParticipant[]) {
  const record = { connections: [] as { url: string; token: string }[] };
  const session: MediaSession = {
    participants: () => participants,
    subscribe: () => () => undefined,
    setMicrophoneEnabled: () => Promise.resolve(),
    setCameraEnabled: () => Promise.resolve(),
    setScreenShareEnabled: () => Promise.resolve(),
    disconnect: () => Promise.resolve(),
  };
  const connect: MediaConnect = (url, token) => {
    record.connections.push({ url, token });
    return Promise.resolve(session);
  };
  return { connect, record };
}

function guestTile(name: string): MediaParticipant {
  return {
    identity: "guest-fixture",
    name,
    isLocal: true,
    speaking: false,
    micEnabled: true,
    cameraEnabled: false,
    screenSharing: false,
    camera: null,
    screen: null,
    microphone: null,
  };
}

/** The preview a live link answers with. */
function installPreview(active = false) {
  server.use(
    http.get("/api/v1/meet/:token", () => HttpResponse.json({ title: TITLE, active })),
  );
}

/** Every dead link — unknown, expired, revoked — is this one response. */
function installDeadLink() {
  server.use(
    http.get("/api/v1/meet/:token", () =>
      HttpResponse.json({ error: { code: "not_found", message: "not found" } }, { status: 404 }),
    ),
  );
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("the conference preview", () => {
  it("names the meeting, asks only for a display name, and says no account is created", async () => {
    installPreview();
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    expect(await screen.findByRole("heading", { name: isolateAuto(TITLE) })).toBeInTheDocument();
    expect(screen.getByText(en.meet.empty)).toBeInTheDocument();

    // The whole form: one field, and it is not an account.
    expect(screen.getByLabelText(en.meet.nameLabel)).toBeInTheDocument();
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.queryByLabelText(/password/iu)).toBeNull();
    expect(screen.getByText(en.meet.guestNote)).toBeInTheDocument();
  });

  it("says somebody is already in there when somebody is", async () => {
    installPreview(true);
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    expect(await screen.findByText(en.meet.active)).toBeInTheDocument();
  });
});

describe("a dead link", () => {
  it("gives one answer for every way a link can be dead, and does not guess which", async () => {
    installDeadLink();
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    expect(await screen.findByText(en.meet.unusable.body)).toBeInTheDocument();

    // The page holds all three possibilities open rather than picking one: the
    // 404 it received is the same 404 for unknown, expired and revoked, and a
    // sentence naming a cause would be invention.
    expect(en.meet.unusable.body).toMatch(/expired/u);
    expect(en.meet.unusable.body).toMatch(/revoked/u);
    expect(en.meet.unusable.body).toMatch(/never have existed/u);

    // A way out, and no way in.
    expect(screen.getByRole("link", { name: en.meet.goHome })).toHaveAttribute("href", "/");
    expect(screen.queryByLabelText(en.meet.nameLabel)).toBeNull();
  });

  it("separates a link that is dead from a server it could not ask", async () => {
    server.use(
      http.get("/api/v1/meet/:token", () =>
        HttpResponse.json(
          { error: { code: "internal", message: "boom" } },
          { status: 500 },
        ),
      ),
    );
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    // Not a claim about the link — the one distinction the client can honestly
    // draw, and the only one it draws.
    expect(await screen.findByText(en.meet.unreachable.body)).toBeInTheDocument();
    expect(screen.queryByText(en.meet.unusable.body)).toBeNull();
  });

  it("falls back to the dead-link answer when the link dies between the preview and the click", async () => {
    installPreview();
    server.use(
      http.post("/api/v1/meet/:token/join", () =>
        HttpResponse.json(
          { error: { code: "not_found", message: "not found" } },
          { status: 404 },
        ),
      ),
    );
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    const user = userEvent.setup({ delay: null });
    await user.type(await screen.findByLabelText(en.meet.nameLabel), "Roya");
    await user.click(screen.getByRole("button", { name: en.meet.join }));

    // Revoking ends the room as well as the link (ADR 005), so "that call
    // failed" would be the wrong sentence: the link is what is gone.
    expect(await screen.findByText(en.meet.unusable.body)).toBeInTheDocument();
  });
});

describe("joining as a guest", () => {
  it("mints a ticket with the typed name and enters the call", async () => {
    installPreview();
    const bodies: unknown[] = [];
    server.use(
      http.post("/api/v1/meet/:token/join", async ({ request, params }) => {
        bodies.push({ token: params.token, body: await request.json() });
        return HttpResponse.json(
          { token: TICKET, room: "conf-fixture", expires_at: "2026-08-27T10:02:00Z" },
          { status: 201 },
        );
      }),
    );
    const media = fakeMedia([guestTile("Roya")]);
    render(<MeetGuestScreen token={TOKEN} media={media.connect} />);

    const user = userEvent.setup({ delay: null });
    // Trailing space on purpose: the field's `required` lets whitespace
    // through, and the server's minLength would not catch it either.
    await user.type(await screen.findByLabelText(en.meet.nameLabel), "Roya ");
    await user.click(screen.getByRole("button", { name: en.meet.join }));

    await waitFor(() => {
      expect(bodies).toEqual([{ token: TOKEN, body: { display_name: "Roya" } }]);
    });

    // The ticket goes to the media client, and the signal address is derived
    // from this page's own origin — a guest is told no more about the media
    // plane than a member is (ADR 005).
    await waitFor(() => {
      expect(media.record.connections).toEqual([
        { url: `ws://${window.location.host}`, token: TICKET },
      ]);
    });

    // And it is the call view a member sees, not a second one.
    const view = await screen.findByRole("region", {
      name: en.calls.view.heading.replace("{{channel}}", isolateAuto(TITLE)),
    });
    expect(
      within(await screen.findByRole("list", { name: en.calls.view.participants })).getByText(
        isolateAuto("Roya"),
      ),
    ).toBeInTheDocument();
    expect(within(view).getByRole("button", { name: en.calls.control.leave })).toBeInTheDocument();
  });

  it("says calls are not configured here rather than blaming the link", async () => {
    installPreview();
    server.use(
      http.post("/api/v1/meet/:token/join", () =>
        HttpResponse.json(
          { error: { code: "calls_unavailable", message: "not configured" } },
          { status: 503 },
        ),
      ),
    );
    render(<MeetGuestScreen token={TOKEN} media={fakeMedia([]).connect} />);

    const user = userEvent.setup({ delay: null });
    await user.type(await screen.findByLabelText(en.meet.nameLabel), "Roya");
    await user.click(screen.getByRole("button", { name: en.meet.join }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.calls.error.unavailable);
    // The link is fine; nothing on screen says otherwise.
    expect(screen.queryByText(en.meet.unusable.body)).toBeNull();
  });
});
