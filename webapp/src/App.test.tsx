import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import App from "./App";
import i18n, { LANGUAGE_STORAGE_KEY } from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";

describe("App", () => {
  beforeEach(async () => {
    window.localStorage.clear();
    await i18n.changeLanguage("en");
  });

  it("renders the login screen in English by default", () => {
    render(<App />);

    expect(
      screen.getByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.usernameLabel)).toBeInTheDocument();
    expect(screen.getByLabelText(en.login.passwordLabel)).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("switching to Persian sets RTL and renders Persian strings", async () => {
    const user = userEvent.setup();
    render(<App />);

    await user.selectOptions(
      screen.getByRole("combobox", { name: en.language.label }),
      "fa",
    );

    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(
      screen.getByRole("heading", { name: fa.login.title }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(fa.login.usernameLabel)).toBeInTheDocument();
  });

  it("persists the chosen language in localStorage", async () => {
    const user = userEvent.setup();
    render(<App />);

    await user.selectOptions(
      screen.getByRole("combobox", { name: en.language.label }),
      "fa",
    );

    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("fa");
  });

  it("switching back to English restores LTR", async () => {
    const user = userEvent.setup();
    render(<App />);

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
