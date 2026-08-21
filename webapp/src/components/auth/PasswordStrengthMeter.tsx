import { useTranslation } from "react-i18next";

import { scorePasswordStrength } from "../../auth/passwordStrength";
import type { PasswordStrengthScore } from "../../auth/passwordStrength";

/** Segment positions, in fill order — four, as drawn. */
const SEGMENTS = [1, 2, 3, 4] as const;

/**
 * The four level labels. Only the third is drawn ("Strong", beside the
 * three-of-four state on login-force-password-change); the others are
 * implementation's to name (docs/design/LOGIN_HANDOFF.md, open question 2).
 */
const LEVEL_KEY = {
  1: "password.strength.weak",
  2: "password.strength.fair",
  3: "password.strength.strong",
  4: "password.strength.veryStrong",
} as const;

interface PasswordStrengthMeterProps {
  password: string;
  /** Instance minimum, so the scale moves with the policy instead of guessing it. */
  minimumLength: number;
}

/**
 * Four segments and a text label, per the login-force-password-change
 * artboard. Advisory only: it never gates submission, and its copy says how
 * strong a password looks, never that it is secure.
 *
 * The label carries the whole signal for assistive technology — the segments
 * differ by colour alone, so they are hidden from it (the handoff makes the
 * label the accessible signal). The row is a polite atomic live region, which
 * announces "Strength: Fair" as one phrase; because the text is identical
 * between keystrokes inside a level, typing on does not re-announce it.
 */
export function PasswordStrengthMeter({
  password,
  minimumLength,
}: PasswordStrengthMeterProps) {
  const { t } = useTranslation();
  const score: PasswordStrengthScore = scorePasswordStrength(password, minimumLength);

  return (
    <div className="hm-strength">
      <div className="hm-strength__row" aria-live="polite" aria-atomic="true">
        <span className="hm-strength__title">{t("password.strength.label")}</span>
        {/* Empty field: no level is claimed, but the row and the track stay
            put — the design holds the same geometry in every state. */}
        <span className="hm-strength__level">{score === 0 ? "" : t(LEVEL_KEY[score])}</span>
      </div>
      <div className="hm-strength__track" aria-hidden="true">
        {SEGMENTS.map((segment) => (
          <span
            key={segment}
            className="hm-strength__segment"
            data-filled={segment <= score}
          />
        ))}
      </div>
    </div>
  );
}
