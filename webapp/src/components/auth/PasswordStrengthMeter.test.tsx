import { render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { PASSWORD_MIN_LENGTH } from "../../auth/passwordPolicy";
import i18n from "../../i18n";
import en from "../../locales/en/common.json";
import fa from "../../locales/fa/common.json";
import { PasswordStrengthMeter } from "./PasswordStrengthMeter";

/** The meter as rendered: its level text and how many segments are filled. */
function renderMeter(password: string): { level: string; segments: HTMLElement[]; filled: number } {
  const { container } = render(
    <PasswordStrengthMeter password={password} minimumLength={PASSWORD_MIN_LENGTH} />,
  );
  const level = container.querySelector(".hm-strength__level");
  if (level === null) {
    throw new Error("the meter rendered no level label");
  }
  const segments = [...container.querySelectorAll<HTMLElement>(".hm-strength__segment")];
  return {
    level: level.textContent,
    segments,
    filled: segments.filter((segment) => segment.dataset.filled === "true").length,
  };
}

afterEach(async () => {
  await i18n.changeLanguage("en");
});

describe("PasswordStrengthMeter", () => {
  const LEVELS = [
    { name: "empty field", password: "", filled: 0, label: "" },
    { name: "weak", password: "a".repeat(PASSWORD_MIN_LENGTH), filled: 1, label: en.password.strength.weak },
    { name: "fair", password: "a".repeat(PASSWORD_MIN_LENGTH + 2), filled: 2, label: en.password.strength.fair },
    { name: "strong", password: "quiet-nest-2026", filled: 3, label: en.password.strength.strong },
    { name: "very strong", password: "abcdefghijk1L!", filled: 4, label: en.password.strength.veryStrong },
  ] as const;

  it.each(LEVELS)("draws the $name state with its label", ({ password, filled, label }) => {
    const meter = renderMeter(password);

    // The design draws four segments in every state — the meter never resizes.
    expect(meter.segments).toHaveLength(4);
    expect(meter.filled).toBe(filled);
    expect(meter.level).toBe(label);
  });

  it("announces the level politely and hides the segments from assistive technology", () => {
    const { container } = render(
      <PasswordStrengthMeter password="quiet-nest-2026" minimumLength={PASSWORD_MIN_LENGTH} />,
    );

    const row = container.querySelector(".hm-strength__row");
    expect(row).toHaveAttribute("aria-live", "polite");
    expect(row).toHaveAttribute("aria-atomic", "true");
    // The whole row is announced, so the level never arrives without its noun.
    expect(row).toHaveTextContent(en.password.strength.label);
    expect(row).toHaveTextContent(en.password.strength.strong);
    // Colour-only fill, so it must not reach the accessibility tree.
    expect(container.querySelector(".hm-strength__track")).toHaveAttribute("aria-hidden", "true");
  });

  it("renders the Persian labels in fa", async () => {
    await i18n.changeLanguage("fa");

    const weak = renderMeter("a".repeat(PASSWORD_MIN_LENGTH));
    expect(weak.level).toBe(fa.password.strength.weak);

    const strong = renderMeter("quiet-nest-2026");
    expect(strong.level).toBe(fa.password.strength.strong);
  });
});
