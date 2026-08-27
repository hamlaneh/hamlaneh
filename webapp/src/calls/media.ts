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
  disconnect: () => Promise<void>;
}

export type MediaConnect = (url: string, token: string) => Promise<MediaSession>;

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
