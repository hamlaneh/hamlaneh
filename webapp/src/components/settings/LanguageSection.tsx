import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { SavedMark } from "./SavedMark";
import { SUPPORTED_LANGUAGES } from "../../i18n";
import type { Language } from "../../i18n";
import { useLanguage } from "../../i18n/useLanguage";

/** The drawn end-of-row tag: a language's writing direction, not a country. */
const DIRECTION_TAG: Record<Language, string> = { en: "LTR", fa: "RTL" };

/**
 * "Language", `settings-components` §02.
 *
 * A single choice, so it commits on selection and shows the inline Saved mark
 * rather than waiting for a Save button. Selecting flips the whole interface
 * direction; nothing typed is lost, because only `dir` and the font stack move.
 *
 * The choice is also saved to the account, in the background and centrally
 * (i18n/useLanguage.ts) — the mark deliberately does not wait for that round
 * trip, and a save that fails leaves the chosen language in place.
 */
export function LanguageSection() {
  const { t } = useTranslation();
  const { language, setLanguage } = useLanguage();
  const groupName = useId();
  const [saved, setSaved] = useState(false);

  return (
    <>
      <div className="hm-settings__heading">
        <h3 className="hm-settings__title">{t("settings.nav.language")}</h3>
        <p className="hm-settings__lede">{t("settings.language.lede")}</p>
      </div>

      <div className="hm-settings__scroll">
        <div className="hm-settings-choices" role="radiogroup" aria-label={t("language.label")}>
          {SUPPORTED_LANGUAGES.map((code) => (
            <label key={code} className="hm-settings-choice" data-selected={language === code}>
              <input
                className="hm-settings-choice__input"
                type="radio"
                name={groupName}
                value={code}
                checked={language === code}
                onChange={() => {
                  setLanguage(code);
                  setSaved(true);
                }}
              />
              <span className="hm-settings-choice__radio" aria-hidden="true" />
              <span className="hm-settings-choice__text">
                {/* Each option is labelled in its own script; the second line
                    names the direction and the typeface, as drawn. */}
                <span className="hm-settings-choice__name" lang={code}>
                  {t(`settings.language.name.${code}`)}
                </span>
                <span className="hm-settings-choice__detail">
                  {t(`settings.language.detail.${code}`)}
                </span>
              </span>
              <span className="hm-settings-choice__tag" dir="ltr">
                {DIRECTION_TAG[code]}
              </span>
            </label>
          ))}
        </div>
        {saved ? <SavedMark /> : null}
        <p className="hm-settings__note">{t("settings.language.note")}</p>
      </div>
    </>
  );
}
