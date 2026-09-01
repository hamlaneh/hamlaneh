/**
 * Lifecycle of a HOME-MODE instance — the second deployment (ADR 012), which
 * is a different thing from the compose stack the rest of the suite drives.
 *
 * Home mode is `hamlaneh-server home`: one binary, one SQLite file, no
 * PostgreSQL, no Caddy, no media plane. What this module starts is therefore
 * NOT a stack at all — it is one process, on its own port, with its own data
 * directory, and it is torn down with that directory when the spec ends.
 *
 * The process runs inside a container, and that is a deliberate choice with
 * one honest consequence worth stating up front. What is under test is the
 * binary's own behaviour across a restart against a persistent data directory;
 * nothing here depends on the host's operating system, which is why the same
 * spec is evidence on a developer's Windows machine and on a Linux runner. Two
 * things are bought with the container and neither is available otherwise:
 *
 *   - the image the compose stack was just BUILT from carries the real web
 *     bundle inside the binary (deploy/Dockerfile replaces the committed
 *     placeholder in internal/webassets with the vite output), so the spec
 *     drives the shipped client — including its MLS/WASM half, which is the
 *     only thing that can send a message on a strict instance;
 *   - the run needs no Rust, no wasm-bindgen and no `npm run build` on the
 *     machine running the tests, which the native alternative would.
 *
 * The three "starts on Windows/macOS/Linux" halves of the Phase 4 gate are a
 * separate, manual check; this module is the fourth half, and says so.
 *
 * Restart means a NEW PROCESS. The container is stopped with SIGTERM (the
 * graceful path in cmd/hamlaneh-server), removed, and a fresh one is started
 * on the same named volume. Nothing is reused but the data directory, which
 * is exactly the claim under test.
 */
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createServer } from "node:net";

import { serverImage } from "./stack";

/**
 * The data directory inside the container. deploy/Dockerfile creates it owned
 * by the non-root user the image runs as, which is what lets Docker seed a
 * fresh named volume with the right ownership — a volume created from nothing
 * belongs to root and the server could not write it.
 */
const DATA_DIR = "/var/lib/hamlaneh";

const READY_TIMEOUT_MS = 120_000;
const POLL_INTERVAL_MS = 500;

/** Longer than the server's own 10s graceful shutdown, so SIGKILL never runs. */
const STOP_TIMEOUT_S = 30;

/**
 * The console announcement home mode makes on a FIRST run, and only then
 * (server/cmd/hamlaneh-server/home.go). Its absence on a later start is what
 * says the instance found an existing database rather than creating one, so a
 * spec asserting persistence has a cheap, direct control against the restart
 * silently landing on an empty directory.
 */
export const FIRST_ADMIN_ANNOUNCEMENT = "created the first administrator account";

const USERNAME_LINE = /^\s*username:\s*(\S+)\s*$/mu;
const PASSWORD_LINE = /^\s*password:\s*(\S+)\s*$/mu;

function docker(args: string[], options: { check: boolean } = { check: true }): string {
  const result = spawnSync("docker", args, { encoding: "utf8" });
  if (result.error !== undefined) {
    throw new Error(`docker ${args.join(" ")} could not be run: ${result.error.message}`);
  }
  const output = `${result.stdout}${result.stderr}`;
  if (options.check && result.status !== 0) {
    throw new Error(`docker ${args.join(" ")} failed (exit ${String(result.status)})\n${output}`);
  }
  return output;
}

/**
 * A port nothing holds, taken by binding and releasing it.
 *
 * The same number is used for the container's listener AND the host
 * publication, because home mode is told its own public origin before it
 * starts: the WebSocket handshake's Origin check is that exact origin, so the
 * URL the browser opens has to be the URL the server was configured with.
 */
async function freePort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const probe = createServer();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      if (address === null || typeof address === "string") {
        probe.close();
        reject(new Error("home mode: could not take a free port"));
        return;
      }
      const { port } = address;
      probe.close(() => {
        resolve(port);
      });
    });
  });
}

interface ContainerSpec {
  image: string;
  container: string;
  volume: string;
  port: number;
  baseURL: string;
}

/**
 * Starts one home-mode process.
 *
 * The environment is the whole configuration, and every entry is load-bearing:
 *
 *   HAMLANEH_HOME_ADDR   0.0.0.0 inside the container, because the default
 *                        loopback bind would be reachable from nothing outside
 *                        it. The trust boundary is kept by the publication
 *                        below, which is loopback-only on the host.
 *   HAMLANEH_PUBLIC_URL  the origin the browser really uses. Sessions are
 *                        Secure cookies, and a browser sends those over plain
 *                        HTTP only to localhost — which 127.0.0.1 is, so the
 *                        shipped cookie policy is exercised rather than
 *                        relaxed.
 *   HAMLANEH_DATA_DIR    the volume. Everything home mode owns lives here:
 *                        the SQLite database, the two generated keys, the
 *                        uploaded bytes.
 *
 * No LiveKit variables: home mode refuses to start with any of them set, and
 * no admin bootstrap variables either, because the generated first admin is
 * part of what the gate is about.
 */
function runContainer(spec: ContainerSpec): void {
  const port = String(spec.port);
  docker([
    "run",
    "--detach",
    "--name",
    spec.container,
    "--volume",
    `${spec.volume}:${DATA_DIR}`,
    "--publish",
    `127.0.0.1:${port}:${port}`,
    "--env",
    `HAMLANEH_HOME_ADDR=0.0.0.0:${port}`,
    "--env",
    `HAMLANEH_PUBLIC_URL=${spec.baseURL}`,
    "--env",
    `HAMLANEH_DATA_DIR=${DATA_DIR}`,
    spec.image,
    "home",
  ]);
}

/** Blocks until the instance answers, so a spec never races its own server. */
async function waitForInstance(baseURL: string, container: string): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  for (;;) {
    try {
      const response = await fetch(`${baseURL}/api/v1/instance`);
      if (response.ok) {
        return;
      }
    } catch {
      // The listener is not up yet; retry until the deadline.
    }
    if (Date.now() > deadline) {
      throw new Error(
        `home mode: ${baseURL}/api/v1/instance never answered\n${docker(["logs", container], { check: false })}`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
  }
}

/**
 * Reads the generated first administrator off the console.
 *
 * There is no other way to get it, by design: home.go prints the password to
 * stdout once and stores it nowhere, so a test has to read the process output
 * exactly as the person who started the binary does.
 */
function parseFirstAdmin(consoleOutput: string): { username: string; password: string } {
  const username = USERNAME_LINE.exec(consoleOutput)?.[1];
  const password = PASSWORD_LINE.exec(consoleOutput)?.[1];
  if (username === undefined || password === undefined) {
    throw new Error(`home mode: no first-admin announcement on the console\n${consoleOutput}`);
  }
  return { username, password };
}

/** One home-mode instance and its data directory. */
export class HomeInstance {
  private constructor(
    /** The origin the browser opens, and the one the server was told it has. */
    readonly baseURL: string,
    /** The generated first admin, exactly as the console announced it. */
    readonly admin: { username: string; password: string },
    private readonly image: string,
    private readonly container: string,
    private readonly volume: string,
    private readonly port: number,
  ) {}

  /** Starts a home instance whose data directory is empty — a first run. */
  static async start(): Promise<HomeInstance> {
    const name = `hamlaneh-e2e-home-${randomBytes(6).toString("hex")}`;
    const port = await freePort();
    const baseURL = `http://127.0.0.1:${String(port)}`;
    const image = serverImage();

    docker(["volume", "create", name]);
    runContainer({ image, container: name, volume: name, port, baseURL });
    await waitForInstance(baseURL, name);

    return new HomeInstance(
      baseURL,
      parseFirstAdmin(docker(["logs", name])),
      image,
      name,
      name,
      port,
    );
  }

  /**
   * Stops this process and starts a new one on the same data directory,
   * returning the new process's console output.
   *
   * The output is returned rather than swallowed because it carries the
   * control a persistence claim needs: a second start that DID announce a
   * first administrator would be one that found no database, and every later
   * assertion in the spec would then be measuring a fresh instance.
   */
  async restart(): Promise<string> {
    docker(["stop", "--time", String(STOP_TIMEOUT_S), this.container]);
    docker(["rm", this.container]);
    runContainer({
      image: this.image,
      container: this.container,
      volume: this.volume,
      port: this.port,
      baseURL: this.baseURL,
    });
    await waitForInstance(this.baseURL, this.container);
    return docker(["logs", this.container]);
  }

  /**
   * Removes the process and the data directory. Unchecked on purpose: this is
   * cleanup after a spec that may already have failed, and a teardown error
   * must not replace the failure that matters.
   */
  dispose(): void {
    docker(["rm", "--force", this.container], { check: false });
    docker(["volume", "rm", "--force", this.volume], { check: false });
  }
}
