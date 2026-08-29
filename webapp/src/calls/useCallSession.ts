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
 * Asks the server for a join ticket for whatever `join` was given.
 *
 * There are two ways to be entitled to a room and they are different requests:
 * a member asks the channel endpoint with a channel id, and a conference guest
 * asks the conference endpoint with the display name they typed. Everything
 * *after* the ticket — connect, publish, subscribe, discard — is identical, so
 * this is the one seam rather than a second copy of the session below.
 *
 * The status travels with the token because 503 (calls are not configured on
 * this instance at all) is a different sentence from "that did not work".
 */
export type MintTicket = (
  target: string,
) => Promise<{ token: string | undefined; status: number }>;

const mintChannelTicket: MintTicket = async (channelId) => {
  const { data, response } = await api.POST("/api/v1/channels/{channelId}/call/token", {
    params: { path: { channelId } },
  });
  return { token: data?.token, status: response.status };
};

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
  /**
   * Whatever `join` was called with — a channel id for a channel call, the
   * typed display name for a conference guest. Null while idle.
   */
  target: string | null;
  participants: MediaParticipant[];
  micEnabled: boolean;
  cameraEnabled: boolean;
  screenSharing: boolean;
  errorKey: CallErrorKey | null;
  join: (target: string) => void;
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
 *
 * `mintTicket` is the only thing a conference guest changes: a guest has no
 * session and no channel, so the ticket comes from the conference endpoint —
 * and everything after it is the same call this hook already ran.
 */
export function useCallSession(
  connect: MediaConnect = connectLiveKit,
  mintTicket: MintTicket = mintChannelTicket,
): CallSessionController {
  const [status, setStatus] = useState<CallStatus>("idle");
  const [target, setTarget] = useState<string | null>(null);
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
    (next: string) => {
      generation.current += 1;
      const mine = generation.current;
      discard(sessionRef.current);
      sessionRef.current = null;
      setStatus("connecting");
      setTarget(next);
      setParticipants([]);
      setErrorKey(null);

      void (async () => {
        try {
          const ticket = await mintTicket(next);
          if (ticket.token === undefined) {
            if (generation.current === mine) {
              setStatus("idle");
              setTarget(null);
              // 503 is the honest one: calls are not configured here at all,
              // which is a different sentence from "that did not work".
              setErrorKey(
                ticket.status === 503 ? "calls.error.unavailable" : "calls.error.failed",
              );
            }
            return;
          }

          const session = await connect(callSignalUrl(), ticket.token);
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
              setTarget(null);
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
            setTarget(null);
            setErrorKey("calls.error.failed");
          }
        }
      })();
    },
    [connect, discard, mintTicket],
  );

  const leave = useCallback(() => {
    generation.current += 1;
    discard(sessionRef.current);
    sessionRef.current = null;
    setStatus("idle");
    setTarget(null);
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
    target,
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
