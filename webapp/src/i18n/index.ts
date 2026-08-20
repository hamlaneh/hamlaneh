import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import en from "../locales/en/common.json";
import fa from "../locales/fa/common.json";

export const SUPPORTED_LANGUAGES = ["en", "fa"] as const;
export type Language = (typeof SUPPORTED_LANGUAGES)[number];

export const FALLBACK_LANGUAGE: Language = "en";
export const LANGUAGE_STORAGE_KEY = "hamlaneh.language";

export function isLanguage(value: unknown): value is Language {
  return (SUPPORTED_LANGUAGES as readonly unknown[]).includes(value);
}

function readStoredLanguage(): Language {
  try {
    const stored = window.localStorage.getItem(LANGUAGE_STORAGE_KEY);
    return isLanguage(stored) ? stored : FALLBACK_LANGUAGE;
  } catch (error) {
    // localStorage can be unavailable (e.g. blocked storage); fall back to the default.
    console.warn("Could not read stored language preference:", error);
    return FALLBACK_LANGUAGE;
  }
}

function persistLanguage(language: Language): void {
  try {
    window.localStorage.setItem(LANGUAGE_STORAGE_KEY, language);
  } catch (error) {
    // Persistence is best-effort; the in-memory language still applies.
    console.warn("Could not persist language preference:", error);
  }
}

/** Reflects the active language on <html>: `lang` plus `dir` (rtl for Persian). */
export function applyDocumentLanguage(language: Language): void {
  document.documentElement.lang = language;
  document.documentElement.dir = language === "fa" ? "rtl" : "ltr";
}

void i18n.use(initReactI18next).init({
  resources: {
    en: { common: en },
    fa: { common: fa },
  },
  lng: readStoredLanguage(),
  fallbackLng: FALLBACK_LANGUAGE,
  defaultNS: "common",
  interpolation: {
    // React already escapes rendered values.
    escapeValue: false,
  },
});

i18n.on("languageChanged", (language) => {
  if (isLanguage(language)) {
    applyDocumentLanguage(language);
    persistLanguage(language);
  }
});

applyDocumentLanguage(isLanguage(i18n.language) ? i18n.language : FALLBACK_LANGUAGE);

export default i18n;
