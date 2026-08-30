import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import i18n from "../../../i18n";
import en from "../../../locales/en/common.json";
import fa from "../../../locales/fa/common.json";
import type { ChangedMember } from "../../../mls/types";
import { VerificationSheet, VerificationWarning } from "./Verification";

/**
 * The two undesigned verification surfaces. No artboard exists yet, so these
 * assert the plumbing and the handful of things ADR 008 makes non-negotiable
 * whatever the eventual design does: two exits and no third, `pinned` and
 * `verified` reading differently, and ASCII digits in both locales.
 */

const NUMBER = "02223 10856 23195 68191 98981 81738 77344 52451 44950 97148 05931 27891";

afterEach(async () => {
  await i18n.changeLanguage("en");
});

function renderSheet(overrides: Partial<Parameters<typeof VerificationSheet>[0]> = {}) {
  const props = {
    userId: "nasrin",
    name: "Nasrin",
    level: null,
    safetyNumberFor: () => Promise.resolve(NUMBER),
    onVerify: vi.fn(),
    onAccept: vi.fn(),
    onClose: vi.fn(),
    ...overrides,
  };
  render(<VerificationSheet {...props} />);
  return props;
}

const CHANGED: ChangedMember[] = [
  { userId: "nasrin", kind: "newDevice", added: ["AQID"], removed: [] },
];

describe("the verification sheet", () => {
  it("shows the number as twelve five-digit groups", async () => {
    renderSheet();
    const number = await screen.findByLabelText(en.chat.e2ee.verification.numberLabel);
    // The isolate characters are invisible direction marks, not content.
    expect(number.textContent.replace(/[⁦-⁩]/g, "")).toBe(NUMBER);
    // Direction pinned, so the groups do not reorder inside an RTL column.
    expect(number).toHaveAttribute("dir", "ltr");
  });

  it("keeps the digits ASCII in Persian", async () => {
    await i18n.changeLanguage("fa");
    renderSheet();

    const number = await screen.findByLabelText(fa.chat.e2ee.verification.numberLabel);
    // A number read aloud in Persian has to be the string the other person is
    // looking at, so it never goes near Intl's `arabext` digits.
    expect(number.textContent.replace(/[⁦-⁩]/g, "")).toBe(NUMBER);
    expect(number.textContent).not.toMatch(/[۰-۹]/);
  });

  it("does not let a pin read like a comparison", () => {
    renderSheet({ level: "pinned" });
    expect(screen.getByText(en.chat.e2ee.verification.level.pinned)).toBeInTheDocument();
    // The distinction is a security property, not a shade of wording: these
    // two strings must never converge.
    expect(en.chat.e2ee.verification.level.pinned).not.toBe(
      en.chat.e2ee.verification.level.verified,
    );
  });

  it("offers the ceremony's verdict only once a number is on screen", async () => {
    renderSheet({ safetyNumberFor: () => Promise.resolve(null) });

    expect(await screen.findByText(en.chat.e2ee.verification.numberUnavailable)).toBeInTheDocument();
    // Nothing to have compared, so the "it matched" button cannot be pressed.
    expect(screen.getByRole("button", { name: en.chat.e2ee.verification.match })).toBeDisabled();
  });

  it("records a match as verified and an unceremonied accept separately", async () => {
    const user = userEvent.setup({ delay: null });
    const props = renderSheet();
    await screen.findByLabelText(en.chat.e2ee.verification.numberLabel);

    await user.click(screen.getByRole("button", { name: en.chat.e2ee.verification.match }));
    expect(props.onVerify).toHaveBeenCalledWith("nasrin");
    expect(props.onAccept).not.toHaveBeenCalled();
  });
});

describe("the changed-key warning", () => {
  function renderWarning(overrides: Partial<Parameters<typeof VerificationWarning>[0]> = {}) {
    const props = {
      changed: CHANGED,
      uncoveredLeaves: 0,
      own: null,
      resolveName: (userId: string) => (userId === "nasrin" ? "Nasrin" : null),
      onCompare: vi.fn(),
      onAccept: vi.fn(),
      onAcceptOwn: vi.fn(),
      ...overrides,
    };
    render(<VerificationWarning {...props} />);
    return props;
  }

  it("says who, and which of the two changes it was", () => {
    renderWarning();
    expect(screen.getByText(/Nasrin has a device/)).toBeInTheDocument();
  });

  it("distinguishes a replaced key from a new device", () => {
    renderWarning({
      changed: [{ userId: "nasrin", kind: "replacedKey", added: ["AQID"], removed: ["BAU="] }],
    });
    expect(screen.getByText(/has been replaced/)).toBeInTheDocument();
  });

  it("offers exactly two exits and no third", async () => {
    const user = userEvent.setup({ delay: null });
    const props = renderWarning();

    const buttons = screen.getAllByRole("button").map((button) => button.textContent);
    expect(buttons).toEqual([
      en.chat.e2ee.verification.compare,
      en.chat.e2ee.verification.accept,
    ]);

    await user.click(screen.getByRole("button", { name: en.chat.e2ee.verification.accept }));
    expect(props.onAccept).toHaveBeenCalledWith("nasrin");
  });

  it("explains the unattributed-device case rather than offering a button for it", () => {
    renderWarning({ changed: [], uncoveredLeaves: 1 });
    expect(screen.getByText(en.chat.e2ee.verification.uncovered)).toBeInTheDocument();
    expect(screen.queryAllByRole("button")).toHaveLength(0);
  });

  it("says that reading carries on", () => {
    renderWarning();
    expect(screen.getByText(en.chat.e2ee.verification.readingContinues)).toBeInTheDocument();
  });

  it("puts the reader's own account first and takes an explicit yes", async () => {
    const user = userEvent.setup({ delay: null });
    const props = renderWarning({ own: { keys: ["AQID"] }, changed: [] });

    expect(screen.getByText(en.chat.e2ee.verification.ownTitle)).toBeInTheDocument();
    expect(screen.getByText(en.chat.e2ee.verification.ownIfNotYours)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: en.chat.e2ee.verification.ownAccept }));
    expect(props.onAcceptOwn).toHaveBeenCalledTimes(1);
  });
});
