import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import App from "./App";
import en from "./locales/en/common.json";
import {
  FIXTURE_TOTP_CODE,
  FIXTURE_TWOSTEP_CREDENTIALS,
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

/** The six drawn cells, in visual order — the row is always dir="ltr". */
function codeCells(): HTMLElement[] {
  return within(screen.getByRole("group", { name: en.totp.codeLabel })).getAllByRole(
    "textbox",
  );
}

/** The nth cell, or a failure that names the missing one. */
function codeCell(index: number): HTMLElement {
  const cell = codeCells()[index];
  if (cell === undefined) {
    throw new Error(`the code input has no cell ${String(index)}`);
  }
  return cell;
}

/** The value of each cell, in order. */
function codeValues(): string[] {
  return codeCells().map((cell) => (cell as HTMLInputElement).value);
}

/**
 * Types a code the way a person does: into the first cell, then wherever the
 * component's own auto-advance puts the caret. `keyboard` follows focus, which
 * `type` does not — and each cell holds exactly one digit.
 */
async function enterCode(user: UserEvent, code: string) {
  await user.click(codeCell(0));
  await user.keyboard(code);
}

/** Signs in with the account whose login answers 202, landing on the code screen. */
async function reachCodeScreen(user: UserEvent) {
  render(<App />);
  await screen.findByRole("heading", { name: en.login.title });
  await user.type(
    screen.getByLabelText(en.login.identifierLabel),
    FIXTURE_TWOSTEP_CREDENTIALS.identifier,
  );
  await user.type(
    screen.getByLabelText(en.login.passwordLabel),
    FIXTURE_TWOSTEP_CREDENTIALS.password,
  );
  await user.click(screen.getByRole("button", { name: en.login.submit }));
  await screen.findByRole("heading", { name: en.totp.title });
}

describe("two-step sign-in", () => {
  it("routes a 202 login to the code screen and completes the sign-in", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);

    // A 202 is not a session: nothing about the app may have unlocked yet.
    expect(
      screen.queryByRole("navigation", { name: en.chat.sidebar.label }),
    ).not.toBeInTheDocument();

    expect(codeCells()).toHaveLength(6);
    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
  });

  it("distributes a typed code across all six cells", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);

    await enterCode(user, FIXTURE_TOTP_CODE);

    expect(codeValues()).toEqual(["1", "2", "3", "4", "5", "6"]);
    expect(FIXTURE_TOTP_CODE).toBe("123456");
  });

  it("clears the cells and keeps focus on the first without restarting the flow", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);

    await enterCode(user, "999999");
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.totp.error.invalidCode);
    // Cells clear, focus returns to the first, and the challenge survives —
    // the caller is still on the code screen, not back at the password step.
    expect(codeValues()).toEqual(["", "", "", "", "", ""]);
    expect(codeCell(0)).toHaveFocus();
    expect(screen.getByRole("heading", { name: en.totp.title })).toBeInTheDocument();

    // And a correct code still works on the same challenge.
    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));
    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
  });

  it("returns to the password step, saying why, when the challenge is gone", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    // Everything the contract calls "no live challenge" — expired, consumed,
    // revoked — arrives as one 401 with code not_authenticated.
    resetMockAuth();

    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    await screen.findByRole("heading", { name: en.login.title });
    expect(await screen.findByText(en.totp.error.challengeLost)).toBeInTheDocument();
  });
});
