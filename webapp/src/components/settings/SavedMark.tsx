import { useTranslation } from "react-i18next";

import { CircleCheckIcon } from "../icons";

/**
 * The inline "Saved" mark from `settings-components` §04. Single choices
 * commit on selection and show this instead of a Save button; a typed field
 * shows it after its own Save succeeds.
 *
 * role="status" so the confirmation is announced without taking focus.
 */
export function SavedMark() {
  const { t } = useTranslation();

  return (
    <span className="hm-settings-saved" role="status">
      <CircleCheckIcon size={16} strokeWidth={1.85} />
      {t("settings.saved")}
    </span>
  );
}
