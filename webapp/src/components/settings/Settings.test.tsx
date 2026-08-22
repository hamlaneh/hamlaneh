import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import App from "../../App";
import i18n from "../../i18n";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import {
  enableMockTotp,
  FIXTURE_CREDENTIALS,
  FIXTURE_RECOVERY_CODES,
  FIXTURE_RETRY_AFTER_SECONDS,
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

/**
 * Answers `path`'s 429 with `header` as its Retry-After, or with none at all
 * when it is undefined — the shapes a server that cannot say when the door
 * reopens produces. Every account-security 429 carries the header for real
 * (spec: RateLimited).
 */
function rateLimitedWith(path: string, header?: string) {
  server.use(
    http.post(path, () =>
      HttpResponse.json(
        { error: { code: "rate_limited", message: "Too many attempts." } },
        {
          status: 429,
          ...(header === undefined ? {} : { headers: { "Retry-After": header } }),
        },
      ),
    ),
  );
}

/** A count-carrying sentence as i18next renders it in English. */
function counted(template: string, count: number): string {
  return template.replace("{{count, number}}", new Intl.NumberFormat("en").format(count));
}

/** Every quiet live region on screen: the panel's own and the chat's behind it. */
function noticeBanners(): HTMLElement[] {
  return screen
    .queryAllByRole("status")
    .filter((notice) => notice.classList.contains("hm-banner"));
}

/**
 * The form-level notice banner. Several quiet regions share `role="status"` —
 * the composer behind the panel, the setup step's loading note — so the banner
 * is picked out by the component's own class rather than by role alone.
 */
async function findNoticeBanner(): Promise<HTMLElement> {
  return waitFor(() => {
    const [banner] = noticeBanners();
    if (banner === undefined) {
      throw new Error("no notice banner on screen");
    }
    return banner;
  });
}

/** Opens the disable prompt on an account that has two-step on, and confirms it. */
async function confirmDisable(user: UserEvent) {
  await user.click(await screen.findByRole("button", { name: en.settings.totp.disable }));
  const dialog = await screen.findByRole("dialog", {
    name: en.settings.totp.disablePrompt.title,
  });
  await user.type(
    within(dialog).getByLabelText(en.settings.totp.confirmPasswordLabel),
    FIXTURE_CREDENTIALS.password,
  );
  await user.click(
    within(dialog).getByRole("button", { name: en.settings.totp.disablePrompt.confirm }),
  );
  return dialog;
}

describe("rate limiting in account security", () => {
  it("states the wait the password prompt's 429 names rather than guessing at it", async () => {
    const user = userEvent.setup({ delay: null });
    enableMockTotp();
    await openSettings(user);
    rateLimitedWith("/api/v1/users/me/totp/disable", String(FIXTURE_RETRY_AFTER_SECONDS));

    const dialog = await confirmDisable(user);

    const notice = await within(dialog).findByRole("alert");
    expect(notice).toHaveTextContent(
      counted(
        en.login.error.rateLimitedMinutes_other,
        Math.ceil(FIXTURE_RETRY_AFTER_SECONDS / 60),
      ),
    );
    // The undated wording is what this replaces, not what it repeats.
    expect(notice).not.toHaveTextContent(en.settings.totp.error.rateLimited);
  });

  it.each([
    ["carries no Retry-After", undefined],
    ["carries a Retry-After of zero", "0"],
    ["carries a non-numeric Retry-After", "soon"],
    ["carries an HTTP-date Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"],
    ["carries an absurd Retry-After", "999999999"],
  ])("falls back to the undated prompt wording when the 429 %s", async (_case, header) => {
    const user = userEvent.setup({ delay: null });
    enableMockTotp();
    await openSettings(user);
    rateLimitedWith("/api/v1/users/me/totp/disable", header);

    const dialog = await confirmDisable(user);

    const notice = await within(dialog).findByRole("alert");
    expect(notice).toHaveTextContent(en.settings.totp.error.rateLimited);
    // Never a number the response did not support — and never "NaN".
    expect(notice.textContent).not.toMatch(/\d|NaN/u);
  });

  it("keeps the prompt's own way out, and reopens it clean", async () => {
    const user = userEvent.setup({ delay: null });
    enableMockTotp();
    await openSettings(user);
    rateLimitedWith("/api/v1/users/me/totp/disable", String(FIXTURE_RETRY_AFTER_SECONDS));

    const dialog = await confirmDisable(user);
    await within(dialog).findByRole("alert");

    // Cancel still closes it — a stated wait must not trap anyone in a dialog.
    await user.click(within(dialog).getByRole("button", { name: en.settings.cancel }));
    expect(
      screen.queryByRole("dialog", { name: en.settings.totp.disablePrompt.title }),
    ).not.toBeInTheDocument();

    // Reopened, it carries no leftover notice, and the action still works once
    // the server's window has slid.
    server.resetHandlers();
    await confirmDisable(user);
    expect(await screen.findByText(en.settings.totp.off)).toBeInTheDocument();
  });

  it("lifts the prompt's notice by itself once the stated wait has passed", async () => {
    const user = userEvent.setup({ delay: null });
    enableMockTotp();
    await openSettings(user);
    // One second, so the countdown runs out inside the test rather than being
    // simulated: what is asserted is that the timer really ends the state.
    rateLimitedWith("/api/v1/users/me/totp/disable", "1");

    const dialog = await confirmDisable(user);
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      counted(en.login.error.rateLimitedSeconds_one, 1),
    );

    await waitFor(
      () => {
        expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
      },
      { timeout: 3000 },
    );
    // The dialog is still open and still usable — only the notice went.
    expect(
      within(dialog).getByRole("button", { name: en.settings.totp.disablePrompt.confirm }),
    ).toBeEnabled();
  });

  it("states the wait a setup 429 names, and keeps the way back to Security", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    rateLimitedWith("/api/v1/users/me/totp/setup", "45");

    await user.click(screen.getByRole("button", { name: en.settings.totp.setUp }));

    // Under a minute the notice counts seconds, exactly as the sign-in screen
    // does — the same sentence, only the number is new.
    const notice = await findNoticeBanner();
    expect(notice).toHaveTextContent(counted(en.login.error.rateLimitedSeconds_other, 45));
    expect(notice).not.toHaveTextContent(en.settings.totp.error.rateLimited);

    // The step header's back link is still the way out it always was.
    await user.click(screen.getByRole("button", { name: en.settings.nav.security }));
    expect(
      await screen.findByRole("button", { name: en.settings.totp.setUp }),
    ).toBeInTheDocument();
  });

  it.each([
    ["carries no Retry-After", undefined],
    ["carries a non-numeric Retry-After", "soon"],
    ["carries an absurd Retry-After", "999999999"],
  ])("falls back to the undated setup wording when the 429 %s", async (_case, header) => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    rateLimitedWith("/api/v1/users/me/totp/setup", header);

    await user.click(screen.getByRole("button", { name: en.settings.totp.setUp }));

    const notice = await findNoticeBanner();
    expect(notice).toHaveTextContent(en.settings.totp.error.rateLimited);
    expect(notice.textContent).not.toMatch(/\d|NaN/u);
  });

  it("states the wait a verify 429 names and lifts it when the code is retyped", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await user.click(screen.getByRole("button", { name: en.settings.totp.setUp }));
    await screen.findByRole("heading", { name: en.settings.totp.scan.title });
    await user.click(screen.getByRole("button", { name: en.settings.totp.scan.continue }));
    rateLimitedWith("/api/v1/users/me/totp/verify", String(FIXTURE_RETRY_AFTER_SECONDS));

    await enterSetupCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.settings.totp.verify.submit }));

    const notice = await findNoticeBanner();
    expect(notice).toHaveTextContent(
      counted(
        en.login.error.rateLimitedMinutes_other,
        Math.ceil(FIXTURE_RETRY_AFTER_SECONDS / 60),
      ),
    );

    // The pending setup survives, exactly as it did before the countdown: the
    // cells cleared, typing again lifts the notice, and the code goes through
    // once the server's window has slid.
    server.resetHandlers();
    await enterSetupCode(user, FIXTURE_TOTP_CODE);
    expect(noticeBanners()).toEqual([]);
    await user.click(screen.getByRole("button", { name: en.settings.totp.verify.submit }));

    expect(
      await screen.findByRole("heading", { name: en.settings.totp.codes.title }),
    ).toBeInTheDocument();
  });

  it("says why the automatic restart failed instead of repeating the expiry notice", async () => {
    const user = userEvent.setup({ delay: null });
    await openSettings(user);
    await user.click(screen.getByRole("button", { name: en.settings.totp.setUp }));
    await screen.findByRole("heading", { name: en.settings.totp.scan.title });
    await user.click(screen.getByRole("button", { name: en.settings.totp.scan.continue }));

    // The pending setup expires while the code is being typed, so the screen
    // says "start again" and fetches a fresh secret by itself — and that fetch
    // is refused. Every non-200 there used to be dropped, leaving the user
    // following an instruction that could not work, with nothing saying so.
    server.use(
      http.post("/api/v1/users/me/totp/verify", () =>
        HttpResponse.json(
          { error: { code: "totp_setup_expired", message: "That setup expired." } },
          { status: 409 },
        ),
      ),
    );
    rateLimitedWith("/api/v1/users/me/totp/setup", "45");

    await enterSetupCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.settings.totp.verify.submit }));

    const notice = await findNoticeBanner();
    expect(notice).toHaveTextContent(counted(en.login.error.rateLimitedSeconds_other, 45));
    // The refusal is the newer fact, so it replaces the expiry line rather
    // than leaving both on screen contradicting each other.
    expect(notice).not.toHaveTextContent(en.settings.totp.error.setupExpired);
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
