import type { ChangeEvent } from "react";
import { useTranslation } from "react-i18next";

import { isLanguage, SUPPORTED_LANGUAGES } from "../i18n";
import { useLanguage } from "../i18n/useLanguage";

export function LanguageSwitcher() {
  const { t } = useTranslation();
  const { language, setLanguage } = useLanguage();

  const handleChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const next = event.target.value;
    if (isLanguage(next)) {
      setLanguage(next);
    }
  };

  return (
    <label>
      {t("language.label")}
      <select value={language} onChange={handleChange}>
        {SUPPORTED_LANGUAGES.map((code) => (
          <option key={code} value={code}>
            {t(`language.${code}`)}
          </option>
        ))}
      </select>
    </label>
  );
}
