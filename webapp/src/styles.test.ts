import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

/**
 * Every component stylesheet has to be imported, and nothing in the type
 * system says so.
 *
 * `admin.css` was written in 1.4 and left out of index.css, so the whole
 * dashboard shipped unstyled for two phases. Nothing failed: the app builds,
 * types check, unit tests pass, and the missing rules only show up as a
 * screen that looks wrong — which nobody saw, because the route to it 404'd
 * as well. A stylesheet is imported or it does not exist, and that is a fact
 * a directory listing can check.
 */
const COMPONENTS = path.join(__dirname, "components");

function stylesheetsOnDisk(): string[] {
  return readdirSync(COMPONENTS, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .flatMap((dir) =>
      readdirSync(path.join(COMPONENTS, dir.name))
        .filter((file) => file.endsWith(".css"))
        .map((file) => `./components/${dir.name}/${file}`),
    );
}

describe("index.css", () => {
  it("imports every stylesheet under src/components", () => {
    const index = readFileSync(path.join(__dirname, "index.css"), "utf8");
    const missing = stylesheetsOnDisk().filter(
      (specifier) => !index.includes(`@import "${specifier}"`),
    );
    expect(missing).toEqual([]);
  });

  it("finds stylesheets to check in the first place", () => {
    // Without this, a rename of the components directory would empty the
    // list above and the test would pass by having nothing to say. The floor
    // is zero deliberately: this asserts the listing works, not how many
    // stylesheets the app happens to have, which is not its business.
    expect(stylesheetsOnDisk().length).toBeGreaterThan(0);
  });
});
