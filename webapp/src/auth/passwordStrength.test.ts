import { describe, expect, it } from "vitest";

import { PASSWORD_MIN_LENGTH } from "./passwordPolicy";
import { scorePasswordStrength } from "./passwordStrength";
import type { PasswordStrengthScore } from "./passwordStrength";

/** The instance minimum the artboards are drawn against. */
const MIN = 12;

/** Seven Persian letters, no diacritics and no zero-width joiner, so the length is exactly 7. */
const PERSIAN_WORD = "گذرواژه";

interface StrengthCase {
  name: string;
  password: string;
  minimumLength: number;
  expected: PasswordStrengthScore;
}

const CASES: readonly StrengthCase[] = [
  // Empty and below the minimum: the meter's floor, whatever the password mixes.
  { name: "empty field scores the empty state", password: "", minimumLength: MIN, expected: 0 },
  { name: "a single character", password: "a", minimumLength: MIN, expected: 1 },
  {
    name: "one character below the minimum",
    password: "a".repeat(MIN - 1),
    minimumLength: MIN,
    expected: 1,
  },
  {
    name: "all four classes still floors below the minimum",
    password: "Aa1!Aa1!Aa1",
    minimumLength: MIN,
    expected: 1,
  },

  // Length axis, one class only: 0 points under minimum+2, 1 point to
  // minimum+7, 2 points from minimum+8.
  { name: "exactly the minimum", password: "a".repeat(MIN), minimumLength: MIN, expected: 1 },
  { name: "minimum + 1", password: "a".repeat(MIN + 1), minimumLength: MIN, expected: 1 },
  { name: "minimum + 2 earns the first length point", password: "a".repeat(MIN + 2), minimumLength: MIN, expected: 2 },
  { name: "minimum + 7", password: "a".repeat(MIN + 7), minimumLength: MIN, expected: 2 },
  { name: "minimum + 8 earns the second length point", password: "a".repeat(MIN + 8), minimumLength: MIN, expected: 3 },
  { name: "far past the minimum", password: "a".repeat(200), minimumLength: MIN, expected: 3 },
  { name: "very long input", password: "a".repeat(10_000), minimumLength: MIN, expected: 3 },

  // Variety axis at exactly the minimum, where length earns nothing.
  { name: "lowercase only", password: "abcdefghijkl", minimumLength: MIN, expected: 1 },
  { name: "two classes earn the first variety point", password: "abcdefghijkL", minimumLength: MIN, expected: 2 },
  // gitleaks:allow — a scorer input, not a credential; nothing authenticates with it.
  { name: "three classes earn no more than two", password: "abcdefghij1L", minimumLength: MIN, expected: 2 },
  { name: "all four classes earn the second variety point", password: "abcdefghi1L!", minimumLength: MIN, expected: 3 },

  // Each class on its own is recognised: one class, so only length scores.
  { name: "digits only", password: "1".repeat(MIN + 2), minimumLength: MIN, expected: 2 },
  { name: "uppercase only", password: "A".repeat(MIN + 2), minimumLength: MIN, expected: 2 },
  { name: "symbols only", password: "!".repeat(MIN + 2), minimumLength: MIN, expected: 2 },
  { name: "accented lowercase counts as lowercase", password: "é".repeat(MIN + 2), minimumLength: MIN, expected: 2 },

  // Both axes together.
  { name: "minimum + 2 with all four classes", password: "abcdefghijk1L!", minimumLength: MIN, expected: 4 },
  { name: "long with two classes", password: "abcdefghijklmnopqrsT", minimumLength: MIN, expected: 4 },
  {
    name: "long with all four classes tops out rather than overflowing",
    password: "abcdefghijklmnopq1L!",
    minimumLength: MIN,
    expected: 4,
  },
  { name: "a spaced passphrase", password: "correct horse battery", minimumLength: MIN, expected: 4 },

  // Non-ASCII: caseless scripts are one class, exactly as lowercase-only Latin is.
  { name: "Persian at minimum + 2", password: PERSIAN_WORD.repeat(2), minimumLength: MIN, expected: 2 },
  { name: "Persian well past the minimum", password: PERSIAN_WORD.repeat(3), minimumLength: MIN, expected: 3 },
  {
    name: "Persian with Persian-Indic digits mixes two classes",
    password: `${PERSIAN_WORD}۱۲۳۴۵۶۷`,
    minimumLength: MIN,
    expected: 3,
  },
  { name: "Persian mixed with Latin, a digit and a symbol", password: `${PERSIAN_WORD}Ab1!xy`, minimumLength: MIN, expected: 3 },
  { name: "surrogate pairs do not break scoring", password: "🔒".repeat(8), minimumLength: MIN, expected: 2 },

  // The minimum is a parameter, not a constant baked into the scale.
  { name: "same password against a lower minimum", password: "a".repeat(12), minimumLength: 8, expected: 2 },
  { name: "same password against a higher minimum", password: "a".repeat(12), minimumLength: 20, expected: 1 },
];

describe("scorePasswordStrength", () => {
  it.each(CASES)("$name", ({ password, minimumLength, expected }) => {
    expect(scorePasswordStrength(password, minimumLength)).toBe(expected);
  });

  it("reproduces the artboard: the drawn password fills three of four segments", () => {
    // login-force-password-change draws "quiet-nest-2026" with three segments
    // filled and the label "Strong"; the scale has to agree with the design.
    expect(scorePasswordStrength("quiet-nest-2026", PASSWORD_MIN_LENGTH)).toBe(3);
  });

  it("can reach every one of the four levels", () => {
    // A scale that cannot fill the drawn meter would be a design bug.
    const reachable = new Set(
      CASES.map((entry) => scorePasswordStrength(entry.password, entry.minimumLength)),
    );
    expect([...reachable].sort((a, b) => a - b)).toStrictEqual([0, 1, 2, 3, 4]);
  });
});
