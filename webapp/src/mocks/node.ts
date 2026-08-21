import { setupServer } from "msw/node";

import { handlers } from "./handlers";

/**
 * MSW server for Vitest. Test files opt in explicitly:
 * listen in beforeAll, resetHandlers in afterEach, close in afterAll
 * (see src/api/client.test.ts).
 */
export const server = setupServer(...handlers);
