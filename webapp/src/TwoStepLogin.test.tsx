import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import App from "./App";
import i18n from "./i18n";
import en from "./locales/en/common.json";
import fa from "./locales/fa/common.json";
import {
  FIXTURE_RECOVERY_CODES,
  FIXTURE_RETRY_AFTER_SECONDS,
  FIXTURE_TOTP_CODE,
  FIXTURE_TWOSTEP_CREDENTIALS,
  resetMockAuth,
} from "./mocks/handlers";
import { server } from "./mocks/node";

/** One code from the set the mock hands out at enrolment: `P4RD-1TWL`. */
const RECOVERY_CODE = FIXTURE_RECOVERY_CODES[4];

/** The same code as someone reads it off paper in a hurry. */
const RECOVERY_CODE_AS_TYPED = " p4rd1twl ";

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

/**
 * Signs in with the account whose login answers 202, landing on the code
 * screen. `copy` selects the locale the labels are read from, so the same
 * helper serves the English and the Persian cases.
 */
async function reachCodeScreen(user: UserEvent, copy: typeof en | typeof fa = en) {
  render(<App />);
  await screen.findByRole("heading", { name: copy.login.title });
  await user.type(
    screen.getByLabelText(copy.login.identifierLabel),
    FIXTURE_TWOSTEP_CREDENTIALS.identifier,
  );
  await user.type(
    screen.getByLabelText(copy.login.passwordLabel),
    FIXTURE_TWOSTEP_CREDENTIALS.password,
  );
  await user.click(screen.getByRole("button", { name: copy.login.submit }));
  await screen.findByRole("heading", { name: copy.totp.title });
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

  it("spends no attempt on a half-typed six-digit code, and says so on the cells", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    const submitted = captureSubmittedCodes();

    await enterCode(user, "123");
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.totp.error.incomplete);
    expect(submitted).toEqual([]);
    // What is typed stays put — only a rejection by the server clears it.
    expect(codeValues()).toEqual(["1", "2", "3", "", "", ""]);
    expect(codeCell(0)).toHaveFocus();
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

/**
 * Records the `code` every two-step request carries and then hands the request
 * on: a resolver that returns nothing falls through to the real mock, so the
 * flow still completes exactly as it does without the spy.
 */
function captureSubmittedCodes(): string[] {
  const codes: string[] = [];
  server.use(
    http.post("/api/v1/auth/login/totp", async ({ request }) => {
      const body = (await request.clone().json()) as { code: string };
      codes.push(body.code);
      return undefined;
    }),
  );
  return codes;
}

/** Switches the challenge screen to its recovery-code half and types `value`. */
async function enterRecoveryCode(
  user: UserEvent,
  value: string,
  copy: typeof en | typeof fa = en,
) {
  await user.click(
    screen.getByRole("button", { name: copy.totp.recovery.useRecoveryCode }),
  );
  await user.type(screen.getByLabelText(copy.totp.recovery.codeLabel), value);
}

describe("recovery codes at sign-in", () => {
  it("switches to the recovery-code field and back to the cells", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);

    await user.click(
      screen.getByRole("button", { name: en.totp.recovery.useRecoveryCode }),
    );

    // One plain field replaces the six cells, and focus follows it: whoever
    // asked for this can start typing without hunting for the input.
    const field = screen.getByLabelText(en.totp.recovery.codeLabel);
    expect(field).toHaveFocus();
    expect(
      screen.queryByRole("group", { name: en.totp.codeLabel }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(en.totp.recovery.helper)).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: en.totp.recovery.useAuthenticator }),
    );

    expect(codeCells()).toHaveLength(6);
    expect(codeCell(0)).toHaveFocus();
    expect(
      screen.queryByLabelText(en.totp.recovery.codeLabel),
    ).not.toBeInTheDocument();
  });

  it("sends a lower-case, hyphen-free code in the form the contract describes", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    const submitted = captureSubmittedCodes();

    await enterRecoveryCode(user, RECOVERY_CODE_AS_TYPED);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
    // "case-insensitive, hyphens and spaces ignored" (openapi.yaml,
    // TotpLoginRequest) — and one request, on the one endpoint both halves use.
    expect(submitted).toEqual([RECOVERY_CODE.replace("-", "")]);
  });

  it("handles a wrong recovery code exactly as it handles a wrong six-digit one", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);

    await enterRecoveryCode(user, "ZZZZ-ZZZZ");
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.totp.error.invalidRecoveryCode);
    // The same three things the cells do: clear, take focus back, and leave
    // the challenge alive rather than dropping the user at the password step.
    const field = screen.getByLabelText(en.totp.recovery.codeLabel);
    expect(field).toHaveValue("");
    expect(field).toHaveFocus();
    expect(screen.getByRole("heading", { name: en.totp.title })).toBeInTheDocument();

    await user.type(field, RECOVERY_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
  });

  it("spends no attempt on a value the contract would reject outright", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    const submitted = captureSubmittedCodes();

    // Four characters is under the contract's minimum, so the server would
    // answer 400 — and the five-attempt budget is too precious to burn on it.
    await enterRecoveryCode(user, "P4RD");
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.totp.error.recoveryIncomplete);
    expect(submitted).toEqual([]);
    expect(screen.getByLabelText(en.totp.recovery.codeLabel)).toHaveFocus();
  });

  it("leaves the authenticator path working after a detour through recovery", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    const submitted = captureSubmittedCodes();

    await enterRecoveryCode(user, RECOVERY_CODE_AS_TYPED);
    await user.click(
      screen.getByRole("button", { name: en.totp.recovery.useAuthenticator }),
    );
    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
    // The abandoned recovery code was never sent, and the six digits went
    // untouched — no normalisation is applied to the authenticator half.
    expect(submitted).toEqual([FIXTURE_TOTP_CODE]);
  });

  it("offers the same way back in, in Persian, on the mirrored page", async () => {
    const user = userEvent.setup({ delay: null });
    await i18n.changeLanguage("fa");
    await reachCodeScreen(user, fa);
    const submitted = captureSubmittedCodes();

    await enterRecoveryCode(user, RECOVERY_CODE_AS_TYPED, fa);

    expect(document.documentElement.dir).toBe("rtl");
    const field = screen.getByLabelText(fa.totp.recovery.codeLabel);
    // Persian copy, not an English fallback.
    expect(screen.getByText(fa.totp.recovery.helper)).toBeInTheDocument();
    // The value is Latin: it keeps its own direction inside the mirrored page,
    // exactly as the six-cell row does.
    expect(field).toHaveAttribute("dir", "ltr");

    await user.click(screen.getByRole("button", { name: fa.totp.submit }));

    // Signing in hands the interface to the account's own locale, and this
    // fixture's account reads English — the Persian above was this browser's
    // choice, not this person's (i18n/useLanguage.ts).
    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
    expect(submitted).toEqual([RECOVERY_CODE.replace("-", "")]);
  });
});

/**
 * Answers the two-step endpoint's 429 with `header` as its Retry-After, or
 * with none at all when it is undefined — the shapes a server that cannot say
 * when the door reopens produces.
 */
function totpRateLimitedWith(header?: string) {
  server.use(
    http.post("/api/v1/auth/login/totp", () =>
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

describe("rate limiting at the two-step step", () => {
  it("states the wait the 429 names rather than guessing at it", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    totpRateLimitedWith(String(FIXTURE_RETRY_AFTER_SECONDS));

    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    // Non-urgent by design: the rate-limit notice is announced politely.
    const notice = await screen.findByRole("status");
    const minutes = Math.ceil(FIXTURE_RETRY_AFTER_SECONDS / 60);
    expect(notice).toHaveTextContent(
      en.login.error.rateLimitedMinutes_other.replace(
        "{{count, number}}",
        new Intl.NumberFormat("en").format(minutes),
      ),
    );
    // The undated wording is what this replaces, not what it repeats.
    expect(notice).not.toHaveTextContent(en.totp.error.rateLimited);
  });

  it.each([
    ["carries no Retry-After", undefined],
    ["carries a Retry-After of zero", "0"],
    ["carries a non-numeric Retry-After", "soon"],
    ["carries an HTTP-date Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT"],
    ["carries an absurd Retry-After", "999999999"],
  ])("falls back to the undated wording when the 429 %s", async (_case, header) => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    totpRateLimitedWith(header);

    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(en.totp.error.rateLimited);
    // Never a number the response did not support — and never "NaN".
    expect(notice.textContent).not.toMatch(/\d|NaN/u);
  });

  it("lifts the notice by itself once the stated wait has passed", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    // One second, so the countdown runs out inside the test rather than being
    // simulated: what is asserted is that the timer really ends the state.
    totpRateLimitedWith("1");

    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(
      en.login.error.rateLimitedSeconds_one.replace("{{count, number}}", "1"),
    );

    await waitFor(
      () => {
        expect(screen.queryByRole("status")).not.toBeInTheDocument();
      },
      { timeout: 3000 },
    );
  });

  it("keeps the retry path the screen already had", async () => {
    const user = userEvent.setup({ delay: null });
    await reachCodeScreen(user);
    totpRateLimitedWith(String(FIXTURE_RETRY_AFTER_SECONDS));

    await enterCode(user, FIXTURE_TOTP_CODE);
    await user.click(screen.getByRole("button", { name: en.totp.submit }));
    await screen.findByRole("status");

    // Submit was never disabled here and must not become so: the stated wait
    // is an extra thing the notice can say, not a new lock on the form.
    expect(screen.getByRole("button", { name: en.totp.submit })).toBeEnabled();

    // Typing again lifts the notice, exactly as it did before the countdown.
    await enterCode(user, FIXTURE_TOTP_CODE);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    // And the challenge is still live: the server's window slides, the same
    // code goes through, and the sign-in completes.
    server.resetHandlers();
    await user.click(screen.getByRole("button", { name: en.totp.submit }));

    expect(
      await screen.findByRole("navigation", { name: en.chat.sidebar.label }),
    ).toBeInTheDocument();
  });
});
