import { useTranslation } from "react-i18next";

import { ArrowLeftIcon } from "../icons";

interface StepHeaderProps {
  /** The section this flow belongs to — "Security" on every 2FA artboard. */
  backLabel: string;
  onBack: () => void;
  step: number;
  total: number;
}

/**
 * The row above a multi-step flow: the way back to the section, the step in
 * words, and the dots.
 *
 * "Step 1 of 3" is stated in words on purpose — the dots repeat it, they do
 * not carry it (settings-components, Accessibility).
 */
export function StepHeader({ backLabel, onBack, step, total }: StepHeaderProps) {
  const { t } = useTranslation();

  return (
    <div className="hm-settings-step">
      <button type="button" className="hm-settings-step__back" onClick={onBack}>
        <ArrowLeftIcon size={16} strokeWidth={1.85} className="hm-mirror-glyph" />
        {backLabel}
      </button>
      <span className="hm-settings-step__divider" aria-hidden="true" />
      <span className="hm-settings-step__label">
        {t("settings.stepOf", { step, total })}
      </span>
      <span className="hm-settings-step__dots" aria-hidden="true">
        {Array.from({ length: total }, (_, index) => (
          <span
            key={index}
            className="hm-settings-step__dot"
            data-state={index + 1 === step ? "current" : index + 1 < step ? "done" : "todo"}
          />
        ))}
      </span>
    </div>
  );
}
