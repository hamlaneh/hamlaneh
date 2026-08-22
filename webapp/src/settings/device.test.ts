import { describe, expect, it } from "vitest";

import { describeDevice } from "./device";
import { groupSecret, lastActiveLabel } from "./sessionTime";

describe("describeDevice", () => {
  it.each([
    [
      "Mozilla/5.0 (X11; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0",
      { titleKey: "linuxDesktop", icon: "monitor", browser: "Firefox 141" },
    ],
    [
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0",
      { titleKey: "windowsDesktop", icon: "monitor", browser: "Edge 138" },
    ],
    [
      "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/139.0.0.0 Mobile Safari/537.36",
      { titleKey: "androidPhone", icon: "smartphone", browser: "Chrome 139" },
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1",
      { titleKey: "iphone", icon: "smartphone", browser: "Safari 17" },
    ],
  ])("names the device and browser from %s", (userAgent, expected) => {
    const device = describeDevice(userAgent);
    expect(device.titleKey).toBe(expected.titleKey);
    expect(device.icon).toBe(expected.icon);
    expect(device.browser).toBe(expected.browser);
    expect(device.runtime).toBeNull();
  });

  it("reports the desktop app as itself, with the runtime it embeds", () => {
    const device = describeDevice(
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Tauri/2.0.1",
    );

    expect(device.titleKey).toBe("desktopApp");
    expect(device.icon).toBe("appWindow");
    expect(device.browser).toBeNull();
    expect(device.runtime).toEqual({ name: "Tauri 2.0", platform: "macOS" });
  });

  it("still names something when the client sent no User-Agent at all", () => {
    // The contract allows an empty string; a row that rendered nothing would
    // just look broken.
    const device = describeDevice("");

    expect(device.titleKey).toBe("unknown");
    expect(device.icon).toBe("monitor");
    expect(device.browser).toBeNull();
  });

  it("never invents a version it did not read", () => {
    expect(describeDevice("Mozilla/5.0 (Windows NT 10.0)").browser).toBeNull();
  });
});

describe("lastActiveLabel", () => {
  const now = new Date("2026-08-21T12:00:00Z");

  it.each([
    ["2026-08-21T11:59:30Z", "now"],
    ["2026-08-21T11:48:00Z", "active"],
    ["2026-08-21T04:00:00Z", "active"],
    ["2026-08-20T04:00:00Z", "last"],
    ["2026-08-06T09:00:00Z", "last"],
  ])("phrases %s as a %s reading", (iso, kind) => {
    expect(lastActiveLabel(iso, "en", now).kind).toBe(kind);
  });

  it("treats a clock-skewed future stamp as now rather than a negative age", () => {
    expect(lastActiveLabel("2026-08-21T12:05:00Z", "en", now)).toEqual({ kind: "now" });
  });

  it("says nothing rather than something wrong about an unparseable stamp", () => {
    expect(lastActiveLabel("not-a-date", "en", now)).toEqual({ kind: "unknown" });
  });
});

describe("groupSecret", () => {
  it("renders the manual key in the groups of four the contract asks for", () => {
    expect(groupSecret("KZ4W9TQR2MHD7FJX")).toBe("KZ4W · 9TQR · 2MHD · 7FJX");
  });

  it("leaves a trailing short group alone rather than padding it", () => {
    expect(groupSecret("ABCDE")).toBe("ABCD · E");
  });
});
