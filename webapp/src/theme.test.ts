import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { initTheme, readThemePreference, setThemePreference, THEME_STORAGE_KEY } from "./theme";

/** Replaces matchMedia with a controllable stub (jsdom has no real one). */
function stubMatchMedia(prefersDark: boolean): Set<() => void> {
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: prefersDark,
    media: query,
    addEventListener: (_type: string, listener: () => void) => {
      listeners.add(listener);
    },
    removeEventListener: (_type: string, listener: () => void) => {
      listeners.delete(listener);
    },
  }));
  return listeners;
}

describe("theme", () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("follows the OS preference when no choice is stored", () => {
    stubMatchMedia(true);

    initTheme();

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(readThemePreference()).toBe("system");
  });

  it("uses the light tokens when the OS prefers light", () => {
    stubMatchMedia(false);

    initTheme();

    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("lets an explicit choice win over the OS preference", () => {
    stubMatchMedia(true);
    window.localStorage.setItem(THEME_STORAGE_KEY, "light");

    initTheme();

    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("persists an explicit choice and applies it immediately", () => {
    stubMatchMedia(false);

    setThemePreference("dark");

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
  });

  it("returns to following the OS when the choice is cleared", () => {
    stubMatchMedia(true);
    setThemePreference("light");

    setThemePreference("system");

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();
  });

  it("follows a later OS change while no choice is stored", () => {
    const listeners = stubMatchMedia(false);
    initTheme();

    stubMatchMedia(true);
    for (const listener of listeners) {
      listener();
    }

    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
