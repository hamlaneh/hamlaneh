import { describe, expect, it, vi } from "vitest";

import type { Channel } from "../chat/types";
import type { ChannelMlsState, MediaKey, MlsState } from "../mls/types";
import { initialMlsState } from "../mls/types";
import { callKeyState } from "./e2ee";

/**
 * The join gates, asked one at a time.
 *
 * Every case below is a way a call could end up unencrypted in a conversation
 * that promised otherwise, so the assertion that matters in most of them is
 * the *absence* of `plain`: a refusal is a working outcome here, and a
 * downgrade is the only real failure.
 */

const CHANNEL_ID = "3f5b1a02-0000-4000-8000-000000000001";

function channel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: CHANNEL_ID,
    kind: "channel",
    slug: "deploys",
    e2ee: true,
    created_by: "someone",
    ...overrides,
  } as Channel;
}

function ready(channelState: ChannelMlsState = { status: "ready" }): MlsState {
  return {
    ...initialMlsState,
    device: { status: "ready" },
    channels: { [CHANNEL_ID]: channelState },
  };
}

const KEY: MediaKey = { epoch: 4, secret: new Uint8Array(32).fill(7) };
const deriveKey = () => KEY;
const deriveNothing = () => null;

describe("callKeyState", () => {
  it("keys an encrypted conversation whose group this device holds", () => {
    expect(callKeyState(channel(), ready(), deriveKey)).toEqual({
      kind: "keyed",
      key: KEY,
      publishBlocked: false,
    });
  });

  it("leaves a plaintext conversation alone, and does not derive a key for it", () => {
    // Media E2EE rides the group: a conversation with no group has no key to
    // derive and promises nothing about its call it does not already promise
    // about its messages.
    const derive = vi.fn(deriveKey);
    expect(callKeyState(channel({ e2ee: false }), initialMlsState, derive)).toEqual({
      kind: "plain",
    });
    expect(derive).not.toHaveBeenCalled();
  });

  it("refuses a conversation it cannot identify", () => {
    // Unreachable in the shell — a call starts from a conversation on screen —
    // but "the object was missing" is exactly the shape of the bug that would
    // otherwise place a plaintext call in an encrypted room.
    expect(callKeyState(undefined, ready(), deriveKey)).toEqual({ kind: "refused" });
  });

  it("refuses while the device has no encryption at all", () => {
    for (const device of [
      { status: "off" },
      { status: "starting" },
      { status: "unavailable", reason: "wasm" },
    ] as const) {
      expect(callKeyState(channel(), { ...ready(), device }, deriveKey)).toEqual({
        kind: "refused",
      });
    }
  });

  it("refuses while the group is not usable, and joins once it is", () => {
    for (const status of ["opening", "waiting", "failed"] as const) {
      expect(callKeyState(channel(), ready({ status }), deriveKey)).toEqual({ kind: "refused" });
    }
    expect(callKeyState(channel(), ready(), deriveKey)).toMatchObject({ kind: "keyed" });
  });

  it("keys an incomplete group: it works, somebody is merely unreachable", () => {
    // The composer sends in this state, so a call is no different — the
    // members who are in the tree can all derive the key.
    const state = ready({ status: "incomplete", unreachableUserIds: ["absent"] });
    expect(callKeyState(channel(), state, deriveKey)).toMatchObject({ kind: "keyed" });
  });

  it("refuses rather than downgrades when no key can be derived", () => {
    // Evicted from the group is the real case: OpenMLS will not export a
    // secret from a group this device is no longer in.
    expect(callKeyState(channel(), ready(), deriveNothing)).toEqual({ kind: "refused" });
  });

  it("still keys a needs-attention conversation, and blocks publishing in it", () => {
    // Both halves are ADR 008's, transposed: deriving a key in order to
    // listen hands an attacker nothing, so the call is joined and heard;
    // sealing frames under a tree holding an unaccepted key is the act that
    // stops.
    const state: MlsState = {
      ...ready(),
      verification: {
        [CHANNEL_ID]: {
          changed: [{ userId: "peer", kind: "newDevice", added: ["k"], removed: [] }],
          uncoveredLeaves: 0,
        },
      },
    };
    expect(callKeyState(channel(), state, deriveKey)).toEqual({
      kind: "keyed",
      key: KEY,
      publishBlocked: true,
    });
  });

  it("blocks publishing on an unattributed leaf too", () => {
    const state: MlsState = {
      ...ready(),
      verification: { [CHANNEL_ID]: { changed: [], uncoveredLeaves: 1 } },
    };
    expect(callKeyState(channel(), state, deriveKey)).toMatchObject({ publishBlocked: true });
  });
});
