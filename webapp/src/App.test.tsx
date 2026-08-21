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
import i18n, { LANGUAGE_STORAGE_KEY } from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";
import { resetMockAuth } from "./mocks/handlers";
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

describe("App", () => {
  beforeEach(async () => {
    window.localStorage.clear();
    await i18n.changeLanguage("en");
  });

  it("renders the login screen in English by default", async () => {
    await renderAppAtLogin();

    expect(
      screen.getByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.identifierLabel)).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.passwordLabel)).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("switching to Persian sets RTL and renders Persian strings", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.selectOptions(
      screen.getByRole("combobox", { name: en.language.label }),
      "fa",
    );

    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(
      screen.getByRole("heading", { name: fa.login.title }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(fa.login.identifierLabel)).toBeInTheDocument();
  });

  it("persists the chosen language in localStorage", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await user.selectOptions(
      screen.getByRole("combobox", { name: en.language.label }),
      "fa",
    );

    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("fa");
  });

  it("switching back to English restores LTR", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    const switcher = screen.getByRole("combobox", { name: en.language.label });
    await user.selectOptions(switcher, "fa");
    await user.selectOptions(
      screen.getByRole("combobox", { name: fa.language.label }),
      "en",
    );

    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
    expect(
      screen.getByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
  });
});
