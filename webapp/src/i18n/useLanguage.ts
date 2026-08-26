import { useCallback, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

import i18n, { FALLBACK_LANGUAGE, isLanguage, type Language } from "./index";
import { api } from "../api/client";

interface UseLanguageResult {
  language: Language;
  setLanguage: (language: Language) => void;
}

/**
 * Exposes the active language and a switcher. Side effects of switching
 * (document `lang`/`dir` and localStorage persistence) are handled centrally
 * by the i18n module's languageChanged listener, and saving the choice to the
 * signed-in account is handled centrally by useAccountLanguage below — so
 * every switcher, before or after sign-in, behaves the same without knowing
 * anything about sessions.
 */
export function useLanguage(): UseLanguageResult {
  const { i18n: instance } = useTranslation();

  const active = instance.resolvedLanguage ?? instance.language;
  const language = isLanguage(active) ? active : FALLBACK_LANGUAGE;

  const setLanguage = useCallback(
    (next: Language) => {
      void instance.changeLanguage(next);
    },
    [instance],
  );

  return { language, setLanguage };
}

/**
 * Binds the interface language to the signed-in account: applies the
 * account's locale when a session appears, and saves later switches back to
 * it in the background. Call it once, from the session root, with the
 * account's locale — or null while nobody is signed in.
 *
 * # What wins when localStorage and the account disagree
 *
 * The account, from the moment we know whose it is.
 *
 * localStorage is a fact about a browser, and a browser is shared and
 * outlives a session: the value there may be the last person's choice on a
 * family machine, or this person's own choice from before they changed it on
 * their phone. The account is the fact about the *person*, so it is what the
 * interface follows once a person is identified. The cost is real and
 * deliberate — someone who picks Persian on the sign-in screen and then signs
 * in to an account that still says English lands in English — and the fix is
 * the one that lasts: change it once in Settings and it follows them
 * everywhere, instead of being re-chosen on every device forever.
 *
 * localStorage keeps its one job: it is the answer *before* sign-in, where
 * there is no account to ask. It is still written on every change (see
 * i18n/index.ts), so the sign-in screen opens in the language this browser
 * last used.
 */
export function useAccountLanguage(accountLocale: Language | null): void {
  // The locale the account holds as far as this session knows — moved when a
  // save is *sent*, not when it lands, and rolled back if it fails.
  //
  // Tracking the last confirmed value instead is subtly wrong and does not
  // heal: switch to Persian, change your mind before the first PATCH lands,
  // and the second switch compares against a stale "en", decides there is
  // nothing to save, and sends nothing. The first response then arrives and
  // records Persian. The interface is English, the account is Persian, and
  // every later attempt to pick English is suppressed the same way.
  const savedLocale = useRef(accountLocale);

  useEffect(() => {
    savedLocale.current = accountLocale;
    if (accountLocale === null) {
      return undefined;
    }
    // Sign-in: the account outranks whatever this browser remembered.
    if (i18n.language !== accountLocale) {
      void i18n.changeLanguage(accountLocale);
    }

    const persist = (next: string) => {
      if (!isLanguage(next) || next === savedLocale.current) {
        return;
      }
      const previous = savedLocale.current;
      savedLocale.current = next;

      // Deliberately unawaited: the interface has already switched, and a
      // round trip must never be what a language change waits for. A failure
      // is logged and rolled back, so the next switch retries — reverting the
      // interface under someone who just chose is worse than an account that
      // learns the change a moment later.
      void api
        .PATCH("/api/v1/users/me", { body: { locale: next } })
        .then(({ data, error }) => {
          if (data === undefined) {
            savedLocale.current = previous;
            console.warn("Could not save the language preference:", error);
            return;
          }
          savedLocale.current = data.locale;
        })
        .catch((requestError: unknown) => {
          savedLocale.current = previous;
          console.warn("Could not save the language preference:", requestError);
        });
    };

    i18n.on("languageChanged", persist);
    return () => {
      i18n.off("languageChanged", persist);
    };
  }, [accountLocale]);
}
