import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import type { OrgSettings, UpdateOrgSettingsRequest } from "../../admin/adminApi";
// Initialises i18next, which App does transitively; rendering a component on
// its own does not, and an uninitialised t() returns the key.
import "../../i18n";
import { FALLBACK_INSTANCE_INFO, InstanceContext } from "../../instance/instanceInfo";
import type { InstanceInfo } from "../../instance/instanceInfo";
import en from "../../locales/en/common.json";
import { FIXTURE_ADMIN } from "../../mocks/handlers";
import { server } from "../../mocks/node";
import { AdminOrgSettings } from "./AdminOrgSettings";

const SETTINGS: OrgSettings = {
  org_name: "Hamlaneh",
  default_locale: "en",
  registration_mode: "invite",
  require_totp: false,
  session_lifetime_hours: 168,
  sso_jit_provisioning: false,
  encryption_mode: "strict",
  conversations_outside_mode: 0,
};

/** What the instance holds, mutated by the PATCH handler. */
let live: OrgSettings;

/** Every body the screen PATCHed, in order. */
let patched: UpdateOrgSettingsRequest[];

/** Set by the case that wants the save refused. */
let patchFails = false;

function installOrgHandlers() {
  server.use(
    http.get("/api/v1/admin/org", () => HttpResponse.json(live)),
    http.patch<never, UpdateOrgSettingsRequest>("/api/v1/admin/org", async ({ request }) => {
      const body = await request.json();
      patched = [...patched, body];
      if (patchFails) {
        return HttpResponse.json(
          { error: { code: "internal_error", message: "the instance refused" } },
          { status: 500 },
        );
      }
      live = { ...live, ...body };
      return HttpResponse.json(live);
    }),
  );
}

/** `sso` absent stands for an instance with no identity provider configured. */
function renderScreen(sso?: NonNullable<InstanceInfo["sso"]>) {
  // Spread rather than `{ sso }`: under exactOptionalPropertyTypes an absent
  // key and a key holding undefined are different types, and the contract's
  // absent-means-none is the first one.
  const info: InstanceInfo = { ...FALLBACK_INSTANCE_INFO, ...(sso === undefined ? {} : { sso }) };
  render(
    <MemoryRouter initialEntries={["/admin/org"]}>
      <InstanceContext value={{ info, loaded: true }}>
        <AdminOrgSettings currentUser={FIXTURE_ADMIN} organizationName="Hamlaneh" />
      </InstanceContext>
    </MemoryRouter>,
  );
}

function jitSwitch() {
  return screen.getByRole("switch", { name: new RegExp(en.admin.org.ssoJitLabel, "u") });
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  live = { ...SETTINGS };
  patched = [];
  patchFails = false;
  installOrgHandlers();
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("just-in-time provisioning", () => {
  it("shows the state the instance holds, not a guess", async () => {
    live = { ...SETTINGS, sso_jit_provisioning: true };
    renderScreen({ enabled: true, provider_name: "Okta" });

    await waitFor(() => {
      expect(jitSwitch()).toHaveAttribute("aria-checked", "true");
    });
    // Both sentences an administrator needs before deciding: what the switch
    // does, and that it is not the registration setting above it.
    expect(screen.getByText(en.admin.org.ssoJitHint)).toBeInTheDocument();
    expect(screen.getByText(en.admin.org.ssoJitNote)).toBeInTheDocument();
    // A provider is configured, so there is nothing to caveat.
    expect(screen.queryByText(en.admin.org.ssoJitUnconfigured)).toBeNull();
  });

  it("saves on the click, on its own, with no Save button", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen({ enabled: true, provider_name: "Okta" });

    await waitFor(() => {
      expect(jitSwitch()).toHaveAttribute("aria-checked", "false");
    });
    await user.click(jitSwitch());

    await waitFor(() => {
      expect(jitSwitch()).toHaveAttribute("aria-checked", "true");
    });
    // One field, alone: the screen promises every setting saves by itself, so
    // this request must not carry the rest of the form along with it.
    expect(patched).toEqual([{ sso_jit_provisioning: true }]);
  });

  it("says a refused save did not happen and puts the switch back", async () => {
    const user = userEvent.setup({ delay: null });
    patchFails = true;
    renderScreen({ enabled: true, provider_name: "Okta" });

    await waitFor(() => {
      expect(jitSwitch()).toHaveAttribute("aria-checked", "false");
    });
    await user.click(jitSwitch());

    // The same banner every other field on this screen fails into.
    expect(await screen.findByText(en.admin.org.saveFailed)).toBeInTheDocument();
    // And the switch still reads what the instance actually holds — the screen
    // never shows a value the save did not land.
    expect(jitSwitch()).toHaveAttribute("aria-checked", "false");
  });

  it("stays offered, and says why it does nothing, when no provider is configured", async () => {
    renderScreen();

    expect(await screen.findByText(en.admin.org.ssoJitUnconfigured)).toBeInTheDocument();
    // Shown and writable anyway: the value is stored policy that governs the
    // moment a provider is configured, so it is not hidden behind one.
    expect(jitSwitch()).toBeEnabled();
  });
});
