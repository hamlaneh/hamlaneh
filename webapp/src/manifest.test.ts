import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

import en from "./locales/en/common.json";

/**
 * The manifest is static JSON served straight from public/, so nothing it
 * says can go through i18n or read a CSS token — every value in it is a copy
 * of a fact that lives somewhere else. These tests are what stops the copies
 * drifting: rename the app or repaint the canvas and they fail here, rather
 * than on someone's home screen months later.
 */
const manifest = JSON.parse(
  readFileSync(path.join(__dirname, "../public/brand/manifest.json"), "utf8"),
) as Record<string, unknown>;

const tokens = readFileSync(path.join(__dirname, "tokens.css"), "utf8");
const indexHtml = readFileSync(path.join(__dirname, "../index.html"), "utf8");

/** Every declared value of a token, in source order: light first, then dark. */
function tokenValues(name: string): string[] {
  const values: string[] = [];
  for (let at = tokens.indexOf(`${name}:`); at !== -1; at = tokens.indexOf(`${name}:`, at + 1)) {
    values.push(tokens.slice(at + name.length + 1, tokens.indexOf(";", at)).trim());
  }
  return values;
}

/** The light value of a token, which is the one declared on bare :root. */
function lightToken(name: string): string {
  const at = tokens.indexOf(`${name}:`);
  if (at === -1) {
    throw new Error(`${name} is not declared in tokens.css`);
  }
  const end = tokens.indexOf(";", at);
  return tokens.slice(at + name.length + 1, end).trim();
}

describe("the web manifest", () => {
  it("carries the app's own name and tagline, not a second copy of them", () => {
    expect(manifest.name).toBe(en.app.name);
    expect(manifest.short_name).toBe(en.app.name);
    expect(manifest.description).toBe(en.app.tagline);
  });

  it("is painted in the canvas token, not a hard-coded hex", () => {
    const canvas = lightToken("--color-canvas");
    expect(manifest.background_color).toBe(canvas);
    expect(manifest.theme_color).toBe(canvas);
  });

  it("paints index.html's theme-colour metas from the same tokens", () => {
    // Two more copies of the canvas colour, in markup no bundler resolves.
    // The dark one is the copy nobody will ever eyeball: it only shows on a
    // phone whose system theme is dark, in the strip above the page.
    const [light, dark] = tokenValues("--color-canvas");
    expect(dark).not.toBe(light);

    const metas = [...indexHtml.matchAll(/<meta name="theme-color" content="([^"]+)"/g)].map(
      (match) => match[1],
    );
    expect(metas).toEqual([light, dark]);
  });

  it("offers a maskable icon as well as a plain one", () => {
    const icons = manifest.icons as { purpose: string; sizes: string }[];
    // Without a maskable icon Android crops the tile to its own shape and
    // takes a bite out of the mark; without a 512 the install prompt is
    // refused outright.
    expect(icons.some((icon) => icon.purpose === "maskable")).toBe(true);
    expect(icons.some((icon) => icon.purpose === "any" && icon.sizes === "512x512")).toBe(true);
  });
});
