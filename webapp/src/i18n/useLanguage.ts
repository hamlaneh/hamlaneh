import { useCallback } from "react";
import { useTranslation } from "react-i18next";

import { FALLBACK_LANGUAGE, isLanguage, type Language } from "./index";

interface UseLanguageResult {
  language: Language;
  setLanguage: (language: Language) => void;
}

/**
 * Exposes the active language and a switcher. Side effects of switching
 * (document `lang`/`dir` and localStorage persistence) are handled centrally
 * by the i18n module's languageChanged listener.
 */
export function useLanguage(): UseLanguageResult {
  const { i18n } = useTranslation();

  const active = i18n.resolvedLanguage ?? i18n.language;
  const language = isLanguage(active) ? active : FALLBACK_LANGUAGE;

  const setLanguage = useCallback(
    (next: Language) => {
      void i18n.changeLanguage(next);
    },
    [i18n],
  );

  return { language, setLanguage };
}
