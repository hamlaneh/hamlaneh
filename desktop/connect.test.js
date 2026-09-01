/**
 * The connect page's two pieces of logic, under `node --test` — the runner
 * Node already ships, so the shell needs no test framework of its own.
 *
 *   node --test desktop/
 *
 * `instanceOrigin` is the interesting one: its return value is handed to
 * `location.assign` inside the shell's own privileged origin, so every case
 * it must refuse is a case a typed address could otherwise exploit.
 */
import assert from "node:assert/strict";
import test from "node:test";

import { instanceOrigin, pickLocale, STRINGS } from "./ui/connect.js";

test("an address it accepts becomes a bare origin", () => {
  const accepted = [
    ["https://chat.example.com", "https://chat.example.com"],
    ["http://localhost:8080", "http://localhost:8080"],
    // A bare host is what people type; https is assumed, not demanded.
    ["chat.example.com", "https://chat.example.com"],
    ["  chat.example.com  ", "https://chat.example.com"],
    // Only the origin survives: the web application addresses its API from
    // window.location.origin, so a path would be discarded regardless.
    ["https://chat.example.com/login", "https://chat.example.com"],
    ["https://chat.example.com/", "https://chat.example.com"],
    // A non-default port is part of the origin and has to stay.
    ["https://chat.example.com:8443/x", "https://chat.example.com:8443"],
    ["http://127.0.0.1:8080", "http://127.0.0.1:8080"],
  ];
  for (const [raw, expected] of accepted) {
    assert.equal(instanceOrigin(raw), expected, raw);
  }
});

test("it refuses every scheme that is not http or https", () => {
  // Each of these parses as a URL. Rejection is on the protocol, which is why
  // prefixing an unschemed string with https:// cannot be what filters them.
  const refused = [
    "javascript:alert(1)",
    "JavaScript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "file:///etc/passwd",
    "tauri://localhost",
    "vbscript:msgbox(1)",
    "ws://chat.example.com",
  ];
  for (const raw of refused) {
    assert.equal(instanceOrigin(raw), null, raw);
  }
});

test("it refuses text that names no host", () => {
  for (const raw of ["", "   ", "https://", "http://", "  ///  ", "https://:8080"]) {
    assert.equal(instanceOrigin(raw), null, JSON.stringify(raw));
  }
  // Not in that list on purpose: `https:///path` parses, per WHATWG, as the
  // host `path`. It is accepted and fails later at DNS, which is the right
  // outcome — refusing it would mean second-guessing the URL parser.
  assert.equal(instanceOrigin("https:///path"), "https://path");
});

test("Persian is chosen only for a Persian tag, and English is the fallback", () => {
  assert.equal(pickLocale(["fa"]), "fa");
  assert.equal(pickLocale(["fa-IR", "en-US"]), "fa");
  assert.equal(pickLocale(["en-GB", "fa"]), "fa");
  assert.equal(pickLocale(["en-US"]), "en");
  assert.equal(pickLocale(["de", "fr"]), "en");
  assert.equal(pickLocale([]), "en");
});

test("en and fa carry the same keys, and none is blank", () => {
  // The shell has no bundler, so `npm run i18n:check` cannot see these
  // strings. This is that check, for this page.
  assert.deepEqual(Object.keys(STRINGS.fa).sort(), Object.keys(STRINGS.en).sort());
  for (const [locale, strings] of Object.entries(STRINGS)) {
    for (const [key, value] of Object.entries(strings)) {
      assert.equal(typeof value, "string", `${locale}.${key}`);
      assert.notEqual(value.trim(), "", `${locale}.${key}`);
    }
  }
});

test("every string the markup asks for exists", async () => {
  const { readFile } = await import("node:fs/promises");
  const html = await readFile(new URL("./ui/index.html", import.meta.url), "utf8");
  const asked = [...html.matchAll(/data-string="([^"]+)"/gu)].map((match) => match[1]);
  assert.ok(asked.length > 0, "the markup asks for no strings at all");
  for (const key of asked) {
    assert.ok(key in STRINGS.en, `index.html asks for an unknown string: ${key}`);
  }
});
