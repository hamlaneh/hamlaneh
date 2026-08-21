import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
} from "vitest";

import App from "./App";
import { PASSWORD_MIN_LENGTH } from "./auth/passwordPolicy";
import i18n, { LANGUAGE_STORAGE_KEY } from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";
import { FIXTURE_NEWHIRE_CREDENTIALS, resetMockAuth } from "./mocks/handlers";
import { server } from "./mocks/node";

// App bootstraps the session over the network on mount, so even the language
// tests need the contract mocks (GET /users/me answers 401 -> login screen).
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

/** Renders App and waits for the session bootstrap to settle on the login screen. */
async function renderAppAtLogin() {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
}

/** The switcher is a two-option radio group, each option in its own script. */
function languageOption(code: "en" | "fa"): HTMLElement {
  return screen.getByRole("radio", {
    name: code === "en" ? en.language.shortEn : en.language.shortFa,
  });
}

describe("App", () => {
  beforeEach(async () => {
    window.localStorage.clear();
    await i18n.changeLanguage("en");
  });

  it("renders the login screen in English by default", async () => {
    await renderAppAtLogin();

    expect(screen.getByRole("heading", { name: en.login.title })).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.identifierLabel)).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.passwordLabel)).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
    expect(document.documentElement.style.getPropertyValue("--hm-font-ui")).toBe(
      "var(--font-ui)",
    );
    // The option labels are identical in both locales ("EN" / "فا"), so the
    // group's own accessible name is the only localized signal on the control.
    expect(
      screen.getByRole("radiogroup", { name: en.language.label }),
    ).toBeInTheDocument();
  });

  it("switching to Persian sets RTL, the Persian font stack and the designer's copy", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.click(languageOption("fa"));

    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(document.documentElement.style.getPropertyValue("--hm-font-ui")).toBe(
      "var(--font-ui-fa)",
    );
    expect(screen.getByRole("heading", { name: fa.login.title })).toBeInTheDocument();
    expect(screen.getByLabelText(fa.login.identifierLabel)).toBeInTheDocument();
    // Copy taken verbatim from the login-rtl-fa artboard.
    expect(screen.getByText(fa.login.helper)).toBeInTheDocument();
    expect(screen.getByText(fa.app.tagline)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: fa.password.show })).toBeInTheDocument();
    // "EN" and "فا" read the same in both locales — the group name is where
    // the switcher's own localization shows.
    expect(
      screen.getByRole("radiogroup", { name: fa.language.label }),
    ).toBeInTheDocument();
  });

  it("renders an interpolated number in Persian digits, not Latin ones", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();
    await user.click(languageOption("fa"));

    // The forced-change screen is the only place a number reaches the copy.
    await user.type(
      screen.getByLabelText(fa.login.identifierLabel),
      FIXTURE_NEWHIRE_CREDENTIALS.identifier,
    );
    await user.type(
      screen.getByLabelText(fa.login.passwordLabel),
      FIXTURE_NEWHIRE_CREDENTIALS.password,
    );
    await user.click(screen.getByRole("button", { name: fa.login.submit }));
    await screen.findByRole("heading", { name: fa.changePassword.title });

    const persianMinimum = new Intl.NumberFormat("fa").format(PASSWORD_MIN_LENGTH);
    // Guards the runtime's ICU data as much as the app's formatter.
    expect(persianMinimum).toMatch(/^[۰-۹]+$/u);
    expect(
      screen.getByText(fa.password.minLength.replace("{{minimum, number}}", persianMinimum)),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(
        fa.password.minLength.replace(
          "{{minimum, number}}",
          String(PASSWORD_MIN_LENGTH),
        ),
      ),
    ).not.toBeInTheDocument();
  });

  it("persists the chosen language in localStorage", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.click(languageOption("fa"));

    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("fa");
  });

  it("switching back to English restores LTR", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.click(languageOption("fa"));
    await user.click(languageOption("en"));

    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
    expect(screen.getByRole("heading", { name: en.login.title })).toBeInTheDocument();
  });

  it("keeps a typed identifier when the language changes", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.type(screen.getByLabelText(en.login.identifierLabel), "a.jones");
    await user.click(languageOption("fa"));

    expect(screen.getByLabelText(fa.login.identifierLabel)).toHaveValue("a.jones");
  });
});
