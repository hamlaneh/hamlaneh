import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import App from "../../App";
import i18n from "../../i18n";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import {
  enableMockTotp,
  FIXTURE_CREDENTIALS,
  FIXTURE_RECOVERY_CODES,
  FIXTURE_TOTP_CODE,
  resetMockAuth,
} from "../../mocks/handlers";
import { server } from "../../mocks/node";

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

/** Signs in and opens the settings panel from the sidebar gear. */
async function openSettings(user: UserEvent, locale: typeof en | typeof fa = en) {
  render(<App />);
  await screen.findByRole("heading", { name: locale.login.title });
  await user.type(
    screen.getByLabelText(locale.login.identifierLabel),
    FIXTURE_CREDENTIALS.identifier,
  );
  await user.type(
    screen.getByLabelText(locale.login.passwordLabel),
    FIXTURE_CREDENTIALS.password,
  );
  await user.click(screen.getByRole("button", { name: locale.login.submit }));
  await screen.findByRole("navigation", { name: locale.chat.sidebar.label });
  await user.click(screen.getByRole("button", { name: locale.chat.footer.account }));
  return screen.findByRole("dialog", { name: locale.settings.title });
}

function sessionRows(): HTMLElement[] {
  return within(screen.getByRole("list", { name: en.settings.sessions.title })).getAllByRole(
    "listitem",
  );
}

/** The nth session row, or a failure that names the missing one. */
function sessionRow(index: number): HTMLElement {
  const row = sessionRows()[index];
  if (row === undefined) {
    throw new Error(`the session list has no row ${String(index)}`);
  }
  return row;
}

/** Types into the setup step's code cells, following the auto-advance. */
async function enterSetupCode(user: UserEvent, code: string) {
  const cells = within(
    await screen.findByRole("group", { name: en.settings.totp.verify.codeLabel }),
  ).getAllByRole("textbox");
  const first = cells[0];
  if (first === undefined) {
    throw new Error("the setup code input has no cells");
  }
  await user.click(first);
  await user.keyboard(code);
}

async function openSessions(user: UserEvent) {
  await user.click(screen.getByRole("button", { name: en.settings.security.manageSessions }));
  await screen.findByRole("list", { name: en.settings.sessions.title });
}

describe("settings panel", () => {
  it("opens over the chat as a labelled dialog with a tab list, and Escape closes it", async () => {
    const user = userEvent.setup({ delay: null });
    const panel = await openSettings(user);

    expect(panel).toHaveAttribute("aria-modal", "true");
    // The section nav is a tab list; Security is the section the artboards draw.
    const tabs = within(panel).getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      en.settings.nav.language,
      en.settings.nav.security,
      en.settings.nav.appearance,
    ]);
    expect(
      within(panel).getByRole("tab", { name: en.settings.nav.security }),
    ).toHaveAttribute("aria-selected", "true");
    // The conversation is still there — dimmed and inert, not replaced.
    expect(
      screen.getByRole("navigation", { name: en.chat.sidebar.label }).closest(".hm-chat"),
    ).toHaveAttribute("inert");

    await user.keyboard("{Escape}");

    expect(
      screen.queryByRole("dialog", { name: en.settings.title }),
    ).not.toBeInTheDocument();
    // Focus goes back to the control that opened it.
    expect(screen.getByRole("button", { name: en.chat.footer.account })).toHaveFocus();
  });

  it("moves between sections with the arrow keys", async () => {
    const user = userEvent.setup({ delay: null });
    const panel = await openSettings(user);

    await user.click(within(panel).getByRole("tab", { name: en.settings.nav.security }));
    await user.keyboard("{ArrowDown}");

    expect(
      within(panel).getByRole("tab", { name: en.settings.nav.appearance }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      screen.getByRole("radio", { name: en.settings.appearance.option.dark }),
    ).toBeInTheDocument();
  });
});

describe("two-step verification setup", () => {
  it("reaches the recovery codes and turns two-step on only after the acknowledgement", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);

    // Step 1: the QR, and the manual key visible without any disclosure.
    await user.click(screen.getByRole("button", { name: en.settings.totp.setUp }));
    await screen.findByRole("heading", { name: en.settings.totp.scan.title });
    expect(screen.getByText(en.settings.totp.scan.manualKeyLabel)).toBeInTheDocument();
    expect(
      screen.getByText(
        en.settings.stepOf.replace("{{step, number}}", "1").replace("{{total, number}}", "3"),
      ),
    ).toBeInTheDocument();

    // Step 2: the same OtpInput as the login step, in its compact size.
    await user.click(screen.getByRole("button", { name: en.settings.totp.scan.continue }));
    await enterSetupCode(user, "000000");
    await user.click(screen.getByRole("button", { name: en.settings.totp.verify.submit }));
    // A wrong code does not restart setup.
    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.settings.totp.error.invalidCode,
    );
    expect(
      screen.getByRole("group", { name: en.settings.totp.verify.codeLabel }),
    ).toBeInTheDocument();

    await enterSetupCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.settings.totp.verify.submit }));

    // Step 3: ten codes as selectable text, and two-step still OFF.
    await screen.findByRole("heading", { name: en.settings.totp.codes.title });
    const codes = within(
      screen.getByRole("list", { name: en.settings.totp.codes.listLabel }),
    ).getAllByRole("listitem");
    expect(codes).toHaveLength(10);
    expect(codes[0]).toHaveTextContent(FIXTURE_RECOVERY_CODES[0]);

    const activate = screen.getByRole("button", { name: en.settings.totp.codes.activate });
    expect(activate).toBeDisabled();

    await user.click(screen.getByLabelText(en.settings.totp.codes.acknowledge));
    await user.click(screen.getByRole("button", { name: en.settings.totp.codes.activate }));

    // Back on Security, now reporting the on state.
    expect(await screen.findByText(en.settings.totp.on)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.settings.totp.setUp }),
    ).not.toBeInTheDocument();
  });

  it("re-asks for the password before turning two-step off", async () => {
    const user = userEvent.setup({ delay: null });
    enableMockTotp();
    await openSettings(user);

    await user.click(await screen.findByRole("button", { name: en.settings.totp.disable }));
    const dialog = await screen.findByRole("dialog", {
      name: en.settings.totp.disablePrompt.title,
    });

    await user.type(
      within(dialog).getByLabelText(en.settings.totp.confirmPasswordLabel),
      "not-the-password",
    );
    await user.click(
      within(dialog).getByRole("button", { name: en.settings.totp.disablePrompt.confirm }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      en.settings.totp.error.invalidPassword,
    );

    await user.clear(within(dialog).getByLabelText(en.settings.totp.confirmPasswordLabel));
    await user.type(
      within(dialog).getByLabelText(en.settings.totp.confirmPasswordLabel),
      FIXTURE_CREDENTIALS.password,
    );
    await user.click(
      within(dialog).getByRole("button", { name: en.settings.totp.disablePrompt.confirm }),
    );

    expect(await screen.findByText(en.settings.totp.off)).toBeInTheDocument();
  });
});

describe("sessions", () => {
  it("marks the current device and gives it no sign-out control of its own", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await openSessions(user);

    expect(sessionRows()).toHaveLength(4);
    const currentRow = sessionRow(0);
    expect(currentRow).toHaveTextContent(en.settings.sessions.thisDevice);
    // The badge is paired with the word, so the state is never badge-only.
    expect(currentRow).toHaveTextContent(en.settings.sessions.current);
    expect(
      within(currentRow).queryByRole("button", { name: en.settings.sessions.signOut }),
    ).not.toBeInTheDocument();
    // Every other row has one.
    expect(
      screen.getAllByRole("button", { name: en.settings.sessions.signOut }),
    ).toHaveLength(3);
  });

  it("renders a row whose agent, address and location the contract left out", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await openSessions(user);

    // No user-agent at all: the row still names a device rather than nothing.
    expect(sessionRow(3)).toHaveTextContent(en.settings.sessions.device.unknown);
    // location null renders as the drawn "Unknown location".
    expect(sessionRow(3)).toHaveTextContent(en.settings.sessions.unknownLocation);
    // ip and location both absent: no stray separators, still a device name.
    expect(sessionRow(2)).toHaveTextContent(en.settings.sessions.device.androidPhone);
    expect(sessionRow(2)).toHaveTextContent(en.settings.sessions.unknownLocation);
  });

  it("removes a row when that device is signed out", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await openSessions(user);

    const target = sessionRow(2);
    expect(target).toHaveTextContent(en.settings.sessions.device.androidPhone);
    await user.click(
      within(target).getByRole("button", { name: en.settings.sessions.signOut }),
    );

    await waitFor(() => {
      expect(sessionRows()).toHaveLength(3);
    });
    expect(
      screen.queryByText(en.settings.sessions.device.androidPhone),
    ).not.toBeInTheDocument();
  });

  it("drops sign-out-everywhere-else once nothing else is signed in", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await openSessions(user);

    await user.click(
      screen.getByRole("button", { name: en.settings.sessions.signOutOthers }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: en.settings.sessions.confirmOthers.title_other.replace("{{count}}", "3"),
    });
    await user.click(
      within(dialog).getByRole("button", { name: en.settings.sessions.confirmOthers.confirm }),
    );

    await waitFor(() => {
      expect(sessionRows()).toHaveLength(1);
    });
    // Absent rather than disabled: there is nothing left to explain.
    expect(
      screen.queryByRole("button", { name: en.settings.sessions.signOutOthers }),
    ).not.toBeInTheDocument();
  });
});

describe("language and appearance", () => {
  it("commits the theme on selection and shows the inline saved mark", async () => {
    const user = userEvent.setup({ delay: null });
    const panel = await openSettings(user);

    await user.click(within(panel).getByRole("tab", { name: en.settings.nav.appearance }));
    await user.click(screen.getByRole("radio", { name: en.settings.appearance.option.dark }));

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(screen.getByText(en.settings.saved)).toBeInTheDocument();
  });

  it("renders the panel mirrored, in Persian, when the language is fa", async () => {
    const user = userEvent.setup({ delay: null });
    await i18n.changeLanguage("fa");
    const panel = await openSettings(user, fa);

    expect(document.documentElement.dir).toBe("rtl");
    expect(document.documentElement.lang).toBe("fa");
    // Persian copy, not English fallbacks.
    expect(within(panel).getByText(fa.settings.security.lede)).toBeInTheDocument();
    expect(
      within(panel).getByRole("tab", { name: fa.settings.nav.security }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(panel).getByRole("button", { name: fa.settings.totp.setUp }),
    ).toBeInTheDocument();
  });
});
