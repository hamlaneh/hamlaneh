/**
 * Device and browser labels for the session list.
 *
 * The contract hands the client the raw `user_agent` and says the client
 * derives the labels ("the client derives the device and browser labels from
 * it"), which is what the `settings-sessions` artboard draws: a device name on
 * the title row and a browser name in the meta line.
 *
 * Two properties matter more than breadth of coverage. It never guesses a
 * version it did not read, and it always produces something to render — the
 * header can legitimately be an empty string (a client that sent none), and a
 * row with no label at all would just look broken.
 */

/** Which of the three drawn glyphs the row uses. */
export type DeviceIcon = "monitor" | "smartphone" | "appWindow";

/** Key suffix under `settings.sessions.device.*` for the row's title. */
export type DeviceTitleKey =
  | "linuxDesktop"
  | "windowsDesktop"
  | "macDesktop"
  | "androidPhone"
  | "iphone"
  | "ipad"
  | "desktopApp"
  | "unknown";

export interface DeviceDescription {
  icon: DeviceIcon;
  titleKey: DeviceTitleKey;
  /**
   * Proper-noun detail for the meta line — "Firefox 141". Null when the agent
   * carried nothing recognisable, in which case the row simply omits it.
   */
  browser: string | null;
  /**
   * The desktop app reports its own runtime instead of a browser: the
   * artboard's "Tauri 2.0 on macOS".
   */
  runtime: { name: string; platform: string } | null;
}

interface PlatformMatch {
  pattern: RegExp;
  titleKey: DeviceTitleKey;
  icon: DeviceIcon;
  /** Proper noun used when composing the desktop app's runtime line. */
  name: string;
}

/** Order matters: iPad announces itself before the generic Mac tokens. */
const PLATFORMS: readonly PlatformMatch[] = [
  { pattern: /Android/u, titleKey: "androidPhone", icon: "smartphone", name: "Android" },
  { pattern: /iPhone/u, titleKey: "iphone", icon: "smartphone", name: "iOS" },
  { pattern: /iPad/u, titleKey: "ipad", icon: "smartphone", name: "iPadOS" },
  { pattern: /Windows/u, titleKey: "windowsDesktop", icon: "monitor", name: "Windows" },
  { pattern: /Mac OS X|Macintosh/u, titleKey: "macDesktop", icon: "monitor", name: "macOS" },
  { pattern: /Linux|X11|CrOS/u, titleKey: "linuxDesktop", icon: "monitor", name: "Linux" },
];

interface BrowserMatch {
  pattern: RegExp;
  name: string;
}

/**
 * Order matters: Chromium-family agents also carry `Chrome/`, and Safari's
 * marketing version lives in `Version/`, not in the `Safari/` build number.
 */
const BROWSERS: readonly BrowserMatch[] = [
  { pattern: /Edg(?:[A-Z]+)?\/(\d+)/u, name: "Edge" },
  { pattern: /OPR\/(\d+)/u, name: "Opera" },
  { pattern: /Firefox\/(\d+)/u, name: "Firefox" },
  { pattern: /Chrome\/(\d+)/u, name: "Chrome" },
  // Mobile Safari squeezes a build token between the two, so allow one.
  { pattern: /Version\/(\d+)[.\d]* (?:Mobile\/\S+ )?Safari\//u, name: "Safari" },
];

function platformOf(userAgent: string): PlatformMatch | null {
  return PLATFORMS.find((candidate) => candidate.pattern.test(userAgent)) ?? null;
}

function browserOf(userAgent: string): string | null {
  for (const candidate of BROWSERS) {
    const major = candidate.pattern.exec(userAgent)?.[1];
    if (major !== undefined) {
      return `${candidate.name} ${major}`;
    }
  }
  return null;
}

/** Everything a session row needs to describe a device, derived client-side. */
export function describeDevice(userAgent: string): DeviceDescription {
  const platform = platformOf(userAgent);

  // The desktop app is the app, whatever webview it happens to embed.
  const tauri = /Tauri\/(\d+)\.(\d+)/u.exec(userAgent);
  if (tauri !== null) {
    return {
      icon: "appWindow",
      titleKey: "desktopApp",
      browser: null,
      runtime: {
        name: `Tauri ${tauri[1] ?? ""}.${tauri[2] ?? ""}`,
        platform: platform?.name ?? "",
      },
    };
  }

  return {
    icon: platform?.icon ?? "monitor",
    titleKey: platform?.titleKey ?? "unknown",
    browser: browserOf(userAgent),
    runtime: null,
  };
}
