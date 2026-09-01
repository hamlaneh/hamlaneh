import { delay, http, HttpResponse } from "msw";
import {
  afterAll,
  afterEach,
  beforeAll,
  describe,
  expect,
  expectTypeOf,
  it,
  vi,
} from "vitest";

import {
  expireMockAccess,
  FIXTURE_ADMIN,
  FIXTURE_CREDENTIALS,
  resetMockAuth,
} from "../mocks/handlers";
import { server } from "../mocks/node";
import { api, retryAfterSeconds } from "./client";
import type { components } from "./schema";

type User = components["schemas"]["User"];
type TwoFactorChallenge = components["schemas"]["TwoFactorChallenge"];
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
    // Login's success union now includes the 202 two-step challenge, so the
    // typed data is User | TwoFactorChallenge | undefined. A 200 narrows it
    // to the user, which is exactly what the runtime assertion below pins.
    expectTypeOf(data).toExtend<User | TwoFactorChallenge | undefined>();
    expect(data).toEqual(FIXTURE_ADMIN);
  });
});

describe("Retry-After", () => {
  it("reads the whole seconds a 429 names", async () => {
    server.use(
      http.post("/api/v1/auth/login", () =>
        HttpResponse.json<ApiError>(
          { error: { code: "rate_limited", message: "Too many attempts." } },
          { status: 429, headers: { "Retry-After": "298" } },
        ),
      ),
    );

    const { response } = await api.POST("/api/v1/auth/login", {
      body: { identifier: "someone", password: "irrelevant-password" },
    });

    expect(response.status).toBe(429);
    expect(retryAfterSeconds(response)).toBe(298);
  });

  it.each([
    ["absent", undefined],
    ["blank", "   "],
    ["zero", "0"],
    ["negative", "-30"],
    ["fractional", "12.5"],
    ["not a number", "soon"],
    ["an HTTP-date", "Wed, 21 Oct 2026 07:28:00 GMT"],
    ["longer than the screen can honestly count down", "999999999"],
  ])("answers null when the header is %s", async (_case, value) => {
    server.use(
      http.post("/api/v1/auth/login", () =>
        HttpResponse.json<ApiError>(
          { error: { code: "rate_limited", message: "Too many attempts." } },
          {
            status: 429,
            ...(value === undefined ? {} : { headers: { "Retry-After": value } }),
          },
        ),
      ),
    );

    const { response } = await api.POST("/api/v1/auth/login", {
      body: { identifier: "someone", password: "irrelevant-password" },
    });

    // Every one of these means the same thing: this response cannot say when
    // the door reopens, so the caller must not render a number.
    expect(retryAfterSeconds(response)).toBeNull();
  });
});

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

describe("session auto-refresh middleware", () => {
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

describe("refresh across tabs", () => {
  /**
   * Stands in for the browser's per-origin lock manager, which jsdom does not
   * implement. ONE instance serves every simulated tab, exactly as one
   * browser's lock manager serves all its documents: requests for a name run
   * one at a time, in arrival order.
   */
  function installLockManager(): () => void {
    const tails = new Map<string, Promise<unknown>>();
    const manager = {
      query: () => Promise.resolve({}),
      request(name: string, callback: () => unknown): Promise<unknown> {
        const run = (tails.get(name) ?? Promise.resolve()).then(() =>
          callback(),
        );
        tails.set(
          name,
          run.then(
            () => undefined,
            () => undefined,
          ),
        );
        return run;
      },
    };
    Object.defineProperty(navigator, "locks", {
      value: manager,
      configurable: true,
    });
    return () => {
      Reflect.deleteProperty(navigator, "locks");
    };
  }

  /**
   * A second tab. Two documents are two module graphs — separate module
   * state, one shared browser — which is precisely why the client's
   * per-document single-flight guard cannot see the other tab's refresh.
   */
  async function openTab(): Promise<typeof import("./client")> {
    vi.resetModules();
    return import("./client");
  }

  /** The first refresh token rotatingSession hands the browser. */
  const FIRST_REFRESH_TOKEN = "refresh-1";

  /**
   * The session endpoints as the server really implements them: refresh
   * tokens rotate, and presenting an already-rotated one is reuse detection
   * — the whole family is revoked, which signs the account out of every
   * device.
   *
   * `jar` stands for the browser's shared cookie jar. It is read when a
   * request is dispatched and rewritten only when that request's response
   * comes back, so two refreshes that overlap present the SAME token. That is
   * the race, and the delay is what makes it deterministic here.
   */
  function rotatingSession(): {
    revoked: () => boolean;
    replay: (token: string) => void;
  } {
    let jar = FIRST_REFRESH_TOKEN;
    let minted = 1;
    let accessValid = false;
    let revoked = false;
    const used = new Set<string>();

    const unauthenticated = () =>
      HttpResponse.json<ApiError>(
        { error: { code: "not_authenticated", message: "Sign in again." } },
        { status: 401 },
      );

    /** Rotation with reuse detection, independent of who presents the token. */
    function present(token: string): boolean {
      if (revoked || used.has(token)) {
        revoked = true;
        accessValid = false;
        return false;
      }
      used.add(token);
      minted += 1;
      jar = `refresh-${String(minted)}`;
      accessValid = true;
      return true;
    }

    server.use(
      http.get("/api/v1/users/me", () =>
        accessValid ? HttpResponse.json(FIXTURE_ADMIN) : unauthenticated(),
      ),
      http.post("/api/v1/auth/refresh", async () => {
        const presented = jar;
        await delay(40);
        return present(presented)
          ? new HttpResponse(null, { status: 204 })
          : unauthenticated();
      }),
    );

    return {
      revoked: () => revoked,
      replay: (token) => {
        present(token);
      },
    };
  }

  it("keeps the session when two tabs wake at the same moment", async () => {
    const restoreLocks = installLockManager();
    const session = rotatingSession();
    const refreshes = trackRefreshCalls();
    const tabA = await openTab();
    const tabB = await openTab();

    try {
      const [a, b] = await Promise.all([
        tabA.api.GET("/api/v1/users/me"),
        tabB.api.GET("/api/v1/users/me"),
      ]);

      // A second refresh would present the token the first just rotated
      // away, which the server reads as theft: the family is revoked and the
      // honest user is signed out of every device.
      expect(session.revoked()).toBe(false);
      expect(a.response.status).toBe(200);
      expect(b.response.status).toBe(200);
      // So exactly ONE refresh may leave the browser, however many tabs wake.
      expect(refreshes.count()).toBe(1);
    } finally {
      refreshes.stop();
      restoreLocks();
    }
  });

  it("still refreshes when the access session expires again", async () => {
    const restoreLocks = installLockManager();
    const session = rotatingSession();
    const refreshes = trackRefreshCalls();
    const tab = await openTab();

    try {
      const first = await Promise.all([
        tab.api.GET("/api/v1/users/me"),
        tab.api.GET("/api/v1/users/me"),
        tab.api.GET("/api/v1/users/me"),
      ]);
      for (const { response } of first) {
        expect(response.status).toBe(200);
      }
      expect(refreshes.count()).toBe(1);

      // A later expiry is a refresh this tab genuinely needs: the record that
      // some tab refreshed a moment ago must not swallow it.
      server.use(
        http.get("/api/v1/users/me", () =>
          HttpResponse.json<ApiError>(
            { error: { code: "not_authenticated", message: "Sign in again." } },
            { status: 401 },
          ),
        ),
      );
      await tab.api.GET("/api/v1/users/me");

      expect(refreshes.count()).toBe(2);
      expect(session.revoked()).toBe(false);
    } finally {
      refreshes.stop();
      restoreLocks();
    }
  });

  it("leaves reuse detection to fire on a genuine replay", async () => {
    const restoreLocks = installLockManager();
    const session = rotatingSession();
    const tab = await openTab();

    try {
      // The browser refreshes normally, rotating its first token away.
      const signedIn = await tab.api.GET("/api/v1/users/me");
      expect(signedIn.response.status).toBe(200);
      expect(session.revoked()).toBe(false);

      // Somebody else now presents that token: another machine, so none of
      // this browser's coordination — not its lock, not its storage, not its
      // cookie jar — reaches them. Nothing about this fix is allowed to
      // soften what that means.
      session.replay(FIRST_REFRESH_TOKEN);

      expect(session.revoked()).toBe(true);
      // And the family revocation reaches this browser too: signed out.
      const after = await tab.api.GET("/api/v1/users/me");
      expect(after.response.status).toBe(401);
    } finally {
      restoreLocks();
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
