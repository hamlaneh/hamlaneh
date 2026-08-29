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

/* ── verification (ADR 008) ──────────────────────────────────────────── */

/**
 * How a key set came to be accepted, and the two are not interchangeable.
 *
 * `pinned` is real and weaker: this device recorded the set on first sight, or
 * a human pressed "I checked" without running a ceremony. `verified` means two
 * humans compared safety numbers out of band and they matched. Rendering them
 * alike would make an unceremonied acceptance look like a proof, which is why
 * the distinction is carried in the data and not only in the copy.
 */
export type VerificationLevel = "pinned" | "verified";

/**
 * One person's accepted device keys — the record the whole slice turns on.
 *
 * Instance-global rather than per-channel: trust is about people, not rooms.
 * The full key set is kept rather than a hash of it, because the UI has to be
 * able to say *which* device is new.
 */
export interface VerificationRecord {
  userId: string;
  /** The accepted signature public keys, base64, as the directory carries them. */
  keys: string[];
  level: VerificationLevel;
  /** When this exact set was accepted, epoch milliseconds. */
  at: number;
}

export type VerificationRecords = Record<string, VerificationRecord>;

/** What changed about a person since this device accepted their keys. */
export type KeyChangeKind =
  /** Keys were added and none were withdrawn — a new device appeared. */
  | "newDevice"
  /** Accepted keys are gone from the current set — a key was replaced. */
  | "replacedKey";

export interface ChangedMember {
  userId: string;
  kind: KeyChangeKind;
  /** Keys in the current set that were never accepted. */
  added: string[];
  /** Accepted keys the directory no longer lists. */
  removed: string[];
}

/**
 * A conversation's verification state, held ALONGSIDE its availability state.
 *
 * A channel can be `ready` and still blocked for sending: availability is
 * "does this device hold a usable group", and this is "does the tree hold only
 * keys this device has accepted". They answer different questions and a single
 * status field would force one to hide the other.
 */
export interface ChannelVerification {
  /** Members whose current key set differs from what this device accepted. */
  changed: ChangedMember[];
  /**
   * Leaves in the tree the directory attributes to nobody currently in the
   * channel. They block sending and heal themselves: the next reconcile's
   * allow-list sweep evicts exactly these (ADR 007), so this is the pre-sweep
   * window stated as a non-sending window.
   */
  uncoveredLeaves: number;
}

/** Nothing changed, and nothing unattributed — the ordinary state. */
export const clearVerification: ChannelVerification = { changed: [], uncoveredLeaves: 0 };

/** Whether this conversation refuses to encrypt until a human decides. */
export function needsAttention(verification: ChannelVerification | undefined): boolean {
  if (verification === undefined) {
    return false;
  }
  return verification.changed.length > 0 || verification.uncoveredLeaves > 0;
}

/**
 * Keys the directory lists under this account that this device has not
 * accepted — the loudest prompt in the slice (ADR 008, decision 2).
 *
 * Verification ships before device linking, so a second key under your own id
 * is either your other browser profile or an attack, and no software on this
 * device can tell the two apart. It is never pinned silently: the key joins
 * the accepted own-set only on an explicit yes, and declining leaves every
 * ceremony failing until the key is gone, which is the correct outcome.
 */
export interface OwnDevicePrompt {
  /** Unaccepted keys the directory lists for this account, base64. */
  keys: string[];
}

export interface MlsState {
  device: MlsDeviceState;
  channels: Record<string, ChannelMlsState>;
  /**
   * Plaintext per message id, or null for a message this device cannot open.
   * Null is a real MLS condition — a message from before this device joined
   * the group — and renders as its own state, never as an empty bubble.
   */
  decrypted: Record<string, string | null>;
  /** Per channel, computed at every reconcile and re-checked at every send. */
  verification: Record<string, ChannelVerification>;
  /** What this device has accepted, per person — the badges and the sheet. */
  records: VerificationRecords;
  /** Null unless this account's own directory set holds an unaccepted key. */
  ownDevices: OwnDevicePrompt | null;
}

export const initialMlsState: MlsState = {
  device: { status: "off" },
  channels: {},
  decrypted: {},
  verification: {},
  records: {},
  ownDevices: null,
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
