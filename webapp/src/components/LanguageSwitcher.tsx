import { useId } from "react";
import { useTranslation } from "react-i18next";

import { SUPPORTED_LANGUAGES } from "../i18n";
import type { Language } from "../i18n";
import { useLanguage } from "../i18n/useLanguage";

/** Each option is labelled in its own script — no flags, no country names. */
const OPTION_LABEL_KEY: Record<Language, "language.shortEn" | "language.shortFa"> = {
  en: "language.shortEn",
  fa: "language.shortFa",
};

/**
 * Two-option segmented switcher, available before sign-in. Native radios
 * carry selection, arrow-key navigation and the tab order; the sibling span
 * carries the drawn appearance. Selection reads from fill *and* weight, so it
 * never rests on colour alone.
 */
export function LanguageSwitcher() {
  const { t } = useTranslation();
  const { language, setLanguage } = useLanguage();
  const groupName = useId();

  return (
    <div className="hm-language" role="radiogroup" aria-label={t("language.label")}>
      {SUPPORTED_LANGUAGES.map((code) => (
        <label key={code}>
          <input
            className="hm-language__input"
            type="radio"
            name={groupName}
            value={code}
            checked={language === code}
            onChange={() => {
              setLanguage(code);
            }}
          />
          <span className={`hm-language__option hm-language__option--${code}`}>
            {t(OPTION_LABEL_KEY[code])}
          </span>
        </label>
      ))}
    </div>
  );
}
