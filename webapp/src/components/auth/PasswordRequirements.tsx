import { useId } from "react";
import { useTranslation } from "react-i18next";

import { CircleCheckIcon, CircleIcon } from "../icons";

export interface PasswordRequirement {
  /** Stable key, also used as the React key. */
  id: string;
  label: string;
  met: boolean;
}

interface PasswordRequirementsProps {
  requirements: readonly PasswordRequirement[];
}

/**
 * The instance's password rules and whether the typed password satisfies
 * them. Met/unmet is carried by the glyph shape, never by colour alone, and
 * is spelled out for assistive technology.
 */
export function PasswordRequirements({ requirements }: PasswordRequirementsProps) {
  const { t } = useTranslation();
  const titleId = useId();

  return (
    <div className="hm-requirements">
      <span className="hm-requirements__title" id={titleId}>
        {t("password.requirementsTitle")}
      </span>
      <ul className="hm-requirements__list" aria-labelledby={titleId}>
        {requirements.map((requirement) => (
          <li key={requirement.id} className="hm-requirement" data-met={requirement.met}>
            {requirement.met ? (
              <CircleCheckIcon size={17} strokeWidth={2} className="hm-requirement__icon" />
            ) : (
              <CircleIcon size={17} className="hm-requirement__icon" />
            )}
            <span>{requirement.label}</span>
            <span className="sr-only">
              {requirement.met ? t("password.requirementMet") : t("password.requirementUnmet")}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
