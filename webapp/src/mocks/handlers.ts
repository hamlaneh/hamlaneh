import { http, HttpResponse } from "msw";

import type { components } from "../api/schema";
import { chatHandlers, resetMockChat } from "./chat";
import { realtimeHandler } from "./ws";

type HealthStatus = components["schemas"]["HealthStatus"];
type ApiError = components["schemas"]["Error"];
type LoginRequest = components["schemas"]["LoginRequest"];
type ChangePasswordRequest = components["schemas"]["ChangePasswordRequest"];
type User = components["schemas"]["User"];
type UserPage = components["schemas"]["UserPage"];
type AdminCreateUserRequest = components["schemas"]["AdminCreateUserRequest"];
type UpdateCurrentUserRequest = components["schemas"]["UpdateCurrentUserRequest"];
type InstanceInfo = components["schemas"]["InstanceInfo"];
type TwoFactorChallenge = components["schemas"]["TwoFactorChallenge"];
type TotpLoginRequest = components["schemas"]["TotpLoginRequest"];
type PasswordResetRequest = components["schemas"]["PasswordResetRequest"];
type PasswordResetCompleteRequest = components["schemas"]["PasswordResetCompleteRequest"];
type PasswordConfirmRequest = components["schemas"]["PasswordConfirmRequest"];
type VerifyTotpSetupRequest = components["schemas"]["VerifyTotpSetupRequest"];
type TotpStatus = components["schemas"]["TotpStatus"];
type TotpSetup = components["schemas"]["TotpSetup"];
type RecoveryCodes = components["schemas"]["RecoveryCodes"];
type SessionFamily = components["schemas"]["SessionFamily"];
type OidcRedirect = components["schemas"]["OidcRedirect"];
type SessionFamilyList = components["schemas"]["SessionFamilyList"];

/** Cookie names from the spec's `sessionCookie` scheme and CSRF description. */
const SESSION_COOKIE = "hamlaneh_session";
const CSRF_COOKIE = "hamlaneh_csrf";

/** Obviously-fake cookie values; the handlers never read them back (see the session-state note below). */
const SESSION_VALUE_PREFIX = "fixture-session-not-a-real-token-for-";
const FIXTURE_CSRF_VALUE = "fixture-csrf-not-a-real-token";

/**
 * Fixture credentials. Obviously fake — they exist solely so tests and
 * mock-backed dev (`VITE_API_MOCK=1`) can exercise the auth flows.
 */
export const FIXTURE_CREDENTIALS = {
  identifier: "fixture.admin",
  password: "fixture-password-only-for-mocks",
} as const;

/** A member whose account reads Persian — the locale-follows-the-person case. */
export const FIXTURE_MEMBER_CREDENTIALS = {
  identifier: "fixture.member",
  password: "fixture-member-password-mock",
} as const;

/** A user still on their admin-assigned temporary password (forced change). */
export const FIXTURE_NEWHIRE_CREDENTIALS = {
  identifier: "fixture.newhire",
  password: "fixture-temporary-password-mock",
} as const;

/** Any login attempt with this identifier gets a 429 (UI rate-limit path). */
export const FIXTURE_RATELIMITED_IDENTIFIER = "fixture.ratelimited";

/**
 * The `Retry-After` that 429 carries, in seconds — the value a real run through
 * Caddy answered with, so the mock exercises the same arithmetic the browser
 * does (298 s reads as "5 minutes" once rounded up).
 */
export const FIXTURE_RETRY_AFTER_SECONDS = 298;

/** An account with two-step verification on: its login answers 202. */
export const FIXTURE_TWOSTEP_CREDENTIALS = {
  identifier: "fixture.twostep",
  password: "fixture-twostep-password-mock",
} as const;

/**
 * An account signing in under an org that requires two-step verification,
 * with no authenticator activated: its session carries
 * `totp_enrollment_required`.
 */
export const FIXTURE_ENROL_CREDENTIALS = {
  identifier: "fixture.enrol",
  password: "fixture-enrol-password-mock",
} as const;

/**
 * Both forced states at once — an admin-issued temporary password *and* the
 * enrolment policy. Reachable in production the first time an admin creates an
 * account on an instance that requires two-step verification.
 */
export const FIXTURE_BOTH_CREDENTIALS = {
  identifier: "fixture.both",
  password: "fixture-both-temporary-password-mock",
} as const;

/** The only authenticator code the mock accepts, at login and at setup. */
export const FIXTURE_TOTP_CODE = "123456";

/** The token the mock's reset links carry; anything else answers 401. */
export const FIXTURE_RESET_TOKEN = "fixture-reset-token-not-a-real-one";

/**
 * What the mock instance calls its identity provider. Latin, on purpose: it is
 * interpolated into Persian copy, which is what the bidi isolation around it is
 * for.
 */
export const FIXTURE_SSO_PROVIDER_NAME = "Fixture Identity";

/** Where POST /users/me/oidc sends the browser. Obviously not a real provider. */
export const FIXTURE_OIDC_REDIRECT_URL =
  "https://identity.example.invalid/authorize?fixture=1";

export const FIXTURE_RECOVERY_CODES = [
  "4T7M-9QKX",
  "B2LP-8WRD",
  "H5NC-3ZVY",
  "K9XA-6JQM",
  "P4RD-1TWL",
  "Q8ZK-5MBN",
  "T3JH-7VCX",
  "W6QM-2LDR",
  "X1NP-4KZB",
  "Z7VT-9HFM",
] as const;

/** Obviously fake: base32 without padding, exactly as the contract describes. */
const FIXTURE_TOTP_SECRET = "KZ4W9TQR2MHD7FJXKZ4W9TQR2MHD7FJX";

const FIXTURE_QR_SVG =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" role="presentation">' +
  '<rect width="8" height="8" fill="#fff"/><rect x="1" y="1" width="2" height="2"/></svg>';

export const FIXTURE_ADMIN: User = {
  id: "00000000-0000-4000-8000-000000000001",
  username: "fixture.admin",
  email: "fixture.admin@example.invalid",
  display_name: "Fixture Admin",
  locale: "en",
  is_admin: true,
  must_change_password: false,
  sso_linked: false,
  totp_enrollment_required: false,
  created_at: "2020-01-01T00:00:00Z",
};

export const FIXTURE_MEMBER: User = {
  id: "00000000-0000-4000-8000-000000000002",
  username: "fixture.member",
  email: "fixture.member@example.invalid",
  display_name: "Fixture Member",
  locale: "fa",
  is_admin: false,
  must_change_password: false,
  sso_linked: false,
  totp_enrollment_required: false,
  created_at: "2020-01-02T00:00:00Z",
};

export const FIXTURE_NEWHIRE: User = {
  id: "00000000-0000-4000-8000-000000000003",
  username: "fixture.newhire",
  email: "fixture.newhire@example.invalid",
  display_name: "Fixture Newhire",
  locale: "en",
  is_admin: false,
  must_change_password: true,
  sso_linked: false,
  totp_enrollment_required: false,
  created_at: "2020-01-03T00:00:00Z",
};

export const FIXTURE_TWOSTEP_USER: User = {
  id: "00000000-0000-4000-8000-000000000004",
  username: "fixture.twostep",
  email: "fixture.twostep@example.invalid",
  display_name: "Fixture Twostep",
  locale: "en",
  is_admin: false,
  must_change_password: false,
  sso_linked: false,
  totp_enrollment_required: false,
  created_at: "2020-01-05T00:00:00Z",
};

export const FIXTURE_ENROL_USER: User = {
  id: "00000000-0000-4000-8000-000000000005",
  username: "fixture.enrol",
  email: "fixture.enrol@example.invalid",
  display_name: "Fixture Enrol",
  locale: "en",
  is_admin: false,
  must_change_password: false,
  sso_linked: false,
  totp_enrollment_required: true,
  created_at: "2020-01-06T00:00:00Z",
};

export const FIXTURE_BOTH_USER: User = {
  id: "00000000-0000-4000-8000-000000000006",
  username: "fixture.both",
  email: "fixture.both@example.invalid",
  display_name: "Fixture Both",
  locale: "en",
  is_admin: false,
  must_change_password: true,
  sso_linked: false,
  totp_enrollment_required: true,
  created_at: "2020-01-07T00:00:00Z",
};

const FIXTURE_CREATED_USER_ID = "00000000-0000-4000-8000-000000000099";

/**
 * Four devices, matching the `settings-sessions` artboard — including the two
 * shapes the contract warns clients about: a family with no `ip`/`location`
 * key at all, and one whose `user_agent` is the empty string a client that
 * sent no header produces.
 */
const FIXTURE_SESSIONS: readonly SessionFamily[] = [
  {
    family_id: "00000000-0000-4000-8000-0000000000a1",
    user_agent:
      "Mozilla/5.0 (X11; Linux x86_64; rv:141.0) Gecko/20100101 Firefox/141.0",
    ip: "85.9.44.7",
    location: "Tehran, IR",
    last_active_at: "2020-01-01T00:00:00Z",
    current: true,
  },
  {
    family_id: "00000000-0000-4000-8000-0000000000a2",
    user_agent:
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Tauri/2.0.1",
    ip: "85.9.44.7",
    location: "Tehran, IR",
    last_active_at: "2019-12-31T23:00:00Z",
    current: false,
  },
  {
    family_id: "00000000-0000-4000-8000-0000000000a3",
    user_agent:
      "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/139.0.0.0 Mobile Safari/537.36",
    // No ip, no location: both are optional and the row must survive it.
    last_active_at: "2019-12-30T09:00:00Z",
    current: false,
  },
  {
    family_id: "00000000-0000-4000-8000-0000000000a4",
    // A client that sent no User-Agent at all.
    user_agent: "",
    ip: "203.0.113.44",
    location: null,
    last_active_at: "2019-12-01T09:00:00Z",
    current: false,
  },
];

interface FixtureAccount {
  user: User;
  password: string;
}

const FIXTURE_ACCOUNTS: readonly FixtureAccount[] = [
  { user: FIXTURE_ADMIN, password: FIXTURE_CREDENTIALS.password },
  { user: FIXTURE_MEMBER, password: FIXTURE_MEMBER_CREDENTIALS.password },
  { user: FIXTURE_NEWHIRE, password: FIXTURE_NEWHIRE_CREDENTIALS.password },
  { user: FIXTURE_TWOSTEP_USER, password: FIXTURE_TWOSTEP_CREDENTIALS.password },
  { user: FIXTURE_ENROL_USER, password: FIXTURE_ENROL_CREDENTIALS.password },
  { user: FIXTURE_BOTH_USER, password: FIXTURE_BOTH_CREDENTIALS.password },
];

/**
 * Mutable mock-server state: at most one logged-in user (a clone, so the
 * fixture constants above stay pristine), each account's current password,
 * and whether the short-lived access session has expired (refreshable state).
 */
interface MockAuthState {
  user: User | null;
  passwords: Map<string, string>;
  accessExpired: boolean;
  /** Set by a 202 login; the /auth/login/totp handler stands for the cookie. */
  challengeFor: User | null;
  /** The instance document these handlers serve; tests override it per case. */
  instance: InstanceInfo;
  totp: TotpStatus;
  /** Pending setup: absent until /totp/setup, "verified" after /totp/verify. */
  pendingTotp: "none" | "unverified" | "verified";
  sessions: SessionFamily[];
}

function freshAuthState(): MockAuthState {
  return {
    user: null,
    passwords: new Map(
      FIXTURE_ACCOUNTS.map((account) => [account.user.username, account.password]),
    ),
    accessExpired: false,
    challengeFor: null,
    // Reset is available by default so the sign-in screen offers the link;
    // a test that wants the other instance overrides this endpoint.
    instance: {
      password_min_length: 12,
      password_reset_available: true,
      max_file_size_bytes: 25 * 1024 * 1024,
      // Configured by default, same reasoning as the reset flag: the screens
      // that offer single sign-on exist, and a case that wants the instance
      // without a provider says so with setMockInstance.
      sso: { enabled: true, provider_name: FIXTURE_SSO_PROVIDER_NAME },
    },
    totp: { enabled: false },
    pendingTotp: "none",
    sessions: FIXTURE_SESSIONS.map((session) => ({ ...session })),
  };
}

let auth = freshAuthState();

/**
 * Tests call this between cases to drop the mock session, password changes and
 * fixture cookies. Conversation state goes with it: the chat shell is part of
 * the signed-in app, so leaking read positions or sent messages between cases
 * would be exactly as confusing as leaking a session.
 */
export function resetMockAuth(): void {
  auth = freshAuthState();
  clearDomCookies();
  resetMockChat();
}

/**
 * Simulates "access session expired, refresh cookie still valid": every
 * session-dependent handler answers 401 until POST /api/v1/auth/refresh
 * succeeds and clears the flag. Lets tests exercise the client's transparent
 * refresh+retry path.
 */
export function expireMockAccess(): void {
  auth.accessExpired = true;
}

/**
 * Overrides the instance document these handlers serve — the sign-in screen
 * reads `password_reset_available` from it to decide whether the reset link
 * exists at all.
 */
export function setMockInstance(info: Partial<InstanceInfo>): void {
  auth.instance = { ...auth.instance, ...info };
}

/**
 * Arms a two-step challenge with no password step in front of it — what the
 * OIDC callback leaves behind when it lands on `?sso=totp`. The real thing is a
 * challenge cookie the callback set; here it is the same module state a 202
 * login sets, because the mock stands in for the cookie either way.
 */
export function startMockSsoChallenge(): void {
  auth.challengeFor = FIXTURE_TWOSTEP_USER;
}

/** Attaches an identity to the signed-in fixture account (`User.sso_linked`). */
export function linkMockSso(): void {
  if (auth.user !== null) {
    auth.user.sso_linked = true;
  }
}

/**
 * Drops the signed-in account's password, which is the state the unlink refusal
 * exists for: single sign-on is then the account's only way in, so DELETE
 * answers 409 sso_unlink_no_password.
 */
export function clearMockPassword(): void {
  if (auth.user !== null) {
    auth.passwords.delete(auth.user.username);
  }
}

/** Turns two-step verification on for the signed-in fixture account. */
export function enableMockTotp(): void {
  auth.totp = {
    enabled: true,
    activated_at: "2026-08-21T10:00:00Z",
    recovery_codes_remaining: 8,
    recovery_codes_total: FIXTURE_RECOVERY_CODES.length,
  };
}

/** The signed-in user as session-dependent handlers see it: null while the access session is expired. */
function activeUser(): User | null {
  return auth.accessExpired ? null : auth.user;
}

/**
 * In the browser MSW itself applies mocked Set-Cookie headers to
 * document.cookie. Under setupServer + jsdom nothing does, so the handlers
 * mirror the cookies manually — the client's CSRF middleware reads
 * document.cookie and must see hamlaneh_csrf after a mocked login.
 */
function setDomCookies(username: string): void {
  if (typeof document === "undefined") {
    return;
  }
  document.cookie = `${SESSION_COOKIE}=${SESSION_VALUE_PREFIX}${username}; path=/`;
  document.cookie = `${CSRF_COOKIE}=${FIXTURE_CSRF_VALUE}; path=/`;
}

function clearDomCookies(): void {
  if (typeof document === "undefined") {
    return;
  }
  const expired = "path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT";
  document.cookie = `${SESSION_COOKIE}=; ${expired}`;
  document.cookie = `${CSRF_COOKIE}=; ${expired}`;
}

// The session deliberately lives ONLY in module state, never derived from the
// request cookies: MSW's node-side cookie jar persists mocked Set-Cookie values
// across tests, so a cookie-based session would leak between test cases.
// (Consequence in `VITE_API_MOCK=1` dev: a page reload starts signed out.)

// Error codes below mirror the real server's stable vocabulary
// (server/internal/httpserver/errors.go): invalid_request, invalid_credentials,
// not_authenticated, invalid_current_password, rate_limited. Do not invent
// codes here — the UI localizes by code, so drift breaks error messages.

function errorResponse(
  status: number,
  code: string,
  message: string,
  headers?: Record<string, string>,
) {
  return HttpResponse.json<ApiError>(
    { error: { code, message } },
    { status, ...(headers === undefined ? {} : { headers }) },
  );
}

function notAuthenticated() {
  return errorResponse(401, "not_authenticated", "No valid session.");
}

/**
 * What a session carrying `totp_enrollment_required` may still reach, per the
 * User schema: logout, reading users/me, and the three TOTP enrolment calls.
 *
 * Two additions the contract sentence does not spell out, both of which the
 * webapp would deadlock without:
 *
 *   - the rest of `/auth/` — refresh (the client's transparent retry path) and
 *     change-password, which a session carrying BOTH forced flags has to reach
 *     to clear the first one;
 *   - `/instance`, which needs no session at all and so cannot be gated by a
 *     property of one.
 */
function enrolmentGateAdmits(method: string, pathname: string): boolean {
  if (pathname === "/api/v1/instance" || pathname.startsWith("/api/v1/auth/")) {
    return true;
  }
  if (pathname === "/api/v1/users/me") {
    // Reading, not patching: the language switcher is 403 here.
    return method === "GET";
  }
  return ["setup", "verify", "activate"].some(
    (step) => pathname === `/api/v1/users/me/totp/${step}`,
  );
}

/**
 * Contract mocks for the Phase 1.1 API surface, typed against the generated
 * schema. Stateful just enough for the auth flows: login/logout maintain the
 * single mock session and change-password updates the stored password and
 * clears must_change_password.
 */
export const handlers = [
  // First, so it can refuse before any handler below answers: while the
  // signed-in session owes an enrolment, everything outside the allowlist is
  // 403 totp_enrollment_required. Returning undefined falls through to the
  // real handler, which keeps `onUnhandledRequest: "error"` working for
  // everything else.
  http.all("/api/v1/*", ({ request }) => {
    if (activeUser()?.totp_enrollment_required !== true) {
      return undefined;
    }
    const { pathname } = new URL(request.url);
    return enrolmentGateAdmits(request.method, pathname)
      ? undefined
      : errorResponse(
          403,
          "totp_enrollment_required",
          "Set up two-step verification to continue.",
        );
  }),

  http.get<never, never, HealthStatus>("/healthz", () =>
    HttpResponse.json({ status: "ok" }),
  ),

  http.post<never, LoginRequest, User | TwoFactorChallenge | ApiError>(
    "/api/v1/auth/login",
    async ({ request }) => {
      const body = await request.json();
      if (body.identifier === FIXTURE_RATELIMITED_IDENTIFIER) {
        return errorResponse(
          429,
          "rate_limited",
          "Too many login attempts; retry later.",
          // The real server sets Retry-After on every 429 it produces
          // (server/internal/httpserver/errors.go), and the spec documents it
          // on RateLimited. A mock that omitted it would only ever exercise
          // the fallback path.
          { "Retry-After": String(FIXTURE_RETRY_AFTER_SECONDS) },
        );
      }
      const account = FIXTURE_ACCOUNTS.find(
        (entry) =>
          entry.user.username === body.identifier ||
          entry.user.email === body.identifier,
      );
      const validCredentials =
        account !== undefined &&
        auth.passwords.get(account.user.username) === body.password;
      if (!validCredentials) {
        // Unknown user and wrong password answer identically (contract: no
        // account enumeration).
        return errorResponse(
          401,
          "invalid_credentials",
          "Incorrect username/email or password.",
        );
      }
      if (account.user.username === FIXTURE_TWOSTEP_USER.username) {
        // Password verified, but two-step verification is on: no session yet.
        // The challenge cookie is HttpOnly on the real server, so the mock
        // keeps it in module state instead.
        auth.challengeFor = { ...account.user };
        return HttpResponse.json<TwoFactorChallenge>({ methods: ["totp"] }, { status: 202 });
      }
      auth.user = { ...account.user };
      setDomCookies(account.user.username);
      const headers = new Headers({ "Content-Type": "application/json" });
      // Not HttpOnly: the browser worker sets cookies via document.cookie.
      headers.append(
        "Set-Cookie",
        `${SESSION_COOKIE}=${SESSION_VALUE_PREFIX}${account.user.username}; Path=/; SameSite=Strict`,
      );
      headers.append(
        "Set-Cookie",
        `${CSRF_COOKIE}=${FIXTURE_CSRF_VALUE}; Path=/; SameSite=Strict`,
      );
      return HttpResponse.json(auth.user, { status: 200, headers });
    },
  ),

  http.post<never, never, ApiError | null>(
    "/api/v1/auth/logout",
    () => {
      if (activeUser() === null) {
        return notAuthenticated();
      }
      auth.user = null;
      clearDomCookies();
      const headers = new Headers();
      headers.append("Set-Cookie", `${SESSION_COOKIE}=; Path=/; Max-Age=0`);
      headers.append("Set-Cookie", `${CSRF_COOKIE}=; Path=/; Max-Age=0`);
      return new HttpResponse(null, { status: 204, headers });
    },
  ),

  http.post<never, never, ApiError | null>(
    "/api/v1/auth/refresh",
    () => {
      if (auth.user === null) {
        // No refreshable session (never signed in, signed out, or family
        // revoked): contract answers 401.
        return notAuthenticated();
      }
      // Rotate: the access session is valid again, fresh cookies are set.
      auth.accessExpired = false;
      setDomCookies(auth.user.username);
      const headers = new Headers();
      headers.append(
        "Set-Cookie",
        `${SESSION_COOKIE}=${SESSION_VALUE_PREFIX}${auth.user.username}; Path=/; SameSite=Strict`,
      );
      headers.append(
        "Set-Cookie",
        `${CSRF_COOKIE}=${FIXTURE_CSRF_VALUE}; Path=/; SameSite=Strict`,
      );
      return new HttpResponse(null, { status: 204, headers });
    },
  ),

  http.post<never, ChangePasswordRequest, ApiError | null>(
    "/api/v1/auth/change-password",
    async ({ request }) => {
      const user = activeUser();
      if (user === null) {
        return notAuthenticated();
      }
      const body = await request.json();
      if (body.new_password.length < 12) {
        return errorResponse(
          400,
          "invalid_request",
          "New password must be at least 12 characters.",
        );
      }
      if (auth.passwords.get(user.username) !== body.current_password) {
        return errorResponse(
          403,
          "invalid_current_password",
          "Current password is incorrect.",
        );
      }
      auth.passwords.set(user.username, body.new_password);
      user.must_change_password = false;
      // Contract: all OTHER sessions are revoked; this one stays valid.
      auth.sessions = auth.sessions.filter((session) => session.current);
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.get<never, never, User | ApiError>("/api/v1/users/me", () => {
    const user = activeUser();
    if (user === null) {
      return notAuthenticated();
    }
    return HttpResponse.json(user);
  }),

  // Saving the account's language. Deliberately answered for a user who owes
  // a password change too: the route is classSessionMustChangeAllowed, and a
  // mock that refused there would hide the switcher the forced-change screen
  // actually renders.
  http.patch<never, UpdateCurrentUserRequest, User | ApiError>(
    "/api/v1/users/me",
    async ({ request }) => {
      const user = activeUser();
      if (user === null) {
        return notAuthenticated();
      }
      const body = await request.json();
      if (body.locale !== undefined) {
        user.locale = body.locale;
      }
      return HttpResponse.json(user);
    },
  ),

  http.get<never, never, InstanceInfo>("/api/v1/instance", () =>
    HttpResponse.json(auth.instance),
  ),

  /* ── two-step sign-in ────────────────────────────────────────────── */

  http.post<never, TotpLoginRequest, User | ApiError>(
    "/api/v1/auth/login/totp",
    async ({ request }) => {
      const challenged = auth.challengeFor;
      if (challenged === null) {
        // No live challenge: missing, expired, consumed or revoked.
        return notAuthenticated();
      }
      const body = await request.json();
      const normalized = body.code.replace(/[\s-]/gu, "").toUpperCase();
      const recovery = FIXTURE_RECOVERY_CODES.map((entry) => entry.replace("-", ""));
      if (normalized !== FIXTURE_TOTP_CODE && !recovery.includes(normalized)) {
        // The challenge survives a wrong code — the caller retries.
        return errorResponse(401, "invalid_totp_code", "That code is not valid.");
      }
      auth.challengeFor = null;
      auth.user = challenged;
      setDomCookies(challenged.username);
      const headers = new Headers({ "Content-Type": "application/json" });
      headers.append(
        "Set-Cookie",
        `${SESSION_COOKIE}=${SESSION_VALUE_PREFIX}${challenged.username}; Path=/; SameSite=Strict`,
      );
      headers.append(
        "Set-Cookie",
        `${CSRF_COOKIE}=${FIXTURE_CSRF_VALUE}; Path=/; SameSite=Strict`,
      );
      return HttpResponse.json(challenged, { status: 200, headers });
    },
  ),

  /* ── password reset ──────────────────────────────────────────────── */

  http.post<never, PasswordResetRequest, ApiError | null>(
    "/api/v1/auth/reset-request",
    async ({ request }) => {
      const body = await request.json();
      if (!body.email.includes("@")) {
        return errorResponse(400, "invalid_request", "That is not an email address.");
      }
      // Enumeration-safe: 202 with an empty body whether or not the address
      // matches an account.
      return new HttpResponse(null, { status: 202 });
    },
  ),

  http.post<never, PasswordResetCompleteRequest, ApiError | null>(
    "/api/v1/auth/reset-complete",
    async ({ request }) => {
      const body = await request.json();
      if (body.token !== FIXTURE_RESET_TOKEN) {
        // Unknown, expired and already-used answer identically.
        return errorResponse(401, "invalid_reset_token", "That link is no longer valid.");
      }
      if (body.new_password.length < 12) {
        return errorResponse(400, "invalid_request", "New password is too short.");
      }
      auth.passwords.set(FIXTURE_ADMIN.username, body.new_password);
      // A reset revokes EVERY session family and sets no cookies.
      auth.user = null;
      auth.challengeFor = null;
      clearDomCookies();
      return new HttpResponse(null, { status: 204 });
    },
  ),

  /* ── two-step verification management ────────────────────────────── */

  http.get<never, never, TotpStatus | ApiError>("/api/v1/users/me/totp", () =>
    activeUser() === null ? notAuthenticated() : HttpResponse.json(auth.totp),
  ),

  http.post<never, never, TotpSetup | ApiError>("/api/v1/users/me/totp/setup", () => {
    if (activeUser() === null) {
      return notAuthenticated();
    }
    if (auth.totp.enabled) {
      return errorResponse(409, "totp_already_enabled", "Two-step verification is on.");
    }
    auth.pendingTotp = "unverified";
    return HttpResponse.json(
      {
        secret: FIXTURE_TOTP_SECRET,
        otpauth_uri: `otpauth://totp/Hamlaneh:${FIXTURE_ADMIN.username}?secret=${FIXTURE_TOTP_SECRET}&issuer=Hamlaneh`,
        qr_svg: FIXTURE_QR_SVG,
      },
      { status: 200, headers: { "Cache-Control": "no-store" } },
    );
  }),

  http.post<never, VerifyTotpSetupRequest, RecoveryCodes | ApiError>(
    "/api/v1/users/me/totp/verify",
    async ({ request }) => {
      if (activeUser() === null) {
        return notAuthenticated();
      }
      if (auth.pendingTotp === "none") {
        return errorResponse(409, "totp_setup_expired", "Start again at step 1.");
      }
      const body = await request.json();
      if (body.code !== FIXTURE_TOTP_CODE) {
        // The pending setup survives: the cells clear, the secret stays.
        return errorResponse(403, "invalid_totp_code", "That code did not match.");
      }
      auth.pendingTotp = "verified";
      return HttpResponse.json(
        { codes: [...FIXTURE_RECOVERY_CODES] },
        { status: 200, headers: { "Cache-Control": "no-store" } },
      );
    },
  ),

  http.post<never, never, ApiError | null>("/api/v1/users/me/totp/activate", () => {
    const user = activeUser();
    if (user === null) {
      return notAuthenticated();
    }
    if (auth.pendingTotp !== "verified") {
      return errorResponse(409, "totp_setup_not_verified", "Nothing to activate.");
    }
    auth.pendingTotp = "none";
    // The debt this session was carrying is paid: the account now has an
    // activated authenticator, so the gate opens without a new sign-in.
    user.totp_enrollment_required = false;
    auth.totp = {
      enabled: true,
      activated_at: "2026-08-21T10:00:00Z",
      recovery_codes_remaining: FIXTURE_RECOVERY_CODES.length,
      recovery_codes_total: FIXTURE_RECOVERY_CODES.length,
    };
    return new HttpResponse(null, { status: 204 });
  }),

  http.post<never, PasswordConfirmRequest, ApiError | null>(
    "/api/v1/users/me/totp/disable",
    async ({ request }) => {
      const user = activeUser();
      if (user === null) {
        return notAuthenticated();
      }
      if (!auth.totp.enabled) {
        return errorResponse(409, "totp_not_enabled", "Two-step verification is off.");
      }
      const body = await request.json();
      if (auth.passwords.get(user.username) !== body.password) {
        return errorResponse(403, "invalid_current_password", "Password is incorrect.");
      }
      // Off, and every recovery code with it. Sessions are deliberately kept.
      auth.totp = { enabled: false };
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.post<never, PasswordConfirmRequest, RecoveryCodes | ApiError>(
    "/api/v1/users/me/totp/recovery-codes",
    async ({ request }) => {
      const user = activeUser();
      if (user === null) {
        return notAuthenticated();
      }
      if (!auth.totp.enabled) {
        return errorResponse(409, "totp_not_enabled", "Two-step verification is off.");
      }
      const body = await request.json();
      if (auth.passwords.get(user.username) !== body.password) {
        return errorResponse(403, "invalid_current_password", "Password is incorrect.");
      }
      auth.totp = {
        ...auth.totp,
        recovery_codes_remaining: FIXTURE_RECOVERY_CODES.length,
        recovery_codes_total: FIXTURE_RECOVERY_CODES.length,
      };
      return HttpResponse.json(
        { codes: [...FIXTURE_RECOVERY_CODES] },
        { status: 200, headers: { "Cache-Control": "no-store" } },
      );
    },
  ),

  /* ── single sign-on linking ──────────────────────────────────────── */

  // GET /auth/oidc/start and /auth/oidc/callback are deliberately NOT mocked:
  // both are browser navigations answered with a 302, and MSW intercepts fetch,
  // not the address bar. The webapp reaches them by link and by redirect.

  http.post<never, never, OidcRedirect | ApiError>("/api/v1/users/me/oidc", () => {
    const user = activeUser();
    if (user === null) {
      return notAuthenticated();
    }
    if (auth.instance.sso?.enabled !== true) {
      return errorResponse(503, "sso_unavailable", "Single sign-on is not configured.");
    }
    if (user.sso_linked) {
      return errorResponse(409, "sso_already_linked", "An identity is already linked.");
    }
    return HttpResponse.json({ redirect_url: FIXTURE_OIDC_REDIRECT_URL });
  }),

  http.delete<never, never, ApiError | null>("/api/v1/users/me/oidc", () => {
    const user = activeUser();
    if (user === null) {
      return notAuthenticated();
    }
    if (!user.sso_linked) {
      return errorResponse(404, "sso_not_linked", "Nothing was linked.");
    }
    if (auth.passwords.get(user.username) === undefined) {
      // Contract: unlinking an account with no password would leave it with no
      // way in at all, so the server refuses and an administrator has to issue
      // a temporary password first.
      return errorResponse(
        409,
        "sso_unlink_no_password",
        "This account has no password.",
      );
    }
    user.sso_linked = false;
    return new HttpResponse(null, { status: 204 });
  }),

  /* ── sessions ────────────────────────────────────────────────────── */

  http.get<never, never, SessionFamilyList | ApiError>("/api/v1/users/me/sessions", () =>
    activeUser() === null
      ? notAuthenticated()
      : HttpResponse.json({ sessions: auth.sessions }),
  ),

  http.post<never, never, ApiError | null>(
    "/api/v1/users/me/sessions/revoke-others",
    () => {
      if (activeUser() === null) {
        return notAuthenticated();
      }
      auth.sessions = auth.sessions.filter((session) => session.current);
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.delete<{ familyId: string }, never, ApiError | null>(
    "/api/v1/users/me/sessions/:familyId",
    ({ params }) => {
      if (activeUser() === null) {
        return notAuthenticated();
      }
      const target = auth.sessions.find((session) => session.family_id === params.familyId);
      if (target === undefined) {
        // A family that is not the caller's answers 404 — a guessed id never
        // confirms another account's session.
        return errorResponse(404, "session_not_found", "No such session.");
      }
      if (target.current) {
        return errorResponse(
          400,
          "cannot_revoke_current_session",
          "Use logout for this device.",
        );
      }
      auth.sessions = auth.sessions.filter((session) => session.family_id !== params.familyId);
      return new HttpResponse(null, { status: 204 });
    },
  ),

  http.get<never, never, UserPage>("/api/v1/admin/users", () =>
    // Single page: no next_cursor.
    HttpResponse.json({ users: [FIXTURE_ADMIN, FIXTURE_MEMBER] }),
  ),

  ...chatHandlers,
  realtimeHandler,

  http.post<never, AdminCreateUserRequest, User>(
    "/api/v1/admin/users",
    async ({ request }) => {
      const body = await request.json();
      return HttpResponse.json(
        {
          id: FIXTURE_CREATED_USER_ID,
          username: body.username,
          email: body.email ?? null,
          display_name: body.display_name ?? body.username,
          locale: body.locale ?? "en",
          is_admin: body.is_admin ?? false,
          // Contract: admin-created users must replace the initial password.
          must_change_password: true,
          // Nothing links an identity at creation time; the account holder
          // does that from Settings once they are signed in.
          sso_linked: false,
          // A property of a session, and this account has none yet: it is
          // decided when they first sign in, not when they are created.
          totp_enrollment_required: false,
          created_at: "2020-01-04T00:00:00Z",
        },
        { status: 201 },
      );
    },
  ),
];
