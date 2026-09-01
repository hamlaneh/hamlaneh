import type { MediaKey } from "../mls/types";

/**
 * The seam between the call UI and the media client.
 *
 * Everything above this interface is ordinary React and runs under jsdom;
 * everything below it opens real WebRTC, which jsdom has no answer for. So
 * `livekit.ts` is the only module that imports `livekit-client`, and tests
 * supply their own `MediaConnect` rather than trying to fake peer connections
 * — the same shape `RealtimeOptions.socketFactory` already uses for sockets.
 */

/** A published track, as much of it as a tile needs. */
export interface MediaTrack {
  attach: (element: HTMLMediaElement) => void;
  detach: (element: HTMLMediaElement) => void;
}

export interface MediaParticipant {
  /** Stable for the life of the connection — the tile's key. */
  identity: string;
  name: string;
  isLocal: boolean;
  speaking: boolean;
  micEnabled: boolean;
  cameraEnabled: boolean;
  screenSharing: boolean;
  /** Null whenever the camera is off, which is most tiles most of the time. */
  camera: MediaTrack | null;
  screen: MediaTrack | null;
  microphone: MediaTrack | null;
}

/**
 * "changed" — something a tile draws moved; re-read `participants()`.
 * "closed" — the room is gone. A media-server restart ends every call
 * (ADR 005), so "the call ended and it was not you" is a real state.
 */
export type MediaEvent = "changed" | "closed";

export interface MediaSession {
  participants: () => MediaParticipant[];
  /** Returns its own unsubscribe. */
  subscribe: (listener: (event: MediaEvent) => void) => () => void;
  setMicrophoneEnabled: (enabled: boolean) => Promise<void>;
  setCameraEnabled: (enabled: boolean) => Promise<void>;
  setScreenShareEnabled: (enabled: boolean) => Promise<void>;
  /**
   * Rotates the media key into the keyring slot its epoch names (ADR 009,
   * decision 3). A no-op on an unencrypted session.
   *
   * Filling a new slot rather than overwriting the old one is what lets
   * frames already in flight — sealed under the previous epoch — still
   * decode while everyone catches up.
   */
  setKey: (key: MediaKey) => Promise<void>;
  disconnect: () => Promise<void>;
}

/**
 * Connects to a room, encrypting its media when a key is given.
 *
 * The key is an argument rather than something set afterwards because the
 * media client has to be built encrypting: a session that connected first and
 * was keyed second would have a window in which it published in the clear,
 * which is the exact downgrade the phase exists to prevent.
 */
export type MediaConnect = (
  url: string,
  token: string,
  key?: MediaKey,
) => Promise<MediaSession>;

/**
 * This browser cannot do insertable streams, so it cannot join an encrypted
 * call at all (ADR 009, decision 2, gate 2).
 *
 * A distinct type because the honest answer here is a specific sentence: the
 * one thing that must never happen is falling back to an unencrypted join,
 * and an error the caller cannot tell apart from "the network failed" invites
 * exactly that.
 */
export class CallEncryptionUnsupportedError extends Error {
  constructor() {
    super("this browser cannot encrypt call media");
    this.name = "CallEncryptionUnsupportedError";
  }
}

/**
 * Where the media server's signal endpoint is.
 *
 * It is deliberately absent from the token response and always will be: Caddy
 * proxies `/rtc*` to the media server on the app's own origin (ADR 005, "the
 * signal path rides the app origin"), so a client derives the address instead
 * of being told it — exactly the way `realtimeUrl` derives the WebSocket
 * endpoint, same host, ws/wss chosen by the page's own scheme.
 *
 * This is the origin and nothing more: the media client appends `/rtc` itself.
 */
export function callSignalUrl(location: Location = window.location): string {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}`;
}
