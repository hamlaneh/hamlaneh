import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import {
  afterAll,
  afterEach,
  beforeAll,
  describe,
  expect,
  it,
} from "vitest";

import { api } from "./api/client";
import App from "./App";
import en from "./locales/en/common.json";
import {
  expireMockAccess,
  FIXTURE_ADMIN,
  FIXTURE_CREDENTIALS,
  FIXTURE_NEWHIRE,
  FIXTURE_NEWHIRE_CREDENTIALS,
  FIXTURE_RATELIMITED_IDENTIFIER,
  resetMockAuth,
} from "./mocks/handlers";
import { server } from "./mocks/node";

const VALID_NEW_PASSWORD = "brand-new-password-1234";

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

function signedInAs(name: string): string {
  return en.home.signedInAs.replace("{{name}}", name);
}

/** Renders App and waits for the session bootstrap to settle on the login screen. */
async function renderAppAtLogin() {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
}

async function submitLogin(user: UserEvent, identifier: string, password: string) {
  await user.type(screen.getByLabelText(en.login.identifierLabel), identifier);
  await user.type(screen.getByLabelText(en.login.passwordLabel), password);
  await user.click(screen.getByRole("button", { name: en.login.submit }));
}

async function loginAsNewhire(user: UserEvent) {
  await renderAppAtLogin();
  await submitLogin(
    user,
    FIXTURE_NEWHIRE_CREDENTIALS.identifier,
    FIXTURE_NEWHIRE_CREDENTIALS.password,
  );
  await screen.findByRole("heading", { name: en.changePassword.title });
}

/** Fills and submits the change-password form; empty fields are left untouched (userEvent.type rejects ""). */
async function submitChangePassword(
  user: UserEvent,
  fields: { current: string; next: string; confirm: string },
) {
  if (fields.current !== "") {
    await user.type(
      screen.getByLabelText(en.changePassword.currentPasswordLabel),
      fields.current,
    );
  }
  if (fields.next !== "") {
    await user.type(
      screen.getByLabelText(en.changePassword.newPasswordLabel),
      fields.next,
    );
  }
  if (fields.confirm !== "") {
    await user.type(
      screen.getByLabelText(en.changePassword.confirmPasswordLabel),
      fields.confirm,
    );
  }
  await user.click(
    screen.getByRole("button", { name: en.changePassword.submit }),
  );
}

/** Counts every request MSW sees until stop() — for asserting client-side-only validation. */
function trackRequests(): { count: () => number; stop: () => void } {
  let calls = 0;
  const onStart = () => {
    calls += 1;
  };
  server.events.on("request:start", onStart);
  return {
    count: () => calls,
    stop: () => {
      server.events.removeListener("request:start", onStart);
    },
  };
}

describe("login", () => {
  it("lands on Home showing the display name after a successful login", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(
      user,
      FIXTURE_CREDENTIALS.identifier,
      FIXTURE_CREDENTIALS.password,
    );

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(signedInAs(FIXTURE_ADMIN.display_name)),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.login.title }),
    ).not.toBeInTheDocument();
  });

  it("shows the generic error for a wrong password", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, FIXTURE_CREDENTIALS.identifier, "wrong-password");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.login.error.invalidCredentials);
  });

  it("shows the identical generic error for an unknown user", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, "no.such.user", "irrelevant-password");

    // Same message as the wrong-password case: the UI must not help
    // account enumeration either.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.login.error.invalidCredentials);
  });

  it("shows the rate-limit message on 429", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, FIXTURE_RATELIMITED_IDENTIFIER, "any-password");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.login.error.rateLimited);
  });
});

describe("session bootstrap", () => {
  it("lands on Home directly when a session already exists", async () => {
    await api.POST("/api/v1/auth/login", { body: { ...FIXTURE_CREDENTIALS } });

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.login.title }),
    ).not.toBeInTheDocument();
  });

  it("lands on Home via a transparent refresh when only the access session expired", async () => {
    await api.POST("/api/v1/auth/login", { body: { ...FIXTURE_CREDENTIALS } });
    expireMockAccess();

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.login.title }),
    ).not.toBeInTheDocument();
  });

  it("lands on the login screen when the refresh itself fails", async () => {
    await api.POST("/api/v1/auth/login", { body: { ...FIXTURE_CREDENTIALS } });
    expireMockAccess();
    // Refresh token revoked (e.g. reuse detection): refresh answers 401,
    // so the bootstrap 401 propagates and the user must sign in again.
    server.use(
      http.post("/api/v1/auth/refresh", () =>
        HttpResponse.json(
          {
            error: {
              code: "not_authenticated",
              message: "Refresh token revoked.",
            },
          },
          { status: 401 },
        ),
      ),
    );

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.home.title }),
    ).not.toBeInTheDocument();
  });
});

describe("forced password change", () => {
  it("routes a must-change user to the forced screen where sign-out is the only escape", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);

    expect(screen.getByText(en.changePassword.forcedNotice)).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.home.title }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: en.changePassword.cancel }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: en.changePassword.signOut }),
    ).toBeInTheDocument();
  });

  it("returns to the login screen when the forced user signs out", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);

    await user.click(
      screen.getByRole("button", { name: en.changePassword.signOut }),
    );

    expect(
      await screen.findByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(en.changePassword.forcedNotice),
    ).not.toBeInTheDocument();
  });

  it("shows the current-password-required error without any request when current is empty", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    const requests = trackRequests();

    try {
      await submitChangePassword(user, {
        current: "",
        next: VALID_NEW_PASSWORD,
        confirm: VALID_NEW_PASSWORD,
      });

      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(en.changePassword.error.currentRequired);
      expect(requests.count()).toBe(0);
    } finally {
      requests.stop();
    }
  });

  it("shows the too-short error client-side without any request for a short new password", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    const requests = trackRequests();

    try {
      await submitChangePassword(user, {
        current: FIXTURE_NEWHIRE_CREDENTIALS.password,
        next: "short",
        confirm: "short",
      });

      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(en.changePassword.error.tooShort);
      expect(requests.count()).toBe(0);
    } finally {
      requests.stop();
    }
  });

  it("falls back to the too-short message when the server answers 400", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    // A password that passes the client-side checks but a stricter server
    // policy rejects: the 400 fallback mapping must still show something apt.
    server.use(
      http.post("/api/v1/auth/change-password", () =>
        HttpResponse.json(
          {
            error: {
              code: "invalid_request",
              message: "Password does not meet the policy.",
            },
          },
          { status: 400 },
        ),
      ),
    );

    await submitChangePassword(user, {
      current: FIXTURE_NEWHIRE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.changePassword.error.tooShort);
  });

  it("shows the mismatch error before any request when confirmation differs", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);

    await submitChangePassword(user, {
      current: FIXTURE_NEWHIRE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: `${VALID_NEW_PASSWORD}-typo`,
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.changePassword.error.mismatch);
  });

  it("shows the invalid-current-password error on 403", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);

    await submitChangePassword(user, {
      current: "not-the-current-password",
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.changePassword.error.invalidCurrentPassword);
  });

  it("lands on Home with the flag cleared after a successful change", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);

    await submitChangePassword(user, {
      current: FIXTURE_NEWHIRE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(signedInAs(FIXTURE_NEWHIRE.display_name)),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(en.changePassword.forcedNotice),
    ).not.toBeInTheDocument();
  });
});

describe("voluntary password change", () => {
  async function openFromHome(user: UserEvent) {
    await renderAppAtLogin();
    await submitLogin(
      user,
      FIXTURE_CREDENTIALS.identifier,
      FIXTURE_CREDENTIALS.password,
    );
    await screen.findByRole("heading", { name: en.home.title });
    await user.click(
      screen.getByRole("button", { name: en.home.changePasswordLink }),
    );
    await screen.findByRole("heading", { name: en.changePassword.title });
  }

  it("opens from Home without the forced notice and can be cancelled", async () => {
    const user = userEvent.setup();
    await openFromHome(user);

    expect(
      screen.queryByText(en.changePassword.forcedNotice),
    ).not.toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: en.changePassword.cancel }),
    );

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
  });

  it("returns to Home after a successful voluntary change", async () => {
    const user = userEvent.setup();
    await openFromHome(user);

    await submitChangePassword(user, {
      current: FIXTURE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    expect(
      await screen.findByRole("heading", { name: en.home.title }),
    ).toBeInTheDocument();
  });
});

describe("logout", () => {
  it("returns to the login screen", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();
    await submitLogin(
      user,
      FIXTURE_CREDENTIALS.identifier,
      FIXTURE_CREDENTIALS.password,
    );
    await screen.findByRole("heading", { name: en.home.title });

    await user.click(screen.getByRole("button", { name: en.home.logout }));

    expect(
      await screen.findByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: en.home.title }),
    ).not.toBeInTheDocument();
  });
});
