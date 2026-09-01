import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";

import App from "./App";
import i18n, { LANGUAGE_STORAGE_KEY } from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";
import {
  FIXTURE_ADMIN,
  FIXTURE_CREDENTIALS,
  FIXTURE_MEMBER_CREDENTIALS,
  resetMockAuth,
} from "./mocks/handlers";
import { server } from "./mocks/node";

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

/** Counts requests MSW sees for one method and path, until stop(). */
function countRequests(method: string, pathname: string) {
  let calls = 0;
  const onStart = ({ request }: { request: Request }) => {
    if (request.method === method && new URL(request.url).pathname === pathname) {
      calls += 1;
    }
  };
  server.events.on("request:start", onStart);
  return {
    count: () => calls,
    stop: () => {
      server.events.removeListener("request:start", onStart);
    },
  };
}

async function signIn(
  user: UserEvent,
  credentials: { identifier: string; password: string },
) {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
  await user.type(
    screen.getByLabelText(en.login.identifierLabel),
    credentials.identifier,
  );
  await user.type(screen.getByLabelText(en.login.passwordLabel), credentials.password);
  await user.click(screen.getByRole("button", { name: en.login.submit }));
}

/** The account menu carries the signed-in half of the language switcher. */
async function openAccountMenu(user: UserEvent) {
  await user.click(screen.getByRole("button", { name: en.account.title }));
  await screen.findByRole("dialog", { name: en.account.title });
}

/** The switcher labels its options in their own script, in every locale. */
function languageOption(code: "en" | "fa"): HTMLElement {
  return screen.getByRole("radio", {
    name: code === "en" ? en.language.shortEn : en.language.shortFa,
  });
}

describe("the account's language", () => {
  beforeEach(async () => {
    // Nothing is mounted yet, so this changes the language without any
    // listener seeing it as somebody's choice.
    window.localStorage.clear();
    await i18n.changeLanguage("en");
  });

  it("takes over from what this browser remembered, at sign-in", async () => {
    const user = userEvent.setup({ delay: null });
    // This browser last showed English — a previous person's session, or this
    // person before they chose Persian on another device.
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, "en");
    const patches = countRequests("PATCH", "/api/v1/users/me");

    await signIn(user, FIXTURE_MEMBER_CREDENTIALS);

    expect(
      await screen.findByRole("navigation", { name: fa.chat.sidebar.label }),
    ).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    // The browser's fallback follows, so the next visit to the sign-in screen
    // opens in the language this machine last showed.
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("fa");
    // Reading the account's own locale is not a change to it: applying it
    // must never write it straight back.
    expect(patches.count()).toBe(0);
    patches.stop();
  });

  it("applies a switch immediately, while the save is still in flight", async () => {
    const user = userEvent.setup({ delay: null });
    const saved: unknown[] = [];
    let releaseSave: (() => void) | undefined;
    const held = new Promise<void>((resolve) => {
      releaseSave = resolve;
    });
    server.use(
      http.patch("/api/v1/users/me", async ({ request }) => {
        saved.push(await request.json());
        await held;
        return HttpResponse.json({ ...FIXTURE_ADMIN, locale: "fa" });
      }),
    );

    await signIn(user, FIXTURE_CREDENTIALS);
    await screen.findByRole("navigation", { name: en.chat.sidebar.label });
    await openAccountMenu(user);

    await user.click(languageOption("fa"));

    // Nothing here awaited the network: the save above cannot answer until
    // releaseSave runs, and the interface is already Persian.
    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(screen.getByRole("dialog", { name: fa.account.title })).toBeInTheDocument();

    await waitFor(() => {
      expect(saved).toEqual([{ locale: "fa" }]);
    });
    releaseSave?.();
  });

  it("keeps the chosen language when the save fails", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const user = userEvent.setup({ delay: null });
    server.use(
      http.patch("/api/v1/users/me", () =>
        HttpResponse.json(
          { error: { code: "internal_error", message: "internal server error" } },
          { status: 500 },
        ),
      ),
    );

    await signIn(user, FIXTURE_CREDENTIALS);
    await screen.findByRole("navigation", { name: en.chat.sidebar.label });
    await openAccountMenu(user);
    await user.click(languageOption("fa"));

    await waitFor(() => {
      expect(warn).toHaveBeenCalled();
    });
    // The choice stands: a failed write is not a reason to yank the interface
    // back under somebody who just chose.
    expect(document.documentElement.lang).toBe("fa");
    expect(languageOption("fa")).toBeChecked();
    warn.mockRestore();
  });

  it("saves a change of mind made while the first save is still in flight", async () => {
    const user = userEvent.setup({ delay: null });
    const sent: string[] = [];
    let releaseFirst: (() => void) | undefined;
    const heldFirst = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    server.use(
      http.patch("/api/v1/users/me", async ({ request }) => {
        const { locale } = (await request.json()) as { locale: "en" | "fa" };
        sent.push(locale);
        if (sent.length === 1) {
          await heldFirst;
        }
        return HttpResponse.json({ ...FIXTURE_ADMIN, locale });
      }),
    );

    await signIn(user, FIXTURE_CREDENTIALS);
    await screen.findByRole("navigation", { name: en.chat.sidebar.label });
    await openAccountMenu(user);

    // Persian, then straight back to English before the first save lands —
    // one impatient double-click, and the interval that makes it possible is
    // any real network.
    await user.click(languageOption("fa"));
    await user.click(languageOption("en"));
    releaseFirst?.();

    // The second switch has to reach the server. Judging it against the last
    // *confirmed* locale would compare "en" to a stale "en", call it
    // redundant, and send nothing — and the first response would then record
    // Persian on an account whose owner is looking at English, permanently.
    await waitFor(() => {
      expect(sent).toEqual(["fa", "en"]);
    });
    expect(document.documentElement.lang).toBe("en");
  });

  it("follows the second account when one person signs out and another in", async () => {
    const user = userEvent.setup({ delay: null });

    await signIn(user, FIXTURE_CREDENTIALS);
    await screen.findByRole("navigation", { name: en.chat.sidebar.label });
    await openAccountMenu(user);
    await user.click(screen.getByRole("button", { name: en.account.logout }));

    // A shared machine: the browser now remembers English from the account
    // that just left, and the next person reads Persian.
    await screen.findByRole("heading", { name: en.login.title });
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("en");

    await user.type(
      screen.getByLabelText(en.login.identifierLabel),
      FIXTURE_MEMBER_CREDENTIALS.identifier,
    );
    await user.type(
      screen.getByLabelText(en.login.passwordLabel),
      FIXTURE_MEMBER_CREDENTIALS.password,
    );
    await user.click(screen.getByRole("button", { name: en.login.submit }));

    expect(
      await screen.findByRole("navigation", { name: fa.chat.sidebar.label }),
    ).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("fa");
  });
});
