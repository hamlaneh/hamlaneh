/**
 * A Linux Chromium for the committed screenshots, for machines that are not.
 *
 * The baselines in `specs/fa-rtl-snapshots.e2e.ts-snapshots/` are Linux
 * renderings, because CI is Linux and there is no honest pixel threshold
 * between FreeType and DirectWrite (the spec's header explains why). This
 * script is what lets somebody on Windows or macOS regenerate one: it runs
 * inside Playwright's own image — the same Chromium build and font packages CI
 * installs — and offers that browser over a WebSocket the local test runner
 * connects to, so the runner, the fixtures and the stack all stay where they
 * are and only the rendering moves.
 *
 * Run it from `webapp/`, with the e2e stack already up:
 *
 *   docker run --rm -d --name hamlaneh-pw \
 *     --network hamlaneh-e2e_edge -p 127.0.0.1:3333:3333 \
 *     -v "$PWD:/w" -w /w --entrypoint node \
 *     -e HAMLANEH_E2E_HTTPS_PORT \
 *     mcr.microsoft.com/playwright:v1.62.1-noble e2e/support/linux-browser.mjs
 *
 *   HAMLANEH_E2E_LINUX_BROWSER=ws://127.0.0.1:3333/ \
 *   HAMLANEH_E2E_REUSE_STACK=1 \
 *     npx playwright test --project=fa fa-rtl-snapshots --update-snapshots
 *
 *   docker rm -f hamlaneh-pw
 *
 * The image tag must match the @playwright/test version in package-lock.json,
 * or the connection is refused as a protocol mismatch. `playwright` is taken
 * from the mounted node_modules for the same reason; the browser binaries come
 * from the image, which is what PLAYWRIGHT_BROWSERS_PATH already points at.
 */
import net from "node:net";
import process from "node:process";

import { chromium } from "playwright";

/** The port the suite's baseURL names — see playwright.config.ts. */
const APP_PORT = Number(process.env.HAMLANEH_E2E_HTTPS_PORT ?? "8443");
/** Where this browser is offered. Published to the host's loopback only. */
const BROWSER_PORT = Number(process.env.HAMLANEH_E2E_BROWSER_PORT ?? "3333");
/** Caddy, by its service name on the stack's edge network. */
const UPSTREAM = { host: "caddy", port: 443 };

/**
 * The suite's baseURL is `https://localhost:<APP_PORT>`, and inside this
 * container "localhost" is this container. So localhost:<APP_PORT> is
 * forwarded to Caddy on the compose network — at the TCP layer, which leaves
 * TLS and its SNI untouched, so Caddy still matches its `localhost` site and
 * serves the certificate the run already verified.
 */
function forwardToCaddy() {
  const proxy = net.createServer((client) => {
    const upstream = net.connect(UPSTREAM);
    const drop = () => {
      client.destroy();
      upstream.destroy();
    };
    client.on("error", drop);
    upstream.on("error", drop);
    client.pipe(upstream).pipe(client);
  });
  proxy.on("error", (error) => {
    console.error(`could not forward :${String(APP_PORT)} to Caddy:`, error);
    process.exitCode = 1;
  });
  proxy.listen(APP_PORT, "127.0.0.1");
}

forwardToCaddy();

// 0.0.0.0 so the published port reaches it. The RPC is reachable by anything
// that can reach this port, which is why the documented `docker run` binds it
// to the host's loopback and the container is thrown away afterwards.
const server = await chromium.launchServer({ host: "0.0.0.0", port: BROWSER_PORT, wsPath: "/" });
console.log(server.wsEndpoint());
