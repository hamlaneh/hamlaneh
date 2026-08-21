import { http, HttpResponse } from "msw";

import type { components } from "../api/schema";

type HealthStatus = components["schemas"]["HealthStatus"];
type ApiError = components["schemas"]["Error"];
type LoginRequest = components["schemas"]["LoginRequest"];
type ChangePasswordRequest = components["schemas"]["ChangePasswordRequest"];
type User = components["schemas"]["User"];
type UserPage = components["schemas"]["UserPage"];
type AdminCreateUserRequest = components["schemas"]["AdminCreateUserRequest"];

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

/** A user still on their admin-assigned temporary password (forced change). */
export const FIXTURE_NEWHIRE_CREDENTIALS = {
  identifier: "fixture.newhire",
  password: "fixture-temporary-password-mock",
} as const;

/** Any login attempt with this identifier gets a 429 (UI rate-limit path). */
export const FIXTURE_RATELIMITED_IDENTIFIER = "fixture.ratelimited";

export const FIXTURE_ADMIN: User = {
  id: "00000000-0000-4000-8000-000000000001",
  username: "fixture.admin",
  email: "fixture.admin@example.invalid",
  display_name: "Fixture Admin",
  locale: "en",
  is_admin: true,
  must_change_password: false,
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
  created_at: "2020-01-03T00:00:00Z",
};

const FIXTURE_CREATED_USER_ID = "00000000-0000-4000-8000-000000000099";

interface FixtureAccount {
  user: User;
  password: string;
}

const FIXTURE_ACCOUNTS: readonly FixtureAccount[] = [
  { user: FIXTURE_ADMIN, password: FIXTURE_CREDENTIALS.password },
  { user: FIXTURE_MEMBER, password: "fixture-member-password-mock" },
  { user: FIXTURE_NEWHIRE, password: FIXTURE_NEWHIRE_CREDENTIALS.password },
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
}

function freshAuthState(): MockAuthState {
  return {
    user: null,
    passwords: new Map(
      FIXTURE_ACCOUNTS.map((account) => [account.user.username, account.password]),
    ),
    accessExpired: false,
  };
}

let auth = freshAuthState();

/** Tests call this between cases to drop the mock session, password changes, and fixture cookies. */
export function resetMockAuth(): void {
  auth = freshAuthState();
  clearDomCookies();
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

function errorResponse(status: number, code: string, message: string) {
  return HttpResponse.json<ApiError>({ error: { code, message } }, { status });
}

function notAuthenticated() {
  return errorResponse(401, "not_authenticated", "No valid session.");
}

/**
 * Contract mocks for the Phase 1.1 API surface, typed against the generated
 * schema. Stateful just enough for the auth flows: login/logout maintain the
 * single mock session and change-password updates the stored password and
 * clears must_change_password.
 */
export const handlers = [
  http.get<never, never, HealthStatus>("/healthz", () =>
    HttpResponse.json({ status: "ok" }),
  ),

  http.post<never, LoginRequest, User | ApiError>(
    "/api/v1/auth/login",
    async ({ request }) => {
      const body = await request.json();
      if (body.identifier === FIXTURE_RATELIMITED_IDENTIFIER) {
        return errorResponse(
          429,
          "rate_limited",
          "Too many login attempts; retry later.",
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

  http.get<never, never, UserPage>("/api/v1/admin/users", () =>
    // Single page: no next_cursor.
    HttpResponse.json({ users: [FIXTURE_ADMIN, FIXTURE_MEMBER] }),
  ),

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
          created_at: "2020-01-04T00:00:00Z",
        },
        { status: 201 },
      );
    },
  ),
];
