import { setupWorker } from "msw/browser";

import { handlers } from "./handlers";

/**
 * MSW worker for backend-less development. Never started by default —
 * main.tsx starts it only when `VITE_API_MOCK=1` in a dev build.
 */
export const worker = setupWorker(...handlers);
