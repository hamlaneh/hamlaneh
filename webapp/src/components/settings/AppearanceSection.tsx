import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { SavedMark } from "./SavedMark";
import { ThemePreview } from "./ThemePreview";
import { readThemePreference, setThemePreference } from "../../theme";
import type { ThemePreference } from "../../theme";

const PREFERENCES: readonly ThemePreference[] = ["light", "dark", "system"];

/**
 * "Appearance", `settings-components` §02.
 *
 * Like Language, a single choice that applies immediately — including
 * "System", which keeps following the operating system afterwards, so it still
 * changes at dusk.
 */
export function AppearanceSection() {
  const { t } = useTranslation();
  const groupName = useId();
  const [preference, setPreference] = useState<ThemePreference>(() => readThemePreference());
  const [saved, setSaved] = useState(false);

  return (
    <>
      <div className="hm-settings__heading">
        <h3 className="hm-settings__title">{t("settings.nav.appearance")}</h3>
        <p className="hm-settings__lede">{t("settings.appearance.lede")}</p>
      </div>

      <div className="hm-settings__scroll">
        <div
          className="hm-theme-choices"
          role="radiogroup"
          aria-label={t("settings.nav.appearance")}
        >
          {PREFERENCES.map((option) => (
            <label
              key={option}
              className="hm-theme-choice"
              data-selected={preference === option}
            >
              <ThemePreview preference={option} />
              <span className="hm-theme-choice__row">
                <input
                  className="hm-settings-choice__input"
                  type="radio"
                  name={groupName}
                  value={option}
                  checked={preference === option}
                  onChange={() => {
                    setThemePreference(option);
                    setPreference(option);
                    setSaved(true);
                  }}
                />
                <span className="hm-settings-choice__radio" aria-hidden="true" />
                <span className="hm-theme-choice__name">
                  {t(`settings.appearance.option.${option}`)}
                </span>
              </span>
            </label>
          ))}
        </div>
        {saved ? <SavedMark /> : null}
        <p className="hm-settings__note">{t("settings.appearance.note")}</p>
      </div>
    </>
  );
}
