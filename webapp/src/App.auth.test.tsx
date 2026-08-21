import { render, screen, within } from "@testing-library/react";
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
import { PASSWORD_MIN_LENGTH } from "./auth/passwordPolicy";
import App from "./App";
import { AuthShell } from "./components/auth/AuthShell";
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

/**
 * The minimum length is instance policy, so its copy is interpolated — and
 * localized: the placeholder runs through i18next's `number` formatter, which
 * is what puts Persian digits in the `fa` copy (asserted in App.test.tsx).
 */
function tooShortMessage(): string {
  return en.changePassword.error.tooShort.replace(
    "{{minimum, number}}",
    new Intl.NumberFormat("en").format(PASSWORD_MIN_LENGTH),
  );
}

/** The requirements checklist, scoped so more than one requirement can exist. */
function requirementItems(): HTMLElement[] {
  return within(screen.getByRole("list")).getAllByRole("listitem");
}

/** The single requirement item whose visible label matches `label`. */
function requirementItem(label: string): HTMLElement {
  const match = requirementItems().find((item) => item.textContent.includes(label));
  if (match === undefined) {
    throw new Error(`no password requirement labelled "${label}"`);
  }
  return match;
}

/** `password.minLength` as rendered in English, for the given minimum. */
function minLengthLabel(): string {
  return en.password.minLength.replace(
    "{{minimum, number}}",
    new Intl.NumberFormat("en").format(PASSWORD_MIN_LENGTH),
  );
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

/**
 * Counts requests MSW sees until stop() — every request, or only those to
 * `pathname`. Used to assert client-side-only validation and blocked
 * duplicate submissions.
 */
function trackRequests(pathname?: string): { count: () => number; stop: () => void } {
  let calls = 0;
  const onStart = ({ request }: { request: Request }) => {
    if (pathname === undefined || new URL(request.url).pathname === pathname) {
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

    // Non-urgent by design: the rate-limit notice is announced politely.
    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(en.login.error.rateLimited);
  });

  it("never blames the password for a server fault", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/auth/login", () =>
        HttpResponse.json(
          { error: { code: "internal_error", message: "boom" } },
          { status: 500 },
        ),
      ),
    );
    await renderAppAtLogin();

    await submitLogin(
      user,
      FIXTURE_CREDENTIALS.identifier,
      FIXTURE_CREDENTIALS.password,
    );

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.login.error.unexpected);
    expect(alert).not.toHaveTextContent(en.login.error.invalidCredentials);
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
      // Delivered behaviour: focus moves to the first invalid field.
      expect(
        screen.getByLabelText(en.changePassword.currentPasswordLabel),
      ).toHaveFocus();
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
      expect(alert).toHaveTextContent(tooShortMessage());
      expect(requests.count()).toBe(0);
      expect(screen.getByLabelText(en.changePassword.newPasswordLabel)).toHaveFocus();
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
    expect(alert).toHaveTextContent(tooShortMessage());
    expect(screen.getByLabelText(en.changePassword.newPasswordLabel)).toHaveFocus();
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
    expect(
      screen.getByLabelText(en.changePassword.confirmPasswordLabel),
    ).toHaveFocus();
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
    // The server decided this one, but focus still lands on the field at fault.
    expect(
      screen.getByLabelText(en.changePassword.currentPasswordLabel),
    ).toHaveFocus();
  });

  it("does not claim the server was unreachable when it answered 500", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    server.use(
      http.post("/api/v1/auth/change-password", () =>
        HttpResponse.json(
          { error: { code: "internal_error", message: "Something broke." } },
          { status: 500 },
        ),
      ),
    );

    await submitChangePassword(user, {
      current: FIXTURE_NEWHIRE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.changePassword.error.unexpected);
    expect(alert).not.toHaveTextContent(en.changePassword.error.networkError);
    // A form-level failure takes focus itself.
    expect(alert).toHaveFocus();
  });

  it("says the server was unreachable only when the request actually failed", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    server.use(http.post("/api/v1/auth/change-password", () => HttpResponse.error()));

    await submitChangePassword(user, {
      current: FIXTURE_NEWHIRE_CREDENTIALS.password,
      next: VALID_NEW_PASSWORD,
      confirm: VALID_NEW_PASSWORD,
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.changePassword.error.networkError);
    expect(alert).toHaveFocus();
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

describe("delivered design behaviour", () => {
  it("disables submit and keeps the switcher live when rate limited", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, FIXTURE_RATELIMITED_IDENTIFIER, "any-password");
    await screen.findByRole("status");

    expect(screen.getByRole("button", { name: en.login.submit })).toBeDisabled();
    // The switcher stays live, so the notice can still be read in either
    // language while the form is blocked.
    expect(screen.getByRole("radio", { name: en.language.shortFa })).toBeEnabled();
    // The fields stay editable: the 429 carries no Retry-After yet, so the
    // edit-and-retry path is the only exit that is not an invented timer.
    expect(screen.getByLabelText(en.login.identifierLabel)).toBeEnabled();
    expect(screen.getByLabelText(en.login.passwordLabel)).toBeEnabled();
  });

  it("recovers from a rate limit as soon as the identifier is edited", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, FIXTURE_RATELIMITED_IDENTIFIER, "any-password");
    await screen.findByRole("status");

    const identifier = screen.getByLabelText(en.login.identifierLabel);
    await user.clear(identifier);
    await user.type(identifier, FIXTURE_CREDENTIALS.identifier);

    // The notice goes with the state it described, and submit comes back.
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.login.submit })).toBeEnabled();

    const requests = trackRequests("/api/v1/auth/login");
    try {
      await user.type(
        screen.getByLabelText(en.login.passwordLabel),
        FIXTURE_CREDENTIALS.password,
      );
      await user.click(screen.getByRole("button", { name: en.login.submit }));

      expect(
        await screen.findByRole("heading", { name: en.home.title }),
      ).toBeInTheDocument();
      // The retry actually reached the network — the screen is not inert.
      expect(requests.count()).toBe(1);
    } finally {
      requests.stop();
    }
  });

  it("marks the not-yet-available reset link as a disabled link, not prose", async () => {
    await renderAppAtLogin();

    const link = screen.getByText(en.login.forgotPassword);
    expect(link).toHaveAttribute("role", "link");
    expect(link).toHaveAttribute("aria-disabled", "true");
  });

  it("keeps the identifier, clears the password and moves focus to the alert", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();

    await submitLogin(user, FIXTURE_CREDENTIALS.identifier, "wrong-password");

    const alert = await screen.findByRole("alert");
    expect(screen.getByLabelText(en.login.identifierLabel)).toHaveValue(
      FIXTURE_CREDENTIALS.identifier,
    );
    expect(screen.getByLabelText(en.login.passwordLabel)).toHaveValue("");
    expect(alert).toHaveFocus();
  });

  it("shows the busy label during submission and blocks a duplicate submit", async () => {
    const user = userEvent.setup();
    await renderAppAtLogin();
    // The busy window is held open by the test, not by wall-clock delay: the
    // handler answers only once `releaseLogin` is called, so a loaded runner
    // can never miss the busy label.
    let releaseLogin: (() => void) | undefined;
    const loginGate = new Promise<void>((resolve) => {
      releaseLogin = resolve;
    });
    server.use(
      http.post("/api/v1/auth/login", async () => {
        await loginGate;
        return HttpResponse.json(
          { error: { code: "invalid_credentials", message: "Invalid credentials." } },
          { status: 401 },
        );
      }),
    );
    const requests = trackRequests("/api/v1/auth/login");

    try {
      await submitLogin(user, FIXTURE_CREDENTIALS.identifier, "wrong-password");

      const busy = await screen.findByRole("button", { name: en.login.submitting });
      expect(busy).toBeDisabled();
      await user.click(busy);

      releaseLogin?.();
      await screen.findByRole("alert");
      expect(requests.count()).toBe(1);
    } finally {
      // Never leave the handler (and therefore the request) hanging if an
      // assertion above threw.
      releaseLogin?.();
      requests.stop();
    }
  });

  it("reflects the typed password in the requirements checklist", async () => {
    const user = userEvent.setup();
    await loginAsNewhire(user);
    const newPassword = screen.getByLabelText(en.changePassword.newPasswordLabel);
    const minLength = minLengthLabel();

    expect(requirementItem(minLength)).toHaveTextContent(en.password.requirementUnmet);

    await user.type(newPassword, "short");
    expect(requirementItem(minLength)).toHaveTextContent(en.password.requirementUnmet);

    await user.clear(newPassword);
    await user.type(newPassword, VALID_NEW_PASSWORD);
    expect(requirementItem(minLength)).toHaveTextContent(en.password.requirementMet);
  });

  it("renders the organization mark when the instance has one", () => {
    const logoUrl = "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg'/%3E";
    const { container } = render(
      <AuthShell organization={{ name: "Sanjab Cooperative", logoUrl }}>
        <p>form</p>
      </AuthShell>,
    );

    expect(screen.getByText("Sanjab Cooperative")).toBeInTheDocument();
    const logo = container.querySelector(".hm-identity__logo");
    expect(logo).toHaveAttribute("src", logoUrl);
    // Decorative: the name beside it carries the accessible text.
    expect(logo).toHaveAttribute("alt", "");
  });

  it("renders no organization block at all when the instance has none", () => {
    const { container } = render(
      <AuthShell>
        <p>form</p>
      </AuthShell>,
    );

    // No box, no reserved height, no residual gap — the slot is simply absent.
    expect(container.querySelector(".hm-identity__organization")).toBeNull();
  });
});
