/**
 * Brings up the stack the whole run drives, and settles its first account.
 *
 * The bootstrapped admin arrives with must_change_password set — that is the
 * install flow, and the forced-change screen has its own spec. Here it is
 * simply completed over the API so the account can go on to create the
 * per-test users; nothing about the browser flow is short-circuited by it.
 */
import { request } from "@playwright/test";
import { randomBytes } from "node:crypto";

import { changePasswordApi, signInApi } from "./support/accounts";
import { saveStackState, startStack, type StackState } from "./support/stack";

const READY_TIMEOUT_MS = 120_000;

/** Compose reports healthy from inside; this confirms the port is reachable. */
async function waitForInstanceDocument(baseURL: string): Promise<void> {
  const context = await request.newContext({ baseURL, ignoreHTTPSErrors: true });
  try {
    const deadline = Date.now() + READY_TIMEOUT_MS;
    for (;;) {
      try {
        const response = await context.get("/api/v1/instance");
        if (response.ok()) {
          return;
        }
      } catch {
        // Caddy may still be starting its listener; retry until the deadline.
      }
      if (Date.now() > deadline) {
        throw new Error(`e2e stack: ${baseURL}/api/v1/instance never answered`);
      }
      await new Promise((resolve) => setTimeout(resolve, 1_000));
    }
  } finally {
    await context.dispose();
  }
}

export default async function globalSetup(): Promise<void> {
  const stack: StackState = await startStack();
  await waitForInstanceDocument(stack.baseURL);

  const settled = `admin-${randomBytes(16).toString("hex")}`;
  const session = await signInApi(stack.baseURL, stack.admin.username, stack.admin.password);
  try {
    await changePasswordApi(session, stack.admin.password, settled);
  } finally {
    await session.dispose();
  }

  saveStackState({ ...stack, admin: { username: stack.admin.username, password: settled } });
}
