import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import App from "./App";
import { forgetResetToken } from "./auth/resetToken";
import { RESET_TOKEN_TTL_MINUTES } from "./auth/passwordPolicy";
import en from "./locales/en/common.json";
import {
  FIXTURE_RESET_TOKEN,
  FIXTURE_RETRY_AFTER_SECONDS,
  resetMockAuth,
  setMockInstance,
} from "./mocks/handlers";
import { server } from "./mocks/node";

const VALID_NEW_PASSWORD = "brand-new-password-1234";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  // The token is read once per page load; each case is a fresh load.
  forgetResetToken();
});

afterEach(() => {
  server.resetHandlers();
  resetMockAuth();
  window.history.replaceState({}, "", "/");
});

afterAll(() => {
  server.close();
});

/** Puts a reset link's fragment on the URL, exactly as the email would. */
function arriveFromResetLink(token: string) {
  window.history.replaceState({}, "", `/reset#token=${token}`);
  forgetResetToken();
}

async function openResetRequest(user: UserEvent) {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
  await user.click(await screen.findByRole("button", { name: en.login.forgotPassword }));
  await screen.findByRole("heading", { name: en.resetRequest.title });
}

describe("password reset request", () => {
  it("shows the same enumeration-safe confirmation for an unknown address", async () => {
    const user = userEvent.setup({ delay: null });
    await openResetRequest(user);

    await user.type(
      screen.getByLabelText(en.resetRequest.emailLabel),
      "nobody.at.all@example.invalid",
    );
    await user.click(screen.getByRole("button", { name: en.resetRequest.submit }));

    // Announced politely, never as an alert: nothing failed, and the copy
    // names no identity either way.
    const confirmation = await screen.findByRole("status");
    expect(confirmation).toHaveTextContent(en.resetRequest.confirmation);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      screen.queryByText("nobody.at.all@example.invalid"),
    ).not.toBeInTheDocument();
    // The contract's own lifetime, not the artboard's "one hour".
    expect(
      screen.getByText(
        en.resetRequest.confirmationHelper.replace(
          "{{minutes, number}}",
          new Intl.NumberFormat("en").format(RESET_TOKEN_TTL_MINUTES),
        ),
      ),
    ).toBeInTheDocument();
  });

  it("hides the reset link entirely when the instance has no mail transport", async () => {
    setMockInstance({ password_reset_available: false });
    render(<App />);
    await screen.findByRole("heading", { name: en.login.title });
    // The password field is proof the form finished rendering, so the link's
    // absence is a decision rather than a race.
    expect(screen.getByLabelText(en.login.passwordLabel)).toBeInTheDocument();

    expect(screen.queryByText(en.login.forgotPassword)).not.toBeInTheDocument();
  });
});

describe("reset link", () => {
  it("completes the reset and scrubs the token out of the address bar", async () => {
    const user = userEvent.setup({ delay: null });
    arriveFromResetLink(FIXTURE_RESET_TOKEN);

    render(<App />);
    await screen.findByRole("heading", { name: en.resetPassword.title });

    // A fragment is never sent to a server; the client's half of the bargain
    // is to keep it out of history the moment it is read.
    expect(window.location.hash).toBe("");
    expect(window.location.href).not.toContain(FIXTURE_RESET_TOKEN);

    await user.type(
      screen.getByLabelText(en.resetPassword.newPasswordLabel),
      VALID_NEW_PASSWORD,
    );
    await user.type(
      screen.getByLabelText(en.resetPassword.confirmPasswordLabel),
      VALID_NEW_PASSWORD,
    );
    await user.click(screen.getByRole("button", { name: en.resetPassword.submit }));

    // Every session family is revoked and no cookies are set: sign in fresh.
    await screen.findByRole("heading", { name: en.login.title });
    expect(await screen.findByText(en.resetPassword.done)).toBeInTheDocument();
  });

  it("shows one message for an unknown, expired or already-used token", async () => {
    const user = userEvent.setup({ delay: null });
    arriveFromResetLink("some-other-token-that-is-not-live");

    render(<App />);
    await screen.findByRole("heading", { name: en.resetPassword.title });

    await user.type(
      screen.getByLabelText(en.resetPassword.newPasswordLabel),
      VALID_NEW_PASSWORD,
    );
    await user.type(
      screen.getByLabelText(en.resetPassword.confirmPasswordLabel),
      VALID_NEW_PASSWORD,
    );
    await user.click(screen.getByRole("button", { name: en.resetPassword.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.resetPassword.error.invalidToken);
    // The only action that helps is offered right there.
    expect(
      screen.getByRole("button", { name: en.resetPassword.requestNewLink }),
    ).toBeInTheDocument();
  });
});

/**
 * Answers `path`'s 429 with `header` as its Retry-After, or with none at all
 * when it is undefined — the shapes a server that cannot say when the door
 * reopens produces. Both reset endpoints carry the header for real (spec:
 * RateLimited), which is exactly what these screens must read.
 */
function rateLimitedWith(path: string, header?: string) {
  server.use(
    http.post(path, () =>
      HttpResponse.json(
        { error: { code: "rate_limited", message: "Too many requests." } },
        {
          status: 429,
          ...(header === undefined ? {} : { headers: { "Retry-After": header } }),
        },
      ),
    ),
  );
}

/** Fills the request form with an address and submits it. */
async function submitResetRequest(user: UserEvent) {
  await user.type(
    screen.getByLabelText(en.resetRequest.emailLabel),
    "someone@example.invalid",
  );
  await user.click(screen.getByRole("button", { name: en.resetRequest.submit }));
}

/** Fills the new-password form with a valid pair and submits it. */
async function submitNewPassword(user: UserEvent) {
  await user.type(
    screen.getByLabelText(en.resetPassword.newPasswordLabel),
    VALID_NEW_PASSWORD,
  );
  await user.type(
    screen.getByLabelText(en.resetPassword.confirmPasswordLabel),
    VALID_NEW_PASSWORD,
  );
  await user.click(screen.getByRole("button", { name: en.resetPassword.submit }));
}

/** A count-carrying sentence as i18next renders it in English. */
function counted(template: string, count: number): string {
  return template.replace("{{count, number}}", new Intl.NumberFormat("en").format(count));
}

describe("rate limiting on the reset screens", () => {
  it("states the wait the request 429 names rather than guessing at it", async () => {
    const user = userEvent.setup({ delay: null });
    await openResetRequest(user);
    rateLimitedWith("/api/v1/auth/reset-request", String(FIXTURE_RETRY_AFTER_SECONDS));

    await submitResetRequest(user);

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(
      counted(
        en.resetRequest.error.rateLimitedMinutes_other,
        Math.ceil(FIXTURE_RETRY_AFTER_SECONDS / 60),
      ),
    );
    // The undated wording is what this replaces, not what it repeats.
    expect(notice).not.toHaveTextContent(en.resetRequest.error.rateLimited);
  });

  it.each([
    ["carries no Retry-After", undefined],
    ["carries a Retry-After of zero", "0"],
    ["carries a non-numeric Retry-After", "soon"],
    ["carries an HTTP-date Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"],
    ["carries an absurd Retry-After", "999999999"],
  ])("falls back to the undated request wording when the 429 %s", async (_case, header) => {
    const user = userEvent.setup({ delay: null });
    await openResetRequest(user);
    rateLimitedWith("/api/v1/auth/reset-request", header);

    await submitResetRequest(user);

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(en.resetRequest.error.rateLimited);
    // Never a number the response did not support — and never "NaN".
    expect(notice.textContent).not.toMatch(/\d|NaN/u);
  });

  it("keeps the request screen's own way out while the wait stands", async () => {
    const user = userEvent.setup({ delay: null });
    await openResetRequest(user);
    rateLimitedWith("/api/v1/auth/reset-request", String(FIXTURE_RETRY_AFTER_SECONDS));

    await submitResetRequest(user);
    await screen.findByRole("status");

    // Submit was never disabled here and must not become so.
    expect(screen.getByRole("button", { name: en.resetRequest.submit })).toBeEnabled();
    // And the way back to sign-in is the same one it always was.
    await user.click(screen.getByRole("button", { name: en.resetRequest.backToSignIn }));
    expect(
      await screen.findByRole("heading", { name: en.login.title }),
    ).toBeInTheDocument();
  });

  it("states the wait the completion 429 names rather than guessing at it", async () => {
    const user = userEvent.setup({ delay: null });
    arriveFromResetLink(FIXTURE_RESET_TOKEN);
    render(<App />);
    await screen.findByRole("heading", { name: en.resetPassword.title });
    rateLimitedWith("/api/v1/auth/reset-complete", "45");

    await submitNewPassword(user);

    // Under a minute the notice counts seconds, exactly as the sign-in screen
    // does — the same sentence, only the number is new. Announced politely:
    // the rate-limit tone on this screen is warning, not danger.
    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(counted(en.login.error.rateLimitedSeconds_other, 45));
    expect(notice).not.toHaveTextContent(en.resetPassword.error.rateLimited);
  });

  it.each([
    ["carries no Retry-After", undefined],
    ["carries a Retry-After of zero", "0"],
    ["carries a non-numeric Retry-After", "soon"],
    ["carries an HTTP-date Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"],
    ["carries an absurd Retry-After", "999999999"],
  ])("falls back to the undated reset wording when the 429 %s", async (_case, header) => {
    const user = userEvent.setup({ delay: null });
    arriveFromResetLink(FIXTURE_RESET_TOKEN);
    render(<App />);
    await screen.findByRole("heading", { name: en.resetPassword.title });
    rateLimitedWith("/api/v1/auth/reset-complete", header);

    await submitNewPassword(user);

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(en.resetPassword.error.rateLimited);
    expect(notice.textContent).not.toMatch(/\d|NaN/u);
  });

  it("lifts the reset notice by itself and completes once the wait has passed", async () => {
    const user = userEvent.setup({ delay: null });
    arriveFromResetLink(FIXTURE_RESET_TOKEN);
    render(<App />);
    await screen.findByRole("heading", { name: en.resetPassword.title });
    // One second, so the countdown runs out inside the test rather than being
    // simulated: what is asserted is that the timer really ends the state.
    rateLimitedWith("/api/v1/auth/reset-complete", "1");

    await submitNewPassword(user);

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(counted(en.login.error.rateLimitedSeconds_one, 1));

    await waitFor(
      () => {
        expect(screen.queryByRole("status")).not.toBeInTheDocument();
      },
      { timeout: 3000 },
    );

    // The screen is not inert afterwards: the server's window has slid and the
    // same form completes the reset.
    server.resetHandlers();
    await user.click(screen.getByRole("button", { name: en.resetPassword.submit }));
    await screen.findByRole("heading", { name: en.login.title });
    expect(await screen.findByText(en.resetPassword.done)).toBeInTheDocument();
  });
});
