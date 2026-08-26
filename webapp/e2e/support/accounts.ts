/**
 * Test accounts, created through the application's own API.
 *
 * Every test gets its OWN account with a random username. That is what makes
 * the suite safe to run in parallel: no test can observe or disturb another
 * test's password, sessions or two-step state, and nothing depends on the
 * order specs happen to run in.
 *
 * The chain starts where a real install starts — the first admin comes from
 * the bootstrap environment variables (server/internal/bootstrap), and every
 * later account is created by that admin through POST /api/v1/admin/users.
 * No test-only seeding path exists, so the suite cannot drift from what an
 * operator's instance actually does.
 */
import { randomBytes } from "node:crypto";

import type { APIRequestContext, APIResponse } from "@playwright/test";
import { request as playwrightRequest } from "@playwright/test";

import { totpCode } from "./totp";

const CSRF_COOKIE = "hamlaneh_csrf";
const CSRF_HEADER = "X-Hamlaneh-CSRF";

/** A signed-in API session, carrying the CSRF token its mutations need. */
export interface ApiSession {
  context: APIRequestContext;
  dispose: () => Promise<void>;
}

export interface TestAccount {
  /**
   * The server's id for this account. Taken from the creation response rather
   * than looked up later: it is the value a mention token carries and the one
   * an invite names, so a spec that needs it must not have to search for it.
   */
  id: string;
  username: string;
  password: string;
  email: string;
  displayName: string;
}

function token(bytes = 18): string {
  return randomBytes(bytes).toString("hex");
}

/** Matches the contract's `^[a-z0-9][a-z0-9_.-]*$`, 3–32 characters. */
function uniqueUsername(prefix: string): string {
  return `${prefix}-${token(6)}`.slice(0, 32);
}

export async function expectOk(response: APIResponse, what: string): Promise<APIResponse> {
  if (!response.ok()) {
    throw new Error(`${what}: ${String(response.status())} ${await response.text()}`);
  }
  return response;
}

async function csrfToken(context: APIRequestContext): Promise<string> {
  const state = await context.storageState();
  const cookie = state.cookies.find((candidate) => candidate.name === CSRF_COOKIE);
  if (cookie === undefined) {
    throw new Error("no CSRF cookie on the API context — is it signed in?");
  }
  return cookie.value;
}

/**
 * Signs in over the API and returns the session.
 *
 * `userAgent` is not cosmetic: the sessions screen names a device from it, so
 * a seeded second session can be given an identity the spec can point at
 * ("iPhone") instead of guessing at a row position.
 */
export async function signInApi(
  baseURL: string,
  username: string,
  password: string,
  options: { userAgent?: string } = {},
): Promise<ApiSession> {
  const context = await playwrightRequest.newContext({
    baseURL,
    ignoreHTTPSErrors: true,
    ...(options.userAgent === undefined ? {} : { extraHTTPHeaders: { "User-Agent": options.userAgent } }),
  });
  await expectOk(
    await context.post("/api/v1/auth/login", { data: { identifier: username, password } }),
    `API sign-in for ${username}`,
  );
  return { context, dispose: () => context.dispose() };
}

/**
 * A mutating request on a signed-in session, carrying the double-submit CSRF
 * header the server requires. Exported because conversation setup (chat.ts)
 * needs exactly the same thing: there is no test-only write path, so every
 * fixture goes through the API a browser would.
 */
export async function post(session: ApiSession, url: string, data?: unknown): Promise<APIResponse> {
  return session.context.post(url, {
    headers: { [CSRF_HEADER]: await csrfToken(session.context) },
    ...(data === undefined ? {} : { data }),
  });
}

/**
 * The multipart half of `post`, for the one endpoint that takes a file
 * (openapi.yaml -> uploadFile). Same CSRF rule; only the encoding differs.
 */
export async function postFile(
  session: ApiSession,
  url: string,
  file: { name: string; mimeType: string; buffer: Buffer },
): Promise<APIResponse> {
  return session.context.post(url, {
    headers: { [CSRF_HEADER]: await csrfToken(session.context) },
    multipart: { file },
  });
}

/** Ends the session's whole family server-side, not just the local cookies. */
export async function logoutApi(session: ApiSession): Promise<void> {
  await expectOk(await post(session, "/api/v1/auth/logout"), "API sign-out");
}

/** Replaces the password of a signed-in account and clears must_change_password. */
export async function changePasswordApi(
  session: ApiSession,
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  await expectOk(
    await post(session, "/api/v1/auth/change-password", {
      current_password: currentPassword,
      new_password: newPassword,
    }),
    "API password change",
  );
}

/**
 * Enrols two-step verification the way the settings panel does — setup,
 * verify with a real code, activate — but over the API.
 *
 * The suite covers the LOGIN challenge (a password alone must not sign the
 * account in), which is a different screen from enrolment; driving three
 * enrolment steps through the UI first would make that assertion depend on
 * screens it is not about, and would spend two-step rate-limit budget the
 * challenge test needs. Enrolment gets its own coverage in the Vitest suite.
 */
export async function enableTotpApi(
  session: ApiSession,
): Promise<{ otpauthUri: string; recoveryCodes: string[] }> {
  const setup = await expectOk(await post(session, "/api/v1/users/me/totp/setup"), "TOTP setup");
  const { otpauth_uri: otpauthUri } = (await setup.json()) as { otpauth_uri: string };

  const verify = await expectOk(
    await post(session, "/api/v1/users/me/totp/verify", { code: totpCode(otpauthUri) }),
    "TOTP setup verification",
  );
  const { recovery_codes: recoveryCodes } = (await verify.json()) as { recovery_codes: string[] };

  await expectOk(await post(session, "/api/v1/users/me/totp/activate"), "TOTP activation");
  return { otpauthUri, recoveryCodes };
}

/** A test account with two-step verification switched on. */
export interface TotpAccount extends TestAccount {
  /** The enrolment URI, kept so the spec can produce a live code at sign-in. */
  otpauthUri: string;
  recoveryCodes: string[];
}

/** Creates accounts, and cleans up the API contexts it opened. */
export class AccountFactory {
  private readonly opened: ApiSession[] = [];

  constructor(
    private readonly baseURL: string,
    private readonly admin: ApiSession,
  ) {}

  /**
   * A brand-new account, exactly as an administrator creates one: the
   * contract forces must_change_password on every account it makes, so this
   * is the state the forced-change screen is reached from.
   */
  async createPending(prefix = "e2e"): Promise<TestAccount> {
    const account = {
      username: uniqueUsername(prefix),
      password: `initial-${token(10)}`,
      email: `${token(8)}@e2e.invalid`,
      displayName: `E2E ${token(3)}`,
    };
    const created = await expectOk(
      await post(this.admin, "/api/v1/admin/users", {
        username: account.username,
        password: account.password,
        email: account.email,
        display_name: account.displayName,
      }),
      "admin user creation",
    );
    const { id } = (await created.json()) as { id: string };
    return { id, ...account };
  }

  /**
   * An account past its forced password change: the ordinary signed-in user.
   *
   * The session that did the change is signed out again before the account
   * is handed over, so a test that counts devices counts only the sessions
   * it created itself.
   */
  async createReady(prefix = "e2e"): Promise<TestAccount> {
    const pending = await this.createPending(prefix);
    const password = `settled-${token(10)}`;
    const session = await this.open(pending.username, pending.password);
    await changePasswordApi(session, pending.password, password);
    await logoutApi(session);
    return { ...pending, password };
  }

  /** A ready account with two-step verification already switched on. */
  async createWithTotp(prefix = "e2e2fa"): Promise<TotpAccount> {
    const account = await this.createReady(prefix);
    const session = await this.open(account.username, account.password);
    const { otpauthUri, recoveryCodes } = await enableTotpApi(session);
    await logoutApi(session);
    return { ...account, otpauthUri, recoveryCodes };
  }

  /** Opens an API session that is disposed with the factory. */
  async open(username: string, password: string, options: { userAgent?: string } = {}): Promise<ApiSession> {
    const session = await signInApi(this.baseURL, username, password, options);
    this.opened.push(session);
    return session;
  }

  async dispose(): Promise<void> {
    for (const session of this.opened) {
      await session.dispose();
    }
    this.opened.length = 0;
  }
}
