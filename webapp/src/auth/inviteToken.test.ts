import { afterEach, describe, expect, it } from "vitest";

import { readInviteToken, resetInviteTokenForTest } from "./inviteToken";

/**
 * The shape of an invitation link, pinned on the client.
 *
 * It had no test until Phase 2, and the absence was the whole bug: the server
 * minted `/invite#token=…` and had a test asserting exactly that, while this
 * module read `/join/{token}` from the path and carried a comment claiming
 * that was what the server minted. Both halves were tested, each against its
 * own assumption, and every real invitation landed on the sign-in screen.
 *
 * So the value below is written the way the server writes it — path, then a
 * `token=` fragment — rather than the way this module happens to parse it.
 * `docs/api/openapi.yaml` (`CreatedInvite.url`) is the shared source both
 * sides now answer to.
 */
const TOKEN = "aaaaaaaaaaaaaaaaaaaaaaaa";

function visit(url: string): void {
  resetInviteTokenForTest();
  window.history.replaceState({}, "", url);
}

afterEach(() => {
  visit("/");
  resetInviteTokenForTest();
});

describe("the invitation link", () => {
  it("reads the token the server actually mints", () => {
    visit(`/invite#token=${TOKEN}`);
    expect(readInviteToken()).toBe(TOKEN);
  });

  it("strips the token from the address bar once it has been read", () => {
    visit(`/invite#token=${TOKEN}`);
    readInviteToken();

    // Not in history, not in a screenshot, not in whatever the next
    // pushState copies forward.
    expect(window.location.hash).toBe("");
    expect(window.location.pathname).toBe("/invite");
  });

  it("answers the same token on every later call, having consumed the fragment", () => {
    visit(`/invite#token=${TOKEN}`);
    expect(readInviteToken()).toBe(TOKEN);
    // StrictMode renders twice; the second must not find a scrubbed URL and
    // conclude there was no invitation.
    expect(readInviteToken()).toBe(TOKEN);
  });

  it("ignores the path form, which is what the client used to read", () => {
    // Kept as a case rather than deleted: no /join route is registered on the
    // server either, so a link of this shape 404s at the document. Reading it
    // here would resurrect a token shape nothing mints.
    visit(`/join/${TOKEN}`);
    expect(readInviteToken()).toBeNull();
  });

  it("ignores a fragment on any other page", () => {
    visit(`/#token=${TOKEN}`);
    expect(readInviteToken()).toBeNull();
  });

  it("discards anything outside the contract's shape", () => {
    for (const bad of ["short", `${TOKEN}!`, "a".repeat(129), ""]) {
      visit(`/invite#token=${bad}`);
      expect(readInviteToken()).toBeNull();
    }
  });
});
