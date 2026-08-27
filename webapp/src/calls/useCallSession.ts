import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "../api/client";
import { callSignalUrl } from "./media";
import type { MediaConnect, MediaParticipant, MediaSession } from "./media";

/**
 * The media client is loaded on the first join, not with the chat shell.
 * `livekit-client` is over a megabyte and most sessions never place a call, so
 * the import is deferred rather than paid for by everyone on every load.
 */
const connectLiveKit: MediaConnect = async (url, token) => {
  const { connectLiveKit: connectReal } = await import("./livekit");
  return connectReal(url, token);
};

export type CallStatus = "idle" | "connecting" | "connected";

/**
 * Keys rather than sentences, so a failure follows a language switch — the
 * shape `SsoCard` already uses.
 */
export type CallErrorKey =
  | "calls.error.unavailable"
  | "calls.error.failed"
  | "calls.error.device"
  | "calls.error.ended";

export interface CallSessionController {
  status: CallStatus;
  /** The channel this session belongs to; null while idle. */
  channelId: string | null;
  participants: MediaParticipant[];
  micEnabled: boolean;
  cameraEnabled: boolean;
  screenSharing: boolean;
  errorKey: CallErrorKey | null;
  join: (channelId: string) => void;
  leave: () => void;
  toggleMicrophone: () => void;
  toggleCamera: () => void;
  toggleScreenShare: () => void;
}

/**
 * One call at a time: ask for a ticket, connect, publish, subscribe.
 *
 * The ticket lives about two minutes and is never stored (openapi.yaml,
 * createCallToken) — it is asked for on the click that joins and dropped when
 * the connection is up. Nothing else about the media plane reaches the client:
 * the room name comes back with the ticket and the signal address is derived
 * from the page's own origin.
 */
export function useCallSession(connect: MediaConnect = connectLiveKit): CallSessionController {
  const [status, setStatus] = useState<CallStatus>("idle");
  const [channelId, setChannelId] = useState<string | null>(null);
  const [participants, setParticipants] = useState<MediaParticipant[]>([]);
  const [errorKey, setErrorKey] = useState<CallErrorKey | null>(null);

  const sessionRef = useRef<MediaSession | null>(null);
  /**
   * Bumped by every join, leave and unmount. An await that resolves after one
   * of those is answering a question nobody is asking any more, so it is
   * dropped — including the connection itself, which is disconnected rather
   * than left running behind a UI that has moved on.
   */
  const generation = useRef(0);

  const discard = useCallback((session: MediaSession | null) => {
    void session?.disconnect().catch((error: unknown) => {
      // Nothing to retry: the UI has already left. Losing the disconnect only
      // means the media server reaps the participant on its own timeout.
      console.warn("Leaving the call cleanly failed:", error);
    });
  }, []);

  const join = useCallback(
    (target: string) => {
      generation.current += 1;
      const mine = generation.current;
      discard(sessionRef.current);
      sessionRef.current = null;
      setStatus("connecting");
      setChannelId(target);
      setParticipants([]);
      setErrorKey(null);

      void (async () => {
        try {
          const { data, response } = await api.POST(
            "/api/v1/channels/{channelId}/call/token",
            { params: { path: { channelId: target } } },
          );
          if (data === undefined) {
            if (generation.current === mine) {
              setStatus("idle");
              setChannelId(null);
              // 503 is the honest one: calls are not configured here at all,
              // which is a different sentence from "that did not work".
              setErrorKey(
                response.status === 503 ? "calls.error.unavailable" : "calls.error.failed",
              );
            }
            return;
          }

          const session = await connect(callSignalUrl(), data.token);
          if (generation.current !== mine) {
            discard(session);
            return;
          }
          sessionRef.current = session;
          setStatus("connected");
          setParticipants(session.participants());

          session.subscribe((event) => {
            if (generation.current !== mine) {
              return;
            }
            if (event === "closed") {
              // Not this user's doing: a media-server restart ends every call
              // (ADR 005), and the screen has to say that rather than simply
              // emptying itself.
              sessionRef.current = null;
              setStatus("idle");
              setChannelId(null);
              setParticipants([]);
              setErrorKey("calls.error.ended");
              return;
            }
            setParticipants(session.participants());
          });

          // Publish the microphone. The camera stays off: no prejoin screen
          // exists yet to check it against (BRIEFS.md §5 `call-prejoin`), and
          // joining with a camera nobody meant to publish is the mistake that
          // screen exists to prevent.
          try {
            await session.setMicrophoneEnabled(true);
          } catch (error) {
            console.warn("Publishing the microphone failed:", error);
            if (generation.current === mine) {
              setErrorKey("calls.error.device");
            }
          }
          if (generation.current === mine) {
            setParticipants(session.participants());
          }
        } catch (error) {
          console.warn("Joining the call failed:", error);
          if (generation.current === mine) {
            setStatus("idle");
            setChannelId(null);
            setErrorKey("calls.error.failed");
          }
        }
      })();
    },
    [connect, discard],
  );

  const leave = useCallback(() => {
    generation.current += 1;
    discard(sessionRef.current);
    sessionRef.current = null;
    setStatus("idle");
    setChannelId(null);
    setParticipants([]);
    setErrorKey(null);
  }, [discard]);

  useEffect(
    () => () => {
      generation.current += 1;
      discard(sessionRef.current);
      sessionRef.current = null;
    },
    [discard],
  );

  const applyLocal = useCallback(
    (change: (session: MediaSession, enabled: boolean) => Promise<void>, enabled: boolean) => {
      const session = sessionRef.current;
      if (session === null) {
        return;
      }
      void (async () => {
        try {
          await change(session, enabled);
          setErrorKey(null);
        } catch (error) {
          // A refused permission or a machine with no camera. Said out loud,
          // because a control that appears to do nothing is worse.
          console.warn("Changing what this call publishes failed:", error);
          setErrorKey("calls.error.device");
        }
        setParticipants(session.participants());
      })();
    },
    [],
  );

  // Derived rather than stored: the media client owns what is published, and a
  // second copy of that here would be a copy that can disagree with the tiles.
  const local = participants.find((participant) => participant.isLocal);
  const micEnabled = local?.micEnabled ?? false;
  const cameraEnabled = local?.cameraEnabled ?? false;
  const screenSharing = local?.screenSharing ?? false;

  const toggleMicrophone = useCallback(() => {
    applyLocal((session, enabled) => session.setMicrophoneEnabled(enabled), !micEnabled);
  }, [applyLocal, micEnabled]);

  const toggleCamera = useCallback(() => {
    applyLocal((session, enabled) => session.setCameraEnabled(enabled), !cameraEnabled);
  }, [applyLocal, cameraEnabled]);

  const toggleScreenShare = useCallback(() => {
    applyLocal((session, enabled) => session.setScreenShareEnabled(enabled), !screenSharing);
  }, [applyLocal, screenSharing]);

  return {
    status,
    channelId,
    participants,
    micEnabled,
    cameraEnabled,
    screenSharing,
    errorKey,
    join,
    leave,
    toggleMicrophone,
    toggleCamera,
    toggleScreenShare,
  };
}
