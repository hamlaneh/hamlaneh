import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import App from "./App";
import { forgetResetToken } from "./auth/resetToken";
import { RESET_TOKEN_TTL_MINUTES } from "./auth/passwordPolicy";
import en from "./locales/en/common.json";
import {
  FIXTURE_RESET_TOKEN,
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
