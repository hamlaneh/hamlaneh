import type { APIRequestContext, BrowserContext } from "@playwright/test";

import { createChannelApi, uniqueSlug } from "../support/chat";
import { expect, test } from "../support/fixtures";
import { readStackState } from "../support/stack";

/**
 * ROADMAP §2: the media server's API key and secret never appear in an HTTP
 * response, a WebSocket message, or the built webapp bundle.
 *
 * # The one precision that decides whether this test is worth keeping
 *
 * The API **key** legitimately appears inside every access token, because it
 * is the token's issuer — that is LiveKit's protocol, and the key is an
 * identifier, not a credential. A scan that failed on it would fail on the
 * protocol working correctly and would be deleted within a week. The API
 * **secret** is the credential: it signs tokens and never travels in one.
 *
 * So the two halves of the pair are asserted differently, on purpose:
 *
 *   - the SECRET must be absent from everything, in every encoding it could
 *     plausibly wear — the hex text, upper case, base64 and base64url of that
 *     text, base64 of the bytes the hex spells, and those raw bytes;
 *   - the KEY may appear ONLY inside a JSON Web Token whose `iss` claim is
 *     that same key. Every occurrence is checked that way: the tokens are cut
 *     out of the text and the key is looked for in what is left. A key in a
 *     response body outside a token it issued, or anywhere in the bundle the
 *     browser is served, is a real leak and fails.
 *
 * # Two halves
 *
 * STATIC — what the server serves: the index document, every asset it links,
 * and the response headers of each. The lazily-imported media chunk is not
 * linked from the document, so it is covered by the dynamic half, which
 * records it when the call join pulls it in.
 *
 * DYNAMIC — every HTTP response body and header, and every WebSocket frame in
 * both directions, recorded from before the first navigation through a session
 * that signs in, opens a channel, sends a message and joins a call. The call
 * is the point: it is the only flow that mints a token, and the signal socket
 * is the only place a token legitimately travels.
 *
 * # Two controls, because a scan that finds nothing may have looked at nothing
 *
 *   1. the run must have observed at least one access token issued by this
 *      instance's key. Without that the recorder captured nothing meaningful
 *      and "no secret found" says nothing;
 *   2. the secret scanner is run over a synthetic text carrying the secret in
 *      each encoding, and must flag every one of them.
 *
 * # What this does NOT prove
 *
 * It covers the surface this spec exercises plus the linked bundle — not every
 * response the server can produce. An endpoint no browser here touches, an
 * error path not taken, an administrative screen not opened: none of them are
 * scanned. It is evidence about the paths a user walks, not a proof over the
 * whole API. The server-side unit tests own the token's claim set (ADR 005),
 * and this is the end-to-end complement to them.
 */

/** A JWT as it appears in text: three base64url segments, header first. */
const JWT_PATTERN = /ey[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}/gu;

/**
 * Names that must never reach a browser. A build that inlined the server's
 * environment would carry these even on a run whose generated credentials
 * post-date the bundle, which the values alone cannot catch.
 */
const FORBIDDEN_MARKERS = [
  "HAMLANEH_LIVEKIT_API_SECRET",
  "HAMLANEH_LIVEKIT_API_KEY",
  "LIVEKIT_KEYS",
];

interface Needle {
  label: string;
  bytes: Buffer;
}

interface Recorded {
  where: string;
  body: Buffer;
}

/** Every encoding the secret could plausibly be wearing. */
function secretNeedles(secret: string): Needle[] {
  const text = Buffer.from(secret, "utf8");
  // The secret is hex, so these are the 32 bytes it spells — what a leak that
  // decoded before re-encoding would carry.
  const decoded = Buffer.from(secret, "hex");
  const unpadded = (value: string): string => value.replace(/=+$/u, "");

  const candidates: [string, string][] = [
    ["the secret itself", secret],
    ["upper case", secret.toUpperCase()],
    ["base64 of the text", text.toString("base64")],
    ["base64 of the text, unpadded", unpadded(text.toString("base64"))],
    ["base64url of the text", text.toString("base64url")],
    ["base64 of the decoded bytes", decoded.toString("base64")],
    ["base64 of the decoded bytes, unpadded", unpadded(decoded.toString("base64"))],
    ["base64url of the decoded bytes", decoded.toString("base64url")],
  ];

  const seen = new Set<string>();
  const needles: Needle[] = [];
  for (const [label, value] of candidates) {
    if (value.length > 0 && !seen.has(value)) {
      seen.add(value);
      needles.push({ label, bytes: Buffer.from(value, "utf8") });
    }
  }
  needles.push({ label: "the raw bytes the hex spells", bytes: decoded });
  return needles;
}

/** Which encoding of the secret this haystack carries, if any. */
function findSecret(haystack: Buffer, needles: Needle[]): string | null {
  for (const needle of needles) {
    if (needle.bytes.length > 0 && haystack.includes(needle.bytes)) {
      return needle.label;
    }
  }
  return null;
}

/** The `iss` claim of a JWT, without verifying it — this is a scan, not auth. */
function jwtIssuer(jwt: string): string | null {
  const payload = jwt.split(".")[1];
  if (payload === undefined) {
    return null;
  }
  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as {
      iss?: unknown;
    };
    return typeof claims.iss === "string" ? claims.iss : null;
  } catch {
    // Not a token, or not one whose payload is JSON. Either way it is text
    // that must not be allowed to hide an occurrence of the key.
    return null;
  }
}

/** Tokens in this text that this instance's key issued — the scan's control. */
function issuedTokens(text: string, apiKey: string): number {
  return [...text.matchAll(JWT_PATTERN)].filter((match) => jwtIssuer(match[0]) === apiKey).length;
}

/**
 * Whether the key appears somewhere other than inside a token it issued.
 *
 * The tokens are removed first, so the legitimate occurrence — the `iss` claim
 * of every access token — cannot mask a real one beside it.
 */
function strayKey(text: string, apiKey: string): boolean {
  const withoutIssuedTokens = text.replace(JWT_PATTERN, (jwt) =>
    jwtIssuer(jwt) === apiKey ? "" : jwt,
  );
  return withoutIssuedTokens.includes(apiKey);
}

/** The index document and every same-origin asset it links. */
async function servedSurface(request: APIRequestContext): Promise<Recorded[]> {
  const index = await request.get("/");
  expect(index.ok(), "the application's index document is not being served").toBe(true);
  const html = await index.text();

  const paths = new Set<string>();
  for (const match of html.matchAll(/(?:src|href)="(\/[^"]*)"/gu)) {
    const path = match[1];
    if (path !== undefined) {
      paths.add(path);
    }
  }

  const surface: Recorded[] = [
    { where: "GET / (headers)", body: Buffer.from(JSON.stringify(index.headers()), "utf8") },
    { where: "GET /", body: Buffer.from(html, "utf8") },
  ];
  for (const path of paths) {
    const response = await request.get(path);
    surface.push({
      where: `GET ${path} (headers)`,
      body: Buffer.from(JSON.stringify(response.headers()), "utf8"),
    });
    surface.push({ where: `GET ${path}`, body: await response.body() });
  }
  return surface;
}

test.describe("LiveKit credential leak scan", () => {
  test("the media secret reaches nothing, and the key only ever rides a token it issued", async ({
    accounts,
    openApp,
    t,
  }) => {
    test.setTimeout(120_000);

    const { apiKey, apiSecret } = readStackState().livekit;
    const needles = secretNeedles(apiSecret);

    /* ── control 2: the scanner catches what it is looking for ────────── */

    for (const needle of needles) {
      const synthetic = Buffer.concat([
        Buffer.from("prefix", "utf8"),
        needle.bytes,
        Buffer.from("suffix", "utf8"),
      ]);
      expect(
        findSecret(synthetic, needles),
        `the scanner does not detect the secret encoded as ${needle.label}`,
      ).not.toBeNull();
    }

    /* ── the dynamic recorder, attached before the first navigation ───── */

    const recorded: Recorded[] = [];
    const bodies: Promise<void>[] = [];

    const record = (context: BrowserContext): void => {
      context.on("response", (response) => {
        recorded.push({
          where: `headers of ${response.url()}`,
          body: Buffer.from(JSON.stringify(response.headers()), "utf8"),
        });
        bodies.push(
          (async () => {
            try {
              recorded.push({ where: response.url(), body: await response.body() });
            } catch {
              // Redirects and aborted requests have no body to scan. Nothing
              // is being swallowed: there is no content here to look at.
            }
          })(),
        );
      });
      context.on("page", (page) => {
        page.on("websocket", (socket) => {
          // The URL matters as much as the frames: the access token travels to
          // the media server as a query parameter, not in a frame.
          recorded.push({ where: "websocket url", body: Buffer.from(socket.url(), "utf8") });
          const frame = (direction: string) => (payload: { payload: string | Buffer }) => {
            recorded.push({
              where: `websocket ${direction} ${socket.url()}`,
              body:
                typeof payload.payload === "string"
                  ? Buffer.from(payload.payload, "utf8")
                  : payload.payload,
            });
          };
          socket.on("framesent", frame("sent"));
          socket.on("framereceived", frame("received"));
        });
      });
    };

    /* ── drive the surface: sign in, talk, place a call ───────────────── */

    const user = await accounts.createReady("e2eleak");
    const api = await accounts.open(user.username, user.password);
    const channelId = await createChannelApi(api, uniqueSlug("leak"));

    const app = await openApp(user, `/c/${channelId}`, record);
    await app.sendMessage("Checking what goes over the wire.");

    // The only flow that mints a token. The connection itself is not waited
    // for: the token response and the signal socket are what carry credentials
    // and both exist before any media does, so the scan does not depend on the
    // runner's network being able to complete a call.
    const ticket = app.page.waitForResponse(
      (response) =>
        response.url().includes("/call/token") && response.request().method() === "POST",
    );
    const signal = app.page.waitForEvent("websocket", (socket) => socket.url().includes("/rtc"));
    await app.page
      .getByRole("region", { name: t("calls.strip.label") })
      .getByRole("button")
      .click();
    await ticket;
    await signal;
    // Long enough for the signal exchange to produce frames worth reading.
    await app.page.waitForTimeout(3_000);

    await Promise.all(bodies);

    /* ── the static half ──────────────────────────────────────────────── */

    const surface = await servedSurface(app.page.request);
    const everything = [...surface, ...recorded];
    expect(everything.length, "nothing was recorded to scan").toBeGreaterThan(10);

    /* ── control 1: the recorder saw a real token ─────────────────────── */

    const tokens = everything.reduce(
      (total, entry) => total + issuedTokens(entry.body.toString("utf8"), apiKey),
      0,
    );
    expect(
      tokens,
      "no access token issued by this instance's key was observed, so a clean scan proves nothing about where credentials travel",
    ).toBeGreaterThan(0);

    // What the scan actually looked at. A gate whose green run says only
    // "passed" is a gate nobody can tell from a gate that ran nothing.
    console.log(
      `key-leak scan: ${String(everything.length)} bodies and header sets (${String(surface.length)} of them served assets), ${String(tokens)} access token(s) issued by this instance's key, ${String(needles.length)} secret encodings searched`,
    );

    /* ── the secret: absent everywhere, in every encoding ─────────────── */

    for (const entry of everything) {
      const encoding = findSecret(entry.body, needles);
      expect(
        encoding,
        `the media server's API SECRET appears in ${entry.where}, encoded as ${String(encoding)}`,
      ).toBeNull();
    }

    /* ── the key: only ever inside a token it issued ──────────────────── */

    for (const entry of everything) {
      expect(
        strayKey(entry.body.toString("utf8"), apiKey),
        `the media server's API KEY appears in ${entry.where} outside any token it issued`,
      ).toBe(false);
    }

    /* ── and never in the bundle at all, tokens or not ────────────────── */

    for (const entry of surface) {
      const text = entry.body.toString("utf8");
      expect(text.includes(apiKey), `the media server's API KEY is served in ${entry.where}`).toBe(
        false,
      );
      for (const marker of FORBIDDEN_MARKERS) {
        expect(
          text.includes(marker),
          `${entry.where} carries ${marker}: the server's environment is reaching the browser`,
        ).toBe(false);
      }
    }
  });
});
