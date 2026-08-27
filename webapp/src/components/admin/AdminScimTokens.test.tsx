import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import type { CreateScimTokenRequest, ScimToken } from "../../admin/adminApi";
// Initialises i18next, which App does transitively; rendering a component on
// its own does not, and an uninitialised t() returns the key.
import "../../i18n";
import en from "../../locales/en/common.json";
import { FIXTURE_ADMIN } from "../../mocks/handlers";
import { server } from "../../mocks/node";
import { AdminScimTokens } from "./AdminScimTokens";

const CREATOR = {
  id: FIXTURE_ADMIN.id,
  username: FIXTURE_ADMIN.username,
  display_name: FIXTURE_ADMIN.display_name,
};

/** Obviously fake, and the only place the value ever appears in a test. */
const MINTED = "hmscim_fixture-token-not-a-real-credential";

/** A token a provider has actually authenticated with. */
const CONFIGURED: ScimToken = {
  id: "00000000-0000-4000-8000-0000000000a1",
  note: "Okta production",
  created_by: CREATOR,
  created_at: "2026-08-01T09:00:00Z",
  last_used_at: "2026-08-20T07:30:00Z",
};

/** Minted, never used, and no note to say what it was for. */
const FORGOTTEN: ScimToken = {
  id: "00000000-0000-4000-8000-0000000000a2",
  note: null,
  created_by: CREATOR,
  created_at: "2026-08-02T09:00:00Z",
  last_used_at: null,
};

/** The instance's live tokens, mutated by the mint and revoke handlers. */
let live: ScimToken[] = [];

function installScimHandlers() {
  server.use(
    http.get("/api/v1/admin/scim/tokens", () => HttpResponse.json({ tokens: live })),
    http.post<never, CreateScimTokenRequest>(
      "/api/v1/admin/scim/tokens",
      async ({ request }) => {
        const body = await request.json();
        const created: ScimToken = {
          id: "00000000-0000-4000-8000-0000000000b1",
          note: body.note ?? null,
          created_by: CREATOR,
          created_at: "2026-08-27T10:00:00Z",
          // Null on creation, always: nothing has authenticated with it yet.
          last_used_at: null,
        };
        live = [...live, created];
        // The one response that carries the value; the list never does.
        return HttpResponse.json({ token: MINTED, scim: created }, { status: 201 });
      },
    ),
    http.delete<{ tokenId: string }>("/api/v1/admin/scim/tokens/:tokenId", ({ params }) => {
      live = live.filter((token) => token.id !== params.tokenId);
      return new HttpResponse(null, { status: 204 });
    }),
  );
}

function renderScreen() {
  render(
    <MemoryRouter initialEntries={["/admin/scim"]}>
      <AdminScimTokens currentUser={FIXTURE_ADMIN} organizationName="Hamlaneh" />
    </MemoryRouter>,
  );
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  live = [CONFIGURED, FORGOTTEN];
  installScimHandlers();
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("the provisioning tokens table", () => {
  it("separates a token the provider has used from one that was never used", async () => {
    renderScreen();

    const configured = await screen.findByRole("row", { name: /Okta production/u });
    // The column the screen exists for: one row names a date, the other says
    // in words that nothing has ever authenticated with it.
    const lastUsed = within(configured).getAllByRole("cell")[3];
    if (lastUsed === undefined) {
      throw new Error("the token row has no Last used cell");
    }
    expect(lastUsed).toHaveTextContent(/2026/u);
    expect(within(configured).queryByText(en.admin.scim.neverUsed)).toBeNull();

    const forgotten = screen.getByRole("row", { name: new RegExp(en.admin.scim.noNote, "u") });
    expect(within(forgotten).getByText(en.admin.scim.neverUsed)).toBeInTheDocument();

    // And the screen says what an empty "Last used" means, rather than leaving
    // an administrator to work it out from a blank cell.
    expect(screen.getByText(en.admin.scim.lastUsedNote)).toBeInTheDocument();
  });

  it("says several live tokens at once is how one is replaced", async () => {
    renderScreen();

    await screen.findByRole("row", { name: /Okta production/u });
    expect(screen.getByText(en.admin.scim.overlap)).toBeInTheDocument();
  });

  it("explains what a token is for when there are none", async () => {
    live = [];
    renderScreen();

    expect(await screen.findByText(en.admin.scim.empty)).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("minting a provisioning token", () => {
  it("shows the value once, and it is gone from the screen once acknowledged", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen();
    await screen.findByRole("row", { name: /Okta production/u });

    await user.type(screen.getByLabelText(en.admin.scim.noteLabel), "Entra staging");
    await user.click(screen.getByRole("button", { name: en.admin.scim.create }));

    const panel = await screen.findByRole("dialog");
    expect(within(panel).getByText(MINTED)).toBeInTheDocument();

    await user.click(within(panel).getByRole("button", { name: en.admin.credentials.acknowledge }));

    // The server keeps only a hash: nothing on the screen — not the panel, not
    // the row the mint just added — can put the value back.
    await waitFor(() => {
      expect(screen.queryByText(MINTED)).toBeNull();
    });
    expect(await screen.findByRole("row", { name: /Entra staging/u })).toBeInTheDocument();
  });
});

describe("revoking a provisioning token", () => {
  it("asks first, saying the provider's next sync will fail, and then drops the row", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen();

    const configured = await screen.findByRole("row", { name: /Okta production/u });
    await user.click(within(configured).getByRole("button", { name: en.admin.scim.revoke }));

    const confirm = await screen.findByRole("dialog", {
      name: en.admin.scim.revokeConfirm.title,
    });
    expect(within(confirm).getByText(en.admin.scim.revokeConfirm.body)).toBeInTheDocument();

    // Backing out changes nothing — this cuts off somebody's provisioning.
    await user.click(within(confirm).getByRole("button", { name: en.chat.common.cancel }));
    expect(screen.getByRole("row", { name: /Okta production/u })).toBeInTheDocument();

    await user.click(within(configured).getByRole("button", { name: en.admin.scim.revoke }));
    await user.click(
      within(await screen.findByRole("dialog", { name: en.admin.scim.revokeConfirm.title })).getByRole(
        "button",
        { name: en.admin.scim.revoke },
      ),
    );

    await waitFor(() => {
      expect(screen.queryByRole("row", { name: /Okta production/u })).toBeNull();
    });
    // The other token is untouched.
    expect(
      screen.getByRole("row", { name: new RegExp(en.admin.scim.noNote, "u") }),
    ).toBeInTheDocument();
  });
});
