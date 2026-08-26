/**
 * Renders the committed brand SVGs into the PWA icon PNGs in
 * public/brand/icons/.
 *
 *   node scripts/generate-icons.mjs
 *
 * They live under brand/ because that is the one public prefix the Go binary
 * routes (server/internal/httpserver/webapp.go enumerates its static routes
 * rather than serving a catch-all), and because they are brand marks.
 *
 * The PNGs are committed, so this is a one-off you re-run when the mark
 * changes — nothing in the build or in CI calls it. It renders with the
 * Chromium that @playwright/test already installs for the e2e suite rather
 * than adding an image library: the only thing needed here is "draw this SVG
 * at N x N", which is a browser's day job.
 *
 * Two purposes, two treatments, because they are genuinely different pictures:
 *
 *   any       the delivered `-bg` mark, screenshotted with a transparent page
 *             so the tile keeps its own rounded corners. Platforms that mask
 *             it round it themselves; platforms that do not get the art as
 *             drawn.
 *   maskable  full bleed — a maskable icon is cropped to an unknown shape, so
 *             transparency and drawn corners are both wrong. The tile colour
 *             fills the square and the mark is inset to 60%, which keeps every
 *             stroke inside the safe zone (the middle 80% circle) whatever
 *             shape the launcher cuts.
 *
 * The LIGHT mark is the one that ships. The manifest carries a single icon set
 * with no way to vary by theme, and the light tile (#F7F6F2) with the dark
 * green mark stays legible on a dark home screen, where the dark tile
 * (#111615) would disappear into it.
 */
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const webapp = join(dirname(fileURLToPath(import.meta.url)), "..");
const brand = join(webapp, "public", "brand");
const out = join(brand, "icons");

/** The tile fill in symbol-light-bg.svg, which is --color-canvas (light). */
const TILE = "#F7F6F2";
/** Fraction of the square the mark occupies on the maskable icon. */
const SAFE_SCALE = 0.6;

const ICONS = [
  { file: "icon-192.png", size: 192, source: "symbol-light-bg.svg", maskable: false },
  { file: "icon-512.png", size: 512, source: "symbol-light-bg.svg", maskable: false },
  { file: "icon-maskable-512.png", size: 512, source: "symbol-light.svg", maskable: true },
];

function page(svg, size, maskable) {
  const inset = maskable ? `${((1 - SAFE_SCALE) / 2) * 100}%` : "0";
  return `<!doctype html><meta charset="utf-8"><style>
    html,body{margin:0;padding:0;inline-size:${size}px;block-size:${size}px;
      background:${maskable ? TILE : "transparent"}}
    .mark{position:absolute;inset:${inset}}
    .mark svg{inline-size:100%;block-size:100%;display:block}
  </style><div class="mark">${svg}</div>`;
}

const browser = await chromium.launch();
try {
  await mkdir(out, { recursive: true });
  const tab = await browser.newPage();
  for (const { file, size, source, maskable } of ICONS) {
    const svg = await readFile(join(brand, source), "utf8");
    await tab.setViewportSize({ width: size, height: size });
    await tab.setContent(page(svg, size, maskable));
    const png = await tab.screenshot({ omitBackground: !maskable });
    await writeFile(join(out, file), png);
    process.stdout.write(`${file}  ${size}x${size}  ${png.length} bytes\n`);
  }
} finally {
  await browser.close();
}
