import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  test: {
    environment: "jsdom",
    // worker_threads instead of child processes: faster for this suite and
    // works in sandboxed environments where process spawning is restricted.
    pool: "threads",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
