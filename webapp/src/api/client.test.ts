import {
  afterAll,
  afterEach,
  beforeAll,
  describe,
  expect,
  expectTypeOf,
  it,
} from "vitest";

import { FIXTURE_ADMIN, FIXTURE_CREDENTIALS } from "../mocks/handlers";
import { server } from "../mocks/node";
import { api } from "./client";
import type { components } from "./schema";

type User = components["schemas"]["User"];
type ApiError = components["schemas"]["Error"];

// Two test-only overrides for the exported client (browser behavior is
// unaffected — MSW's browser worker intercepts at the Service Worker level):
// - baseUrl: the client's "/" is same-origin in the browser, but Node's fetch
//   rejects relative URLs. Use the jsdom origin, which is also what MSW
//   resolves its relative handler paths against.
// - fetch: openapi-fetch captures global fetch at createClient() time, before
//   server.listen() patches it in beforeAll. Defer to the current global so
//   requests reach the interceptor.
const testBase = {
  baseUrl: window.location.origin,
  fetch: (request: Request) => globalThis.fetch(request),
};

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
});

afterAll(() => {
  server.close();
});

describe("api client against the contract mocks", () => {
  it("fetches /healthz", async () => {
    const { data, error, response } = await api.GET("/healthz", {
      ...testBase,
    });

    expect(response.status).toBe(200);
    expect(error).toBeUndefined();
    expect(data).toEqual({ status: "ok" });
  });

  it("rejects unknown credentials with 401 and the contract Error shape", async () => {
    const { data, error, response } = await api.POST("/api/v1/auth/login", {
      ...testBase,
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
      ...testBase,
      body: { ...FIXTURE_CREDENTIALS },
    });

    expect(response.status).toBe(200);
    expect(error).toBeUndefined();
    expectTypeOf(data).toExtend<User | undefined>();
    expect(data).toEqual(FIXTURE_ADMIN);
  });
});
