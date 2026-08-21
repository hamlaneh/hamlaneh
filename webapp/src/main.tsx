import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "./i18n";
import "./index.css";
import App from "./App";
import { initTheme } from "./theme";

// Follow the OS light/dark preference unless an explicit choice is stored.
initTheme();

// API mocks are OFF by default. For backend-less development run
// `VITE_API_MOCK=1 npm run dev` to serve the contract mocks in src/mocks/.
// The DEV guard is statically false in production builds, so MSW code is
// never bundled or started there.
if (import.meta.env.DEV && import.meta.env.VITE_API_MOCK === "1") {
  const { worker } = await import("./mocks/browser");
  // "bypass": only contract endpoints are mocked; Vite's own module and
  // asset requests pass through without unhandled-request warnings.
  await worker.start({ onUnhandledRequest: "bypass" });
}

const container = document.getElementById("root");
if (container === null) {
  throw new Error("Root container #root not found in index.html");
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
