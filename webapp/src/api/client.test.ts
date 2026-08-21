import { http, HttpResponse } from "msw";
import {
  afterAll,
  afterEach,
  beforeAll,
  describe,
  expect,
  expectTypeOf,
  it,
} from "vitest";

import {
  expireMockAccess,
  FIXTURE_ADMIN,
  FIXTURE_CREDENTIALS,
  resetMockAuth,
} from "../mocks/handlers";
import { server } from "../mocks/node";
import { api } from "./client";
import type { components } from "./schema";

type User = components["schemas"]["User"];
type ApiError = components["schemas"]["Error"];

const CSRF_COOKIE = "hamlaneh_csrf";
const CSRF_HEADER = "X-Hamlaneh-CSRF";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
  // Also clears the fixture cookies the mocked login writes to document.cookie.
  resetMockAuth();
});

afterAll(() => {
  server.close();
});

describe("api client against the contract mocks", () => {
  it("fetches /healthz", async () => {
    const { data, error, response } = await api.GET("/healthz");

    expect(response.status).toBe(200);
    expect(error).toBeUndefined();
    expect(data).toEqual({ status: "ok" });
  });

  it("rejects unknown credentials with 401 and the contract Error shape", async () => {
    const { data, error, response } = await api.POST("/api/v1/auth/login", {
      body: { identifier: "nobody", password: "wrong-password" },
    });

    expect(response.status).toBe(401);
    expect(data).toBeUndefined();
    expectTypeOf(error).toExtend<ApiError | undefined>();
    expect(error).toEqual({
      error: {
        code: "invalid_credentials",
        message: expect.any(String) as string,
      },
    });
  });

  it("returns a contract-shaped User for the fixture credentials", async () => {
    const { data, error, response } = await api.POST("/api/v1/auth/login", {
      body: { ...FIXTURE_CREDENTIALS },
    });

    expect(response.status).toBe(200);
    expect(error).toBeUndefined();
    expectTypeOf(data).toExtend<User | undefined>();
    expect(data).toEqual(FIXTURE_ADMIN);
  });
});

describe("session auto-refresh middleware", () => {
  /** Counts POST /api/v1/auth/refresh dispatches for the enclosing test. */
  function trackRefreshCalls(): { count: () => number; stop: () => void } {
    let calls = 0;
    const onStart = ({ request }: { request: Request }) => {
      if (new URL(request.url).pathname === "/api/v1/auth/refresh") {
        calls += 1;
      }
    };
    server.events.on("request:start", onStart);
    return {
      count: () => calls,
      stop: () => {
        server.events.removeListener("request:start", onStart);
      },
    };
  }

  it("refreshes and retries transparently when the access session expired", async () => {
    await api.POST("/api/v1/auth/login", { body: { ...FIXTURE_CREDENTIALS } });
    expireMockAccess();
    const refreshes = trackRefreshCalls();

    try {
      const { data, error, response } = await api.GET("/api/v1/users/me");

      // The caller never sees the intermediate 401.
      expect(response.status).toBe(200);
      expect(error).toBeUndefined();
      expect(data).toEqual(FIXTURE_ADMIN);
      expect(refreshes.count()).toBe(1);
    } finally {
      refreshes.stop();
    }
  });

  it("propagates the original 401 when the refresh itself fails", async () => {
    // Never signed in: /users/me answers 401 and so does the refresh attempt.
    const { data, error, response } = await api.GET("/api/v1/users/me");

    expect(response.status).toBe(401);
    expect(data).toBeUndefined();
    expect(error).toEqual({
      error: {
        code: "not_authenticated",
        message: expect.any(String) as string,
      },
    });
  });

  it("single-flights the refresh across concurrent 401s", async () => {
    await api.POST("/api/v1/auth/login", { body: { ...FIXTURE_CREDENTIALS } });
    expireMockAccess();
    const refreshes = trackRefreshCalls();

    try {
      const results = await Promise.all([
        api.GET("/api/v1/users/me"),
        api.GET("/api/v1/users/me"),
        api.GET("/api/v1/users/me"),
      ]);

      for (const { response } of results) {
        expect(response.status).toBe(200);
      }
      // Exactly ONE refresh: parallel refreshes would trip the server's
      // token-reuse family revocation.
      expect(refreshes.count()).toBe(1);
    } finally {
      refreshes.stop();
    }
  });
});

describe("CSRF middleware", () => {
  it("attaches the CSRF header to a POST when the cookie is present", async () => {
    document.cookie = `${CSRF_COOKIE}=test-csrf-token`;
    let captured: string | null = null;
    server.use(
      http.post("/api/v1/auth/logout", ({ request }) => {
        captured = request.headers.get(CSRF_HEADER);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    await api.POST("/api/v1/auth/logout");

    expect(captured).toBe("test-csrf-token");
  });

  it("does not attach the CSRF header to a GET", async () => {
    document.cookie = `${CSRF_COOKIE}=test-csrf-token`;
    let captured: string | null = "sentinel";
    server.use(
      http.get("/healthz", ({ request }) => {
        captured = request.headers.get(CSRF_HEADER);
        return HttpResponse.json({ status: "ok" });
      }),
    );

    await api.GET("/healthz");

    expect(captured).toBeNull();
  });

  it("sends no CSRF header when the cookie is absent", async () => {
    let captured: string | null = "sentinel";
    server.use(
      http.post("/api/v1/auth/logout", ({ request }) => {
        captured = request.headers.get(CSRF_HEADER);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    await api.POST("/api/v1/auth/logout");

    expect(captured).toBeNull();
  });
});
