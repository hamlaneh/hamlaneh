import { useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { CallErrorKey, CallStatus } from "../../calls/useCallSession";
import type { MediaParticipant, MediaTrack } from "../../calls/media";
import { isolateAuto } from "../../i18n/bidi";

/**
 * Attaches a track to the element that draws it for as long as both exist.
 *
 * The media client owns the `MediaStream`; a React ref is only where it is
 * pointed. Detaching on the way out is what keeps a camera that was turned off
 * from leaving its last frame on screen.
 */
function useAttached(track: MediaTrack | null, ref: React.RefObject<HTMLMediaElement | null>) {
  useEffect(() => {
    const element = ref.current;
    if (track === null || element === null) {
      return undefined;
    }
    track.attach(element);
    return () => {
      track.detach(element);
    };
  }, [track, ref]);
}

function CallTile({ participant }: { participant: MediaParticipant }) {
  const { t } = useTranslation();
  const videoRef = useRef<HTMLVideoElement>(null);
  const audioRef = useRef<HTMLAudioElement>(null);

  // A shared screen is the thing worth looking at when there is one.
  const video = participant.screen ?? participant.camera;
  // Never the local participant's own microphone: that is a feedback loop.
  const audio = participant.isLocal ? null : participant.microphone;

  useAttached(video, videoRef);
  useAttached(audio, audioRef);

  return (
    <li
      data-participant={participant.identity}
      data-speaking={participant.speaking}
      data-muted={!participant.micEnabled}
      data-camera={participant.cameraEnabled ? "on" : "off"}
    >
      <p>{isolateAuto(participant.name)}</p>
      {/* Most tiles in a real call have no video at all (BRIEFS.md §5), so the
          name above is the tile, and the element below is the exception. Muted
          in every case: the audio element is what carries sound. */}
      {video === null ? null : <video ref={videoRef} autoPlay playsInline muted />}
      {audio === null ? null : <audio ref={audioRef} autoPlay />}
      <p>
        {participant.speaking ? <span>{t("calls.tile.speaking")}</span> : null}
        {participant.micEnabled ? null : <span>{t("calls.tile.muted")}</span>}
        {participant.cameraEnabled ? null : <span>{t("calls.tile.cameraOff")}</span>}
        {participant.screenSharing ? <span>{t("calls.tile.sharing")}</span> : null}
      </p>
    </li>
  );
}

interface CallViewProps {
  /** The conversation this call belongs to, already bidi-isolated. */
  channelTitle: string;
  status: CallStatus;
  participants: MediaParticipant[];
  micEnabled: boolean;
  cameraEnabled: boolean;
  screenSharing: boolean;
  errorKey: CallErrorKey | null;
  onToggleMicrophone: () => void;
  onToggleCamera: () => void;
  onToggleScreenShare: () => void;
  onLeave: () => void;
}

/**
 * `call-grid`: the tiles and the control bar.
 *
 * UNDESIGNED SURFACE — `docs/design/STATUS.md` has the call view PENDING, so
 * this is plain semantic HTML with no styling beyond structure. Per-tile state
 * is text plus a `data-` attribute rather than an invented visual treatment:
 * the artboard decides how "muted" and "speaking" survive the smallest tile,
 * and text is the only thing that says it without deciding.
 *
 * Not here because ADR 005 does not build it: raise-hand, reactions, moderator
 * controls, an in-call chat panel, a recording indicator.
 */
export function CallView({
  channelTitle,
  status,
  participants,
  micEnabled,
  cameraEnabled,
  screenSharing,
  errorKey,
  onToggleMicrophone,
  onToggleCamera,
  onToggleScreenShare,
  onLeave,
}: CallViewProps) {
  const { t } = useTranslation();
  const headingId = useId();

  const connected = status === "connected";

  return (
    <section aria-labelledby={headingId}>
      <h2 id={headingId}>{t("calls.view.heading", { channel: channelTitle })}</h2>
      {status === "connecting" ? <p>{t("calls.connecting")}</p> : null}
      {errorKey === null ? null : <p role="alert">{t(errorKey)}</p>}

      {connected ? (
        <>
          <ul aria-label={t("calls.view.participants")}>
            {participants.map((participant) => (
              <CallTile key={participant.identity} participant={participant} />
            ))}
          </ul>

          {/* aria-pressed rather than a changing label: the control is one
              thing that is on or off, and a reader should hear which. */}
          <button type="button" aria-pressed={micEnabled} onClick={onToggleMicrophone}>
            {t("calls.control.microphone")}
          </button>
          <button type="button" aria-pressed={cameraEnabled} onClick={onToggleCamera}>
            {t("calls.control.camera")}
          </button>
          <button type="button" aria-pressed={screenSharing} onClick={onToggleScreenShare}>
            {t("calls.control.screenShare")}
          </button>
        </>
      ) : null}

      {/* Last, and named for what it does: it ends the call for this person and
          for nobody else (BRIEFS.md §5). Where it sits so a mis-click cannot
          reach it is the artboard's answer, not this file's.
          With no session left it is the way to acknowledge a call that ended
          without this person's help, which is the same click. */}
      <button type="button" onClick={onLeave}>
        {status === "idle" ? t("calls.dismiss") : t("calls.control.leave")}
      </button>
    </section>
  );
}
