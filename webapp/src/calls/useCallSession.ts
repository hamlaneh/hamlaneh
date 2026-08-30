import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "../api/client";
import type { CallKeyState } from "./e2ee";
import { CallEncryptionUnsupportedError, callSignalUrl } from "./media";
import type { MediaConnect, MediaParticipant, MediaSession } from "./media";

/**
 * The media client is loaded on the first join, not with the chat shell.
 * `livekit-client` is over a megabyte and most sessions never place a call, so
 * the import is deferred rather than paid for by everyone on every load.
 */
const connectLiveKit: MediaConnect = async (url, token, key) => {
  const { connectLiveKit: connectReal } = await import("./livekit");
  return connectReal(url, token, key);
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
  | "calls.error.ended"
  /** The conversation is encrypted and this device cannot key the call. */
  | "calls.error.encryption"
  /** This browser has no insertable streams, so it cannot encrypt media. */
  | "calls.error.encryptionUnsupported";

/**
 * Answers "how, if at all, may a call in this conversation be keyed", for
 * whatever target `join` was handed.
 *
 * A callback rather than a value because the answer depends on the target,
 * and the target is decided inside this hook — a caller cannot compute it in
 * advance without duplicating the state machine it is asking about.
 *
 * Absent means unencrypted: that is the conference-guest path, which has no
 * MLS group to key with and no member session to ask (ADR 006, decision 3).
 * The room kind therefore comes from the join path this hook was built on,
 * not from anything the server says, which is what makes the boundary
 * unflippable.
 */
export type CallKeyResolver = (target: string) => CallKeyState;

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
  /** This call's media is end-to-end encrypted. Fixed at join, never toggles. */
  encrypted: boolean;
  /**
   * This device has stopped publishing because somebody's keys changed and a
   * human has not decided yet. Reading and hearing the call carry on.
   */
  publishBlocked: boolean;
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
  resolveKey?: CallKeyResolver,
): CallSessionController {
  const [status, setStatus] = useState<CallStatus>("idle");
  const [target, setTarget] = useState<string | null>(null);
  const [participants, setParticipants] = useState<MediaParticipant[]>([]);
  const [errorKey, setErrorKey] = useState<CallErrorKey | null>(null);
  /**
   * Whether THIS call is encrypted, decided once at join.
   *
   * State rather than something re-derived per render because the room kind
   * is fixed at birth (ADR 006, decision 3): nothing mid-call may turn an
   * encrypted call into a plain one, and holding the answer is what stops a
   * momentarily-missing channel object from doing so by accident.
   */
  const [encrypted, setEncrypted] = useState(false);

  const sessionRef = useRef<MediaSession | null>(null);
  /**
   * Bumped by every join, leave and unmount. An await that resolves after one
   * of those is answering a question nobody is asking any more, so it is
   * dropped — including the connection itself, which is disconnected rather
   * than left running behind a UI that has moved on.
   */
  const generation = useRef(0);

  /**
   * What this device was publishing when the publish gate closed, or null
   * while it is open.
   *
   * Both halves matter: it is the flag that says publishing is currently
   * withheld, and it is what the two exits restore — somebody whose camera
   * was on when a warning appeared gets their camera back, and somebody whose
   * camera was off does not suddenly acquire one.
   */
  const blocked = useRef<{ microphone: boolean; camera: boolean; screen: boolean } | null>(null);

  /**
   * The epoch whose key the live session is already holding.
   *
   * Set at join — the first key travels with `connect`, so the session is
   * born keyed — and moved by the rotation effect. Without it that effect
   * would re-set the joining key once for nothing, and "one rotation per
   * epoch" would not be a property anything could check.
   */
  const keyedAt = useRef<number | null>(null);

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
      blocked.current = null;
      keyedAt.current = null;

      // Gates 1 and 3, before the ticket is even asked for: whether this
      // conversation is encrypted comes from the join path this hook was
      // built on (a member's channel, or a guest's conference), and whether
      // it can be keyed comes from this device's own MLS state. A refusal
      // here is the end of it — there is no unencrypted retry.
      const keys: CallKeyState = resolveKey?.(next) ?? { kind: "plain" };
      if (keys.kind === "refused") {
        setStatus("idle");
        setTarget(null);
        setParticipants([]);
        setEncrypted(false);
        setErrorKey("calls.error.encryption");
        return;
      }

      setStatus("connecting");
      setTarget(next);
      setParticipants([]);
      setEncrypted(keys.kind === "keyed");
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

          const session = await connect(
            callSignalUrl(),
            ticket.token,
            keys.kind === "keyed" ? keys.key : undefined,
          );
          if (generation.current !== mine) {
            discard(session);
            return;
          }
          sessionRef.current = session;
          keyedAt.current = keys.kind === "keyed" ? keys.key.epoch : null;
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
          //
          // Nothing is published at all while somebody's device keys are
          // unresolved: joining to listen is fine, sealing frames under a
          // tree holding an unaccepted key is not (ADR 009, decision 3). The
          // microphone is remembered as owed, and the effect below publishes
          // it the moment a human takes one of the two exits.
          if (keys.kind === "keyed" && keys.publishBlocked) {
            blocked.current = { microphone: true, camera: false, screen: false };
          } else {
            try {
              await session.setMicrophoneEnabled(true);
            } catch (error) {
              console.warn("Publishing the microphone failed:", error);
              if (generation.current === mine) {
                setErrorKey("calls.error.device");
              }
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
            setEncrypted(false);
            // A browser with no insertable streams gets its own sentence,
            // told apart from an ordinary failure: "try again" is the wrong
            // advice for something that will never work here, and nothing on
            // this path may read as an invitation to join unencrypted.
            setErrorKey(
              error instanceof CallEncryptionUnsupportedError
                ? "calls.error.encryptionUnsupported"
                : "calls.error.failed",
            );
          }
        }
      })();
    },
    [connect, discard, mintTicket, resolveKey],
  );

  const leave = useCallback(() => {
    generation.current += 1;
    discard(sessionRef.current);
    sessionRef.current = null;
    blocked.current = null;
    keyedAt.current = null;
    setStatus("idle");
    setTarget(null);
    setParticipants([]);
    setEncrypted(false);
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

  /*
   * ROTATION AND THE PUBLISH GATE (ADR 009, decision 3).
   *
   * Both read the same answer, re-resolved on every render: an encrypted call
   * asks its own conversation for the current epoch's key and whether a human
   * still owes a decision about somebody's keys. The resolver reads state this
   * device already holds — no network, no signalling — which is what lets both
   * of these be effects rather than a protocol.
   *
   * Only while `encrypted`: a conference guest and a plaintext channel have no
   * resolver worth calling and nothing to rotate.
   */
  const keys = encrypted && target !== null ? resolveKey?.(target) : undefined;
  const epoch = keys?.kind === "keyed" ? keys.key.epoch : null;
  /**
   * An encrypted call whose key has gone away — this device evicted from the
   * group, or the conversation gone — stops publishing too. It cannot derive
   * what the others are now using, so anything it sealed would be noise to
   * them and a frame nobody asked for to the server.
   */
  const publishBlocked =
    keys === undefined ? false : keys.kind !== "keyed" || keys.publishBlocked;

  /** The newest answer, for effects that must not re-run once per render. */
  const latestKeys = useRef(keys);
  useEffect(() => {
    latestKeys.current = keys;
  });

  // Rotation: one `setKey` per epoch, filling the slot the epoch names. The
  // old slot keeps its key, so frames still in flight decode; the sender's
  // outbound slot switches here, at the moment its own merge landed.
  useEffect(() => {
    const session = sessionRef.current;
    const key = latestKeys.current;
    if (session === null || epoch === null || key?.kind !== "keyed" || keyedAt.current === epoch) {
      return;
    }
    keyedAt.current = epoch;
    void session.setKey(key.key).catch((error: unknown) => {
      // Nothing to fall back to, and deliberately so: the old slot stays
      // current, so this device keeps sealing under an epoch its peers have
      // left — which they will not decode. Silence, not plaintext.
      console.warn("Rotating the call's media key failed:", error);
    });
  }, [epoch]);

  // The publish gate, and the two exits out of it. There is no third: nothing
  // here clears on a timer, on a dismissal, or per track.
  useEffect(() => {
    const session = sessionRef.current;
    if (session === null) {
      return;
    }
    if (publishBlocked) {
      if (blocked.current !== null) {
        return;
      }
      const local = session.participants().find((participant) => participant.isLocal);
      blocked.current = {
        microphone: local?.micEnabled ?? false,
        camera: local?.cameraEnabled ?? false,
        screen: local?.screenSharing ?? false,
      };
      void Promise.all([
        session.setMicrophoneEnabled(false),
        session.setCameraEnabled(false),
        session.setScreenShareEnabled(false),
      ])
        .then(() => {
          setParticipants(session.participants());
        })
        .catch((error: unknown) => {
          console.warn("Stopping this device's tracks failed:", error);
        });
      return;
    }

    const resume = blocked.current;
    blocked.current = null;
    if (resume === null) {
      return;
    }
    // What was on when it closed, and nothing more: a decision about somebody
    // else's keys must not turn a camera on for you.
    void Promise.all([
      resume.microphone ? session.setMicrophoneEnabled(true) : Promise.resolve(),
      resume.camera ? session.setCameraEnabled(true) : Promise.resolve(),
      resume.screen ? session.setScreenShareEnabled(true) : Promise.resolve(),
    ])
      .then(() => {
        setParticipants(session.participants());
      })
      .catch((error: unknown) => {
        console.warn("Publishing again after the warning failed:", error);
        setErrorKey("calls.error.device");
      });
  }, [publishBlocked, status]);

  const applyLocal = useCallback(
    (change: (session: MediaSession, enabled: boolean) => Promise<void>, enabled: boolean) => {
      const session = sessionRef.current;
      if (session === null || blocked.current !== null) {
        // Refused rather than merely hidden: the controls are disabled while
        // the warning stands, and a gate that lived only in the UI would be
        // one keyboard shortcut away from not being a gate.
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
    encrypted,
    publishBlocked,
    errorKey,
    join,
    leave,
    toggleMicrophone,
    toggleCamera,
    toggleScreenShare,
  };
}
