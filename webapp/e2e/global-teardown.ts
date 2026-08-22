/**
 * Saves the stack's logs into the report directory, then removes everything
 * the run created — containers AND volumes, but only under the run's own
 * project name, so an instance already running on the machine is untouched.
 *
 * The logs are written unconditionally: a red end-to-end job whose only
 * output is "expected X, got Y" usually costs more time than the failure
 * itself, and the server's own log is where the answer normally is.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import { collectLogs, stopStack } from "./support/stack";

const REPORT_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../test-results",
);

export default function globalTeardown(): void {
  try {
    mkdirSync(REPORT_DIR, { recursive: true });
    writeFileSync(path.join(REPORT_DIR, "stack-logs.txt"), collectLogs(), "utf8");
  } catch (error) {
    // Never let log collection mask the real result, but never swallow it
    // silently either (CLAUDE.md: every error is handled or propagated).
    process.stderr.write(`e2e: could not collect stack logs: ${String(error)}\n`);
  }
  stopStack();
}
