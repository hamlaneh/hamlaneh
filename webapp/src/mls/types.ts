/** The states the E2EE surfaces render. Deliberately small and honest. */

/**
 * Why this session has no MLS. Every reason is rendered as the same
 * "encryption is unavailable" state; they are separate so the console and
 * future telemetry can tell them apart.
 */
export type MlsUnavailableReason =
  /** No IndexedDB, or no WebCrypto to wrap what would go in it. */
  | "keystore"
  /** The wasm wrapper could not be loaded or instantiated. */
  | "wasm"
  /** The device could not be registered with the directory. */
  | "server";

export type MlsDeviceState =
  /** Nothing has asked for encryption yet — the plaintext-only session. */
  | { status: "off" }
  | { status: "starting" }
  | { status: "ready" }
  | { status: "unavailable"; reason: MlsUnavailableReason };

export type ChannelMlsState =
  /** Fetching or creating the group. */
  | { status: "opening" }
  /**
   * A group exists and this device is not in it. Not an error: whoever
   * created the group is adding us, and a Welcome is on its way.
   */
  | { status: "waiting" }
  | { status: "ready" }
  /**
   * Ready, but some members could not be added — they have no MLS device yet,
   * or their key-package pool is empty. They cannot read this conversation
   * until they come back, and the screen says so rather than pretending.
   */
  | { status: "incomplete"; unreachableUserIds: string[] }
  /** The group could not be reached or advanced. Nothing sends here. */
  | { status: "failed" };

export interface MlsState {
  device: MlsDeviceState;
  channels: Record<string, ChannelMlsState>;
  /**
   * Plaintext per message id, or null for a message this device cannot open.
   * Null is a real MLS condition — a message from before this device joined
   * the group — and renders as its own state, never as an empty bubble.
   */
  decrypted: Record<string, string | null>;
}

export const initialMlsState: MlsState = {
  device: { status: "off" },
  channels: {},
  decrypted: {},
};

/** What a bubble draws: plaintext, decrypted text, or the honest refusal. */
export type MessageBody =
  | { kind: "plaintext"; text: string }
  /** Decrypted here, in this browser. Rendered exactly like plaintext. */
  | { kind: "decrypted"; text: string }
  /** Encrypted, and this device holds no key that opens it. */
  | { kind: "undecryptable" }
  /** Encrypted, and the decryption has not come back yet. */
  | { kind: "pending" };
