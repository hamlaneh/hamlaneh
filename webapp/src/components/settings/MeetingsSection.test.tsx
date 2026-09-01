import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { MeetingsSection } from "./MeetingsSection";
import type { components } from "../../api/schema";
// Initialises i18next, which App does transitively; rendering a component on
// its own does not, and an uninitialised t() returns the key.
import "../../i18n";
import en from "../../locales/en/common.json";
import { FIXTURE_ADMIN } from "../../mocks/handlers";
import { server } from "../../mocks/node";

type Conference = components["schemas"]["Conference"];
type CreateConferenceRequest = components["schemas"]["CreateConferenceRequest"];

const CREATOR = {
  id: FIXTURE_ADMIN.id,
  username: FIXTURE_ADMIN.username,
  display_name: FIXTURE_ADMIN.display_name,
};

/** Obviously fake, and the only place the value ever appears in a test. */
const MINTED_URL = "https://hamlaneh.example/meet/fixture-conference-link-not-a-real-one";

const WEEKLY: Conference = {
  id: "00000000-0000-4000-8000-0000000000c1",
  title: "Weekly planning",
  created_by: CREATOR,
  created_at: "2026-08-01T09:00:00Z",
  expires_at: null,
  active: true,
};

const RETRO: Conference = {
  id: "00000000-0000-4000-8000-0000000000c2",
  title: "Retro",
  created_by: null,
  created_at: "2026-08-02T09:00:00Z",
  expires_at: null,
  active: false,
};

/** The instance's live conferences, mutated by the create and revoke handlers. */
let live: Conference[] = [];

function installConferenceHandlers() {
  server.use(
    http.get("/api/v1/conferences", () => HttpResponse.json({ conferences: live })),
    http.post<never, CreateConferenceRequest>("/api/v1/conferences", async ({ request }) => {
      const body = await request.json();
      const conference: Conference = {
        id: "00000000-0000-4000-8000-0000000000d1",
        title: body.title ?? "",
        created_by: CREATOR,
        created_at: "2026-08-27T10:00:00Z",
        expires_at: null,
        active: false,
      };
      live = [...live, conference];
      // The one response that carries the link; the list never does.
      return HttpResponse.json({ conference, url: MINTED_URL }, { status: 201 });
    }),
    http.delete<{ conferenceId: string }>(
      "/api/v1/conferences/:conferenceId",
      ({ params }) => {
        live = live.filter((conference) => conference.id !== params.conferenceId);
        return new HttpResponse(null, { status: 204 });
      },
    ),
  );
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  live = [WEEKLY, RETRO];
  installConferenceHandlers();
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("the meetings table", () => {
  it("says which room somebody is in, and that a lost link cannot be recovered", async () => {
    render(<MeetingsSection />);

    const weekly = await screen.findByRole("row", { name: /Weekly planning/u });
    expect(within(weekly).getByText(en.settings.meetings.live)).toBeInTheDocument();

    const retro = screen.getByRole("row", { name: /Retro/u });
    expect(within(retro).getByText(en.settings.meetings.idle)).toBeInTheDocument();
    // The account that made it is gone — the contract allows a null creator.
    expect(within(retro).getByText(en.settings.meetings.creatorGone)).toBeInTheDocument();

    // The column people look for is absent and the page says why, rather than
    // leaving them to hunt for a copy control that cannot exist: the absence
    // is stated above the table, and no header or cell stands in for it.
    expect(screen.getByText(en.settings.meetings.noLinkColumn)).toBeInTheDocument();
    expect(screen.getByText(en.settings.meetings.linkOnceNote)).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: /link/iu })).toBeNull();
    expect(within(weekly).queryByRole("button", { name: /copy/iu })).toBeNull();
    expect(screen.queryByText(MINTED_URL)).toBeNull();
  });

  it("explains what a meeting link is when there are none", async () => {
    live = [];
    render(<MeetingsSection />);

    expect(await screen.findByText(en.settings.meetings.empty)).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("creating a meeting link", () => {
  it("shows the link once, and it is gone from the screen once acknowledged", async () => {
    const user = userEvent.setup({ delay: null });
    render(<MeetingsSection />);
    await screen.findByRole("row", { name: /Weekly planning/u });

    // Creation is the header action opening a dialog, not a text field kept
    // permanently open above a table about the links that already exist.
    expect(screen.queryByLabelText(en.settings.meetings.titleLabel)).toBeNull();
    await user.click(screen.getByRole("button", { name: en.settings.meetings.create }));

    const form = await screen.findByRole("dialog", { name: en.settings.meetings.create });
    await user.type(
      within(form).getByLabelText(en.settings.meetings.titleLabel),
      "Design review",
    );
    await user.click(
      within(form).getByRole("button", { name: en.settings.meetings.createSubmit }),
    );

    const panel = await screen.findByRole("dialog", {
      name: en.settings.meetings.created.title,
    });
    expect(within(panel).getByText(MINTED_URL)).toBeInTheDocument();
    // Once means once: the whole document contains the link exactly once, so
    // there is no second copy anywhere to outlive the acknowledgement below.
    // Asserted over the raw text rather than with a query, because a leak into
    // a cell beside other text is exactly what a by-element query cannot see.
    expect(document.body.textContent.split(MINTED_URL)).toHaveLength(2);

    await user.click(within(panel).getByRole("button", { name: en.admin.credentials.acknowledge }));

    // The server keeps only a hash: nothing on the screen — not the panel, not
    // the row the creation just added — can put the link back.
    await waitFor(() => {
      expect(document.body.textContent).not.toContain(MINTED_URL);
    });
    expect(await screen.findByRole("row", { name: /Design review/u })).toBeInTheDocument();
  });
});

describe("revoking a meeting link", () => {
  it("asks first, saying the meeting behind it ends, and then drops the row", async () => {
    const user = userEvent.setup({ delay: null });
    render(<MeetingsSection />);

    const weekly = await screen.findByRole("row", { name: /Weekly planning/u });
    await user.click(within(weekly).getByRole("button", { name: en.settings.meetings.revoke }));

    const confirm = await screen.findByRole("dialog", {
      name: en.settings.meetings.revokeConfirm.title,
    });
    expect(
      within(confirm).getByText(en.settings.meetings.revokeConfirm.body),
    ).toBeInTheDocument();

    // Backing out changes nothing — this disconnects everybody in the room.
    await user.click(within(confirm).getByRole("button", { name: en.settings.cancel }));
    expect(screen.getByRole("row", { name: /Weekly planning/u })).toBeInTheDocument();

    await user.click(within(weekly).getByRole("button", { name: en.settings.meetings.revoke }));
    await user.click(
      within(
        await screen.findByRole("dialog", { name: en.settings.meetings.revokeConfirm.title }),
      ).getByRole("button", { name: en.settings.meetings.revoke }),
    );

    await waitFor(() => {
      expect(screen.queryByRole("row", { name: /Weekly planning/u })).toBeNull();
    });
    // The other room is untouched.
    expect(screen.getByRole("row", { name: /Retro/u })).toBeInTheDocument();
  });
});
