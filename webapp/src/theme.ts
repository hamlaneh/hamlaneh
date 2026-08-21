/**
 * Light/dark theme selection. The delivered token sheet maps every colour for
 * both themes and keys dark off `:root[data-theme="dark"]`, so all this module
 * does is decide which value that attribute carries.
 *
 * There is no theme picker in the delivered design; the stored preference is
 * the contract a later settings screen writes to. Until then the OS decides.
 */

export const THEME_STORAGE_KEY = "hamlaneh.theme";

export type Theme = "light" | "dark";
export type ThemePreference = Theme | "system";

const DARK_QUERY = "(prefers-color-scheme: dark)";

function isTheme(value: unknown): value is Theme {
  return value === "light" || value === "dark";
}

/** The explicit choice on record, or "system" when there is none. */
export function readThemePreference(): ThemePreference {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
    return isTheme(stored) ? stored : "system";
  } catch (error) {
    // Storage can be blocked (private mode, hardened browsers); the OS
    // preference still applies.
    console.warn("Could not read stored theme preference:", error);
    return "system";
  }
}

function prefersDark(): boolean {
  return window.matchMedia(DARK_QUERY).matches;
}

/** Reflects the active theme on <html> for the token sheet to pick up. */
export function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}

function applyPreference(preference: ThemePreference): void {
  applyTheme(preference === "system" ? (prefersDark() ? "dark" : "light") : preference);
}

/** Records an explicit choice and applies it immediately. */
export function setThemePreference(preference: ThemePreference): void {
  try {
    if (preference === "system") {
      window.localStorage.removeItem(THEME_STORAGE_KEY);
    } else {
      window.localStorage.setItem(THEME_STORAGE_KEY, preference);
    }
  } catch (error) {
    // Persistence is best-effort; the in-memory choice still applies.
    console.warn("Could not persist theme preference:", error);
  }
  applyPreference(preference);
}

/**
 * Applies the stored preference (or the OS one) and keeps following the OS
 * while no explicit choice exists.
 */
export function initTheme(): void {
  applyPreference(readThemePreference());

  window.matchMedia(DARK_QUERY).addEventListener("change", () => {
    if (readThemePreference() === "system") {
      applyPreference("system");
    }
  });
}
