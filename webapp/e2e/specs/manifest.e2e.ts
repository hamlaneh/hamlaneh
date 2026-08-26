import { expect, test } from "../support/fixtures";

/**
 * The PWA baseline: the document points at a manifest, the real stack serves
 * it, it parses, and every icon it names resolves — under the application's
 * own Content-Security-Policy, which is where this can quietly fail.
 *
 * Three things this is pinning, all of which have already been got wrong once
 * somewhere:
 *
 *   1. The manifest is REACHABLE. server/internal/httpserver/webapp.go
 *      enumerates its static routes instead of serving a catch-all, so a
 *      manifest at a path nobody routed is a 404 that no build step notices.
 *   2. Its Content-Type is a JSON MIME type. Every response carries
 *      X-Content-Type-Options: nosniff, and the manifest spec refuses a
 *      manifest that is not served as JSON — so an extension missing from
 *      that server's Content-Type allowlist (.webmanifest is missing) turns
 *      into application/octet-stream and the manifest silently does not
 *      exist.
 *   3. The policy admits it. `manifest-src` is not declared, which is
 *      deliberate: CSP Level 3 falls it back to `default-src 'self'`, and the
 *      manifest is same-origin. The assertion below states that relationship
 *      rather than trusting it, so adding a narrower default-src later fails
 *      here instead of on someone's phone.
 *
 * `en` only: there is nothing locale-dependent in a manifest, and the app
 * ships one for the product rather than one per language.
 */
interface Manifest {
  name?: string;
  short_name?: string;
  start_url?: string;
  display?: string;
  icons?: { src: string; sizes: string; type: string; purpose?: string }[];
}

test("the web manifest and its icons are served under the app's own CSP", async ({ page }) => {
  const response = await page.goto("/");
  expect(response, "no response for /").not.toBeNull();

  const csp = response?.headers()["content-security-policy"] ?? "";
  expect(csp, "the document carries no CSP").toContain("default-src 'self'");
  expect(
    csp,
    "manifest-src is now declared — this test's reasoning about the default-src fallback is stale",
  ).not.toContain("manifest-src");

  const href = await page.locator('link[rel="manifest"]').getAttribute("href");
  expect(href, "the document declares no manifest").not.toBeNull();

  const served = await page.request.get(href ?? "");
  expect(served.status(), `${href ?? ""} is not served`).toBe(200);
  // A JSON MIME type, which is what the manifest spec requires and what
  // nosniff makes non-negotiable.
  expect(served.headers()["content-type"]).toContain("application/json");

  const manifest = (await served.json()) as Manifest;
  expect(manifest.name).toBeTruthy();
  expect(manifest.short_name).toBeTruthy();
  expect(manifest.start_url).toBe("/");
  expect(manifest.display).toBe("standalone");

  const icons = manifest.icons ?? [];
  expect(icons.length, "the manifest names no icons").toBeGreaterThan(0);
  // Installability wants both: something square to show, and something that
  // survives being cropped to whatever shape the launcher uses.
  expect(icons.some((icon) => icon.sizes === "512x512")).toBe(true);
  expect(icons.some((icon) => icon.purpose === "maskable")).toBe(true);

  for (const icon of icons) {
    const image = await page.request.get(icon.src);
    expect(image.status(), `${icon.src} is not served`).toBe(200);
    expect(image.headers()["content-type"]).toBe("image/png");
    expect((await image.body()).length, `${icon.src} is empty`).toBeGreaterThan(0);
  }

  // And the browser itself will load them: an <img> is governed by img-src,
  // the same directive the icons answer to, so this fails if the policy ever
  // stops admitting them.
  const loaded = await page.evaluate(
    (src) =>
      new Promise<boolean>((resolve) => {
        const image = new Image();
        image.addEventListener("load", () => {
          resolve(true);
        });
        image.addEventListener("error", () => {
          resolve(false);
        });
        image.src = src;
      }),
    icons[0]?.src ?? "",
  );
  expect(loaded, "the browser refused to load the icon").toBe(true);
});
