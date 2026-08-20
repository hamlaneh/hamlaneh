#!/usr/bin/env node
/**
 * Locale key-parity check (CI gate).
 *
 * `en` is the source of truth. Every JSON file under src/locales/en must have
 * a counterpart under src/locales/fa with exactly the same (flattened) key
 * set — no missing keys, no extra keys, no missing or extra files.
 *
 * Exits 1 and prints every divergence; exits 0 when locales are in parity.
 */

import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const SOURCE_LOCALE = "en";
const TARGET_LOCALE = "fa";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const localesDir = path.join(scriptDir, "..", "src", "locales");

/** Recursively lists JSON files under dir, as paths relative to dir. */
function listJsonFiles(dir, prefix = "") {
  const entries = readdirSync(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relative = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
    if (entry.isDirectory()) {
      files.push(...listJsonFiles(path.join(dir, entry.name), relative));
    } else if (entry.isFile() && entry.name.endsWith(".json")) {
      files.push(relative);
    }
  }
  return files.sort();
}

/** Flattens nested objects into dot-separated key paths. */
function flattenKeys(value, prefix = "") {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value)
  ) {
    return [prefix];
  }
  const keys = [];
  for (const [key, child] of Object.entries(value)) {
    const keyPath = prefix === "" ? key : `${prefix}.${key}`;
    keys.push(...flattenKeys(child, keyPath));
  }
  return keys;
}

function loadKeys(locale, relativeFile) {
  const filePath = path.join(localesDir, locale, relativeFile);
  const parsed = JSON.parse(readFileSync(filePath, "utf8"));
  return new Set(flattenKeys(parsed));
}

const problems = [];

const sourceFiles = listJsonFiles(path.join(localesDir, SOURCE_LOCALE));
const targetFiles = listJsonFiles(path.join(localesDir, TARGET_LOCALE));

if (sourceFiles.length === 0) {
  problems.push(`no JSON files found under src/locales/${SOURCE_LOCALE}`);
}

for (const file of sourceFiles) {
  if (!targetFiles.includes(file)) {
    problems.push(`missing file in ${TARGET_LOCALE}: ${file}`);
  }
}
for (const file of targetFiles) {
  if (!sourceFiles.includes(file)) {
    problems.push(`extra file in ${TARGET_LOCALE} (not in ${SOURCE_LOCALE}): ${file}`);
  }
}

for (const file of sourceFiles.filter((f) => targetFiles.includes(f))) {
  let sourceKeys;
  let targetKeys;
  try {
    sourceKeys = loadKeys(SOURCE_LOCALE, file);
    targetKeys = loadKeys(TARGET_LOCALE, file);
  } catch (error) {
    problems.push(`failed to parse ${file}: ${String(error)}`);
    continue;
  }

  for (const key of [...sourceKeys].sort()) {
    if (!targetKeys.has(key)) {
      problems.push(`${file}: key missing in ${TARGET_LOCALE}: ${key}`);
    }
  }
  for (const key of [...targetKeys].sort()) {
    if (!sourceKeys.has(key)) {
      problems.push(
        `${file}: extra key in ${TARGET_LOCALE} (not in ${SOURCE_LOCALE}): ${key}`,
      );
    }
  }
}

if (problems.length > 0) {
  console.error(
    `Locale parity check FAILED (${String(problems.length)} problem(s)):`,
  );
  for (const problem of problems) {
    console.error(`  - ${problem}`);
  }
  process.exit(1);
}

console.log(
  `Locale parity check passed: ${String(sourceFiles.length)} file(s), ` +
    `${SOURCE_LOCALE} and ${TARGET_LOCALE} key sets match.`,
);
