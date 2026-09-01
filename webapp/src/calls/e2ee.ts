import type { Channel } from "../chat/types";
import type { MediaKey, MlsState } from "../mls/types";
import { needsAttention } from "../mls/types";

/**
 * Two of ADR 009 decision 2's three join gates, as one pure function: gate 1
 * (room kind) and gate 3 (group state).
 *
 * They are here rather than inside the call hook because both are decisions
 * about *this client's own state* — which conversation it is entering, whether
 * it holds that conversation's group, whether a human still owes a decision
 * about somebody's keys — and neither may ever be answered by something the
 * server said. Keeping them in a function that takes no session, no socket and
 * no fetch is what makes that checkable.
 *
 * Gate 2, `isE2EESupported()`, is deliberately NOT here: it needs
 * `livekit-client`, which is a megabyte loaded on the first join, so it lives
 * behind that dynamic import in `livekit.ts` and refuses there.
 */

/**
 * What encryption says about a call in one conversation.
 *
 * There is no fourth case, and in particular no "encrypted if it works out":
 * an encrypted conversation whose key cannot be derived is `refused`, never
 * downgraded to `plain`. That is the media form of the null-refusal rail the
 * composer already runs on — a call that cannot be keyed must not happen
 * rather than happen in the clear.
 */
export type CallKeyState =
  /** Not an encrypted conversation. Media rides TLS/SRTP, as it always did. */
  | { kind: "plain" }
  | {
      kind: "keyed";
      key: MediaKey;
      /**
       * Somebody's device keys changed and a human has not decided yet, so
       * this device subscribes and decrypts but publishes nothing (ADR 009,
       * decision 3 — the same invariant `encrypt` enforces for messages).
       */
      publishBlocked: boolean;
    }
  /** An encrypted conversation this device cannot key. Nothing is joined. */
  | { kind: "refused" };

/**
 * Whether — and how — a call in `channel` may be keyed.
 *
 * `deriveKey` is `MlsService.mediaKey`, injected rather than imported so this
 * stays testable without a device, a keystore or wasm.
 *
 * An unknown channel refuses. It should be unreachable — a call is started
 * from a conversation that is on screen — but "the object was missing" is
 * precisely the shape of a bug that would otherwise place an unencrypted call
 * in an encrypted room, and there is no cost to closing it.
 */
export function callKeyState(
  channel: Channel | undefined,
  mls: MlsState,
  deriveKey: (channelId: string) => MediaKey | null,
): CallKeyState {
  if (channel === undefined) {
    return { kind: "refused" };
  }
  if (!channel.e2ee) {
    // Media E2EE rides the group: it is on exactly where message E2EE is on,
    // because the key exists exactly where the group exists (ADR 009,
    // decision 2). A plaintext conversation promises nothing about its call
    // that it does not already promise about its messages.
    return { kind: "plain" };
  }

  const state = mls.channels[channel.id];
  // `incomplete` is a working group with somebody unreachable in the
  // directory — the composer sends in it, and a call is no different.
  if (
    mls.device.status !== "ready" ||
    state === undefined ||
    (state.status !== "ready" && state.status !== "incomplete")
  ) {
    return { kind: "refused" };
  }

  const key = deriveKey(channel.id);
  if (key === null) {
    return { kind: "refused" };
  }
  return {
    kind: "keyed",
    key,
    publishBlocked: needsAttention(mls.verification[channel.id]),
  };
}
