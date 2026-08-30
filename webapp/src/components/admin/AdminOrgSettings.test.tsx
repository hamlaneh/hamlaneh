import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import type {
  OrgSettings,
  SetEncryptionModeRequest,
  UpdateOrgSettingsRequest,
} from "../../admin/adminApi";
// Initialises i18next, which App does transitively; rendering a component on
// its own does not, and an uninitialised t() returns the key.
import i18n from "../../i18n";
import { FALLBACK_INSTANCE_INFO, InstanceContext } from "../../instance/instanceInfo";
import type { InstanceInfo } from "../../instance/instanceInfo";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import { FIXTURE_ADMIN } from "../../mocks/handlers";
import { server } from "../../mocks/node";
import { AdminOrgSettings } from "./AdminOrgSettings";
import { EncryptionModeSwitchDialog } from "./EncryptionModeSection";

const SETTINGS: OrgSettings = {
  org_name: "Hamlaneh",
  default_locale: "en",
  registration_mode: "invite",
  require_totp: false,
  session_lifetime_hours: 168,
  sso_jit_provisioning: false,
  // Every install is Strict, and every migrated one arrives carrying whatever
  // it created before the mode existed (ADR 011 decision 3).
  encryption_mode: "strict",
  conversations_outside_mode: 0,
};

/** What the instance holds, mutated by the PATCH handler. */
let live: OrgSettings;

/** Every body the screen PATCHed, in order. */
let patched: UpdateOrgSettingsRequest[];

/** Every mode the screen PUT to the dedicated endpoint, in order. */
let modesPut: SetEncryptionModeRequest[];

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
    // The mode's own endpoint. `compliance` is refused while it is not
    // selectable, exactly as the contract says (409 encryption_mode_locked),
    // so the screen can never talk a locked instance into it.
    http.put<never, SetEncryptionModeRequest>(
      "/api/v1/admin/org/encryption-mode",
      async ({ request }) => {
        const body = await request.json();
        modesPut = [...modesPut, body];
        if (body.encryption_mode === "compliance") {
          return HttpResponse.json(
            { error: { code: "encryption_mode_locked", message: "not available yet" } },
            { status: 409 },
          );
        }
        live = { ...live, encryption_mode: body.encryption_mode };
        return HttpResponse.json(live);
      },
    ),
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
  modesPut = [];
  patchFails = false;
  installOrgHandlers();
});

afterEach(async () => {
  server.resetHandlers();
  // The Persian case changes it, and every other case reads `en` strings.
  await i18n.changeLanguage("en");
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

const ENCRYPTION = en.admin.org.encryption;

function modeRadio(label: string) {
  return screen.getByRole("radio", { name: new RegExp(label, "u") });
}

describe("the organization encryption mode", () => {
  it("shows Compliance, disabled, with the reason it is not honest yet", async () => {
    renderScreen();

    // Shown, never hidden: a hidden option teaches nobody what this product
    // will offer, and an offered one would be the dishonest toggle.
    const compliance = await screen.findByRole("radio", {
      name: new RegExp(ENCRYPTION.mode.compliance, "u"),
    });
    expect(compliance).toBeDisabled();
    // And the refusal names all three missing pieces rather than saying
    // "unavailable" and leaving the admin to guess.
    expect(screen.getByText(ENCRYPTION.complianceLocked)).toBeInTheDocument();
    expect(compliance).toHaveAccessibleDescription(ENCRYPTION.complianceLocked);
    // Strict is what the instance is in, and it says so in words as well.
    expect(modeRadio(ENCRYPTION.mode.strict)).toBeChecked();
    expect(screen.getByText(ENCRYPTION.current.strict)).toBeInTheDocument();
  });

  it("states the standing count of conversations outside the mode", async () => {
    live = { ...SETTINGS, conversations_outside_mode: 12 };
    renderScreen();

    expect(
      await screen.findByText(
        ENCRYPTION.outside.strict.replace("{{conversations}}", "12"),
      ),
    ).toBeInTheDocument();
  });

  it("keeps the count in ASCII digits in Persian", async () => {
    await i18n.changeLanguage("fa");
    live = { ...SETTINGS, conversations_outside_mode: 12 };
    renderScreen();

    // The designer's locked rule: every app-generated number is Latin digits,
    // in Persian too. "۱۲" would be the CLDR default and is not what is drawn.
    const line = await screen.findByText(
      fa.admin.org.encryption.outside.strict.replace("{{conversations}}", "12"),
    );
    expect(line.textContent).toContain("12");
    expect(line.textContent).not.toContain("۱۲");
  });

  it("says so plainly when nothing is outside the mode, rather than staying silent", async () => {
    renderScreen();

    // Zero is stated, not omitted: silence would teach an admin that the mode
    // covers everything, which is the implication the ADR refuses.
    expect(await screen.findByText(ENCRYPTION.outside.none)).toBeInTheDocument();
  });

  it("confirms a switch to Strict, and says what it does not do to what exists", async () => {
    const user = userEvent.setup({ delay: null });
    // The only instance from which Strict is a change: one already in
    // Compliance. Not reachable through this screen while Compliance is
    // locked, and the write path has to be right the day it is.
    live = { ...SETTINGS, encryption_mode: "compliance", conversations_outside_mode: 3 };
    renderScreen();

    await user.click(await screen.findByRole("radio", { name: new RegExp(ENCRYPTION.mode.strict, "u") }));

    const dialog = await screen.findByRole("dialog", {
      name: ENCRYPTION.switch.strict.title,
    });
    // Nothing already stored is converted, and the copy says exactly that.
    expect(within(dialog).getByText(ENCRYPTION.switch.strict.unchanged)).toBeInTheDocument();
    expect(within(dialog).getByText(ENCRYPTION.switch.strict.begins)).toBeInTheDocument();
    // The blast radius before confirming, not from support requests after.
    expect(
      within(dialog).getByText(
        ENCRYPTION.outside.compliance.replace("{{conversations}}", "3"),
      ),
    ).toBeInTheDocument();

    await user.click(
      within(dialog).getByRole("button", { name: ENCRYPTION.switch.strict.confirm }),
    );

    await waitFor(() => {
      expect(screen.getByText(ENCRYPTION.current.strict)).toBeInTheDocument();
    });
    // Its own endpoint, and the settings PATCH never carried it: the mode is a
    // decision with a ceremony, not a field on a screen that saves as you type.
    expect(modesPut).toEqual([{ encryption_mode: "strict" }]);
    expect(patched).toEqual([]);
  });

  it("does not switch on the choice alone — cancelling leaves the mode where it was", async () => {
    const user = userEvent.setup({ delay: null });
    live = { ...SETTINGS, encryption_mode: "compliance" };
    renderScreen();

    await user.click(await screen.findByRole("radio", { name: new RegExp(ENCRYPTION.mode.strict, "u") }));
    const dialog = await screen.findByRole("dialog", { name: ENCRYPTION.switch.strict.title });
    await user.click(within(dialog).getByRole("button", { name: en.chat.common.cancel }));

    expect(
      screen.queryByRole("dialog", { name: ENCRYPTION.switch.strict.title }),
    ).not.toBeInTheDocument();
    expect(modesPut).toEqual([]);
    expect(screen.getByText(ENCRYPTION.current.compliance)).toBeInTheDocument();
  });
});

describe("the switch dialog's other direction", () => {
  /**
   * Strict → Compliance cannot be reached from the screen while Compliance is
   * drawn disabled, so the dialog is rendered on its own — the copy has to be
   * right the day the option unlocks, and it makes a security statement that
   * should not first be read in production.
   */
  function renderComplianceDialog(conversationsOutsideMode = 0) {
    render(
      <EncryptionModeSwitchDialog
        to="compliance"
        currentMode="strict"
        conversationsOutsideMode={conversationsOutsideMode}
        busy={false}
        error={null}
        onConfirm={() => undefined}
        onCancel={() => undefined}
      />,
    );
  }

  it("says nothing already encrypted becomes readable, and that a full export is impossible", () => {
    renderComplianceDialog();

    const dialog = screen.getByRole("dialog", { name: ENCRYPTION.switch.compliance.title });
    expect(within(dialog).getByText(ENCRYPTION.switch.compliance.unchanged)).toBeInTheDocument();
    expect(within(dialog).getByText(ENCRYPTION.switch.compliance.begins)).toBeInTheDocument();
    // The sentence the product is not allowed to soften: the impossibility is
    // what end-to-end encryption is, not a gap to apologise for.
    expect(
      within(dialog).getByText(ENCRYPTION.switch.compliance.exportImpossible),
    ).toBeInTheDocument();
  });

  it("shows the live count before confirming", () => {
    renderComplianceDialog(7);

    expect(
      screen.getByText(ENCRYPTION.outside.strict.replace("{{conversations}}", "7")),
    ).toBeInTheDocument();
  });
});
