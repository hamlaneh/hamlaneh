import { http, HttpResponse } from "msw";

import type { components } from "../api/schema";

type HealthStatus = components["schemas"]["HealthStatus"];
type ApiError = components["schemas"]["Error"];
type LoginRequest = components["schemas"]["LoginRequest"];
type User = components["schemas"]["User"];
type UserPage = components["schemas"]["UserPage"];
type AdminCreateUserRequest = components["schemas"]["AdminCreateUserRequest"];

/** Cookie name from the spec's `sessionCookie` security scheme. */
const SESSION_COOKIE = "hamlaneh_session";
const FIXTURE_SESSION_VALUE = "fixture-session-not-a-real-token";

/**
 * The only credentials the login mock accepts. Obviously fake — they exist
 * solely so tests and mock-backed dev can exercise the success path.
 */
export const FIXTURE_CREDENTIALS = {
  identifier: "fixture.admin",
  password: "fixture-password-only-for-mocks",
} as const;

export const FIXTURE_ADMIN: User = {
  id: "00000000-0000-4000-8000-000000000001",
  username: "fixture.admin",
  email: "fixture.admin@example.invalid",
  display_name: "Fixture Admin",
  locale: "en",
  is_admin: true,
  created_at: "2020-01-01T00:00:00Z",
};

export const FIXTURE_MEMBER: User = {
  id: "00000000-0000-4000-8000-000000000002",
  username: "fixture.member",
  email: "fixture.member@example.invalid",
  display_name: "Fixture Member",
  locale: "fa",
  is_admin: false,
  created_at: "2020-01-02T00:00:00Z",
};

const FIXTURE_CREATED_USER_ID = "00000000-0000-4000-8000-000000000099";

// Error codes below are illustrative mock values; the backend owns the real
// stable codes (contract only fixes the shape and the example
// `invalid_credentials`).

/**
 * Contract mocks for the Phase 1.1 API surface, typed against the generated
 * schema. Deliberately stateless and deterministic: the only "session" is the
 * fixture cookie set by a successful login.
 */
export const handlers = [
  http.get<never, never, HealthStatus>("/healthz", () =>
    HttpResponse.json({ status: "ok" }),
  ),

  http.post<never, LoginRequest, User | ApiError>(
    "/api/v1/auth/login",
    async ({ request }) => {
      const body = await request.json();
      const validCredentials =
        body.identifier === FIXTURE_CREDENTIALS.identifier &&
        body.password === FIXTURE_CREDENTIALS.password;
      if (!validCredentials) {
        return HttpResponse.json(
          {
            error: {
              code: "invalid_credentials",
              message: "Incorrect username/email or password.",
            },
          },
          { status: 401 },
        );
      }
      return HttpResponse.json(FIXTURE_ADMIN, {
        status: 200,
        headers: {
          // Not HttpOnly: the browser worker sets cookies via document.cookie.
          "Set-Cookie": `${SESSION_COOKIE}=${FIXTURE_SESSION_VALUE}; Path=/; SameSite=Strict`,
        },
      });
    },
  ),

  http.get<never, never, User | ApiError>(
    "/api/v1/users/me",
    ({ cookies }) => {
      if (cookies[SESSION_COOKIE] !== FIXTURE_SESSION_VALUE) {
        return HttpResponse.json(
          {
            error: {
              code: "unauthenticated",
              message: "No valid session.",
            },
          },
          { status: 401 },
        );
      }
      return HttpResponse.json(FIXTURE_ADMIN);
    },
  ),

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
          created_at: "2020-01-03T00:00:00Z",
        },
        { status: 201 },
      );
    },
  ),
];
