import { useEffect, useId, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { CallErrorKey, CallStatus } from "../../calls/useCallSession";
import type { MediaParticipant, MediaTrack } from "../../calls/media";
import { formatCount } from "../../chat/format";
import { isolateAuto } from "../../i18n/bidi";
import { Avatar } from "../chat/Avatar";
import { CircleAlertIcon, LoaderCircleIcon, MonitorIcon, UsersIcon, XIcon } from "../icons";
import { MicIcon, MicOffIcon, PhoneOffIcon, VideoIcon, VideoOffIcon } from "./icons";

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

/**
 * The grid stops growing at twelve cells: eleven tiles and a count.
 *
 * `call-states` -> "Grid at 2, 3, 5 and 12 — and beyond". The count cell is a
 * count and not a participant tile — see `MoreCell` for what that costs.
 */
const GRID_CELLS = 12;

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
      className="hm-call-tile"
      data-participant={participant.identity}
      data-speaking={participant.speaking}
      data-muted={!participant.micEnabled}
      data-camera={participant.cameraEnabled ? "on" : "off"}
      data-sharing={participant.screenSharing}
    >
      {/* The camera-off tile is the designed case, not the exception: in a
          real call most tiles are (CALLS_HANDOFF.md, "The tile is the whole
          design"). Identity is the sidebar's avatar treatment enlarged, so a
          person does not change shape between the sidebar and a call. */}
      {video === null ? (
        <div className="hm-call-tile__off">
          <Avatar userId={participant.identity} displayName={participant.name} size={54} typeSize={21} />
          {participant.cameraEnabled ? null : (
            <p className="hm-call-tile__off-label">
              <VideoOffIcon size={13} strokeWidth={2} />
              {t("calls.tile.cameraOff")}
            </p>
          )}
        </div>
      ) : (
        // Muted in every case: the audio element below is what carries sound.
        <video className="hm-call-tile__video" ref={videoRef} autoPlay playsInline muted />
      )}
      {audio === null ? null : <audio ref={audioRef} autoPlay />}

      {/* The plate: the name, always, and every state as a shape or a word as
          well as a colour, so it survives the smallest cell in a twelve-person
          grid. */}
      <div className="hm-call-tile__plate">
        <p className="hm-call-tile__name" dir="auto">
          {isolateAuto(participant.name)}
        </p>
        <p className="hm-call-tile__chips">
          {participant.speaking ? (
            <span className="hm-call-chip hm-call-chip--speaking">{t("calls.tile.speaking")}</span>
          ) : null}
          {participant.micEnabled ? null : (
            <span className="hm-call-chip hm-call-chip--muted">
              <MicOffIcon size={12} strokeWidth={2.1} />
              {t("calls.tile.muted")}
            </span>
          )}
          {participant.screenSharing ? (
            <span className="hm-call-chip">
              <MonitorIcon size={12} strokeWidth={2.2} />
              {t("calls.tile.sharing")}
            </span>
          ) : null}
        </p>
        {/* The level meter is the speaking state's motion, and it is drawn
            beside a ring on all four sides and a name at 600 — never the only
            carrier. Decorative: the chip beside it already says the word. */}
        {participant.speaking ? (
          <span className="hm-call-tile__level" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
        ) : null}
      </div>
    </li>
  );
}

/**
 * The twelfth cell once there are more than twelve people: a count, and the
 * names under it.
 *
 * `role="presentation"` is the whole point. The artboard says "It is a count,
 * not a participant tile: no avatar, no plate, not focusable as a person", so
 * it drops out of the participant list it sits inside — assistive technology
 * still reads the text, but the cell is not one of the list's items and
 * nothing in it can be tabbed to.
 */
function MoreCell({ hidden }: { hidden: MediaParticipant[] }) {
  const { t, i18n } = useTranslation();

  return (
    <li className="hm-call-tile hm-call-tile--count" role="presentation">
      <p className="hm-call-tile__count">
        {t("calls.view.more", { count: formatCount(hidden.length, i18n.language) })}
      </p>
      <p className="hm-call-tile__count-names" dir="auto">
        {hidden.map((participant) => isolateAuto(participant.name)).join(" · ")}
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
  /** This call's media is end-to-end encrypted (ADR 009). */
  encrypted?: boolean;
  /**
   * The mid-call publish warning, when one stands. A node rather than a flag
   * because its two exits are the verification ceremony's own, unchanged —
   * the caller passes the same component the composer is replaced by, and
   * this view stays ignorant of MLS (a conference draws it too).
   */
  publishWarning?: React.ReactNode;
  errorKey: CallErrorKey | null;
  onToggleMicrophone: () => void;
  onToggleCamera: () => void;
  onToggleScreenShare: () => void;
  onLeave: () => void;
}

/**
 * `call-grid` and `call-screenshare`: the tiles and the control bar.
 *
 * The stage is dark in both themes. `call-states` draws every tile, control
 * and call-level state twice, and the light column carries the same ink as the
 * dark one — what changes between them is the page's own brand, danger and
 * on-brand tokens, which is why those are the only colours here that come from
 * `tokens.css` unpinned.
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
  encrypted = false,
  publishWarning = null,
  errorKey,
  onToggleMicrophone,
  onToggleCamera,
  onToggleScreenShare,
  onLeave,
}: CallViewProps) {
  const { t, i18n } = useTranslation();
  const headingId = useId();

  const connected = status === "connected";
  /* The three publish toggles are dead while the warning stands. Not merely
     cosmetic: the session refuses them too (useCallSession), so this is the
     control agreeing with the gate rather than being it — a button that
     silently does nothing is the thing this file already refuses elsewhere. */
  const canPublish = connected && publishWarning === null;
  // Beyond twelve the grid stops growing rather than shrinking everybody.
  const overflowing = participants.length > GRID_CELLS;
  const shown = overflowing ? participants.slice(0, GRID_CELLS - 1) : participants;
  const hidden = overflowing ? participants.slice(GRID_CELLS - 1) : [];

  return (
    <section className="hm-call" aria-labelledby={headingId} data-status={status}>
      <div className="hm-call__header">
        <span className="hm-call__live" aria-hidden="true" />
        <h2 className="hm-call__heading" id={headingId}>
          {t("calls.view.heading", { channel: channelTitle })}
        </h2>
      </div>

      {/* UNDESIGNED — `call-encrypted-indicator` is PENDING in
          docs/design/STATUS.md, so this is plain semantic HTML with no styling
          beyond structure, per CLAUDE.md's UI pipeline.

          What must survive the reskin is not the markup but the pairing: the
          claim about content and the disclaimer about metadata are one
          statement, and the artboard may not keep the first without the
          second. A true sentence about what the server cannot read, standing
          alone, is read by most people as a claim about who it cannot see —
          which is the overclaim §2.4 forbids and the SFU cannot honour. */}
      {encrypted ? (
        <div className="hm-plumbing hm-plumbing--inline" aria-label={t("calls.encrypted.label")}>
          <p>{t("calls.encrypted.label")}</p>
          <p>{t("calls.encrypted.detail")}</p>
          <p>{t("calls.encrypted.metadata")}</p>
        </div>
      ) : null}

      {/* The mid-call form of the composer's warning, drawn above the stage so
          somebody whose camera just went dark reads why before they start
          testing hardware (BRIEFS.md §Media E2EE). Two exits, no third. */}
      {publishWarning}

      {/* `call-screenshare`: persistent while sharing, and never a toast. "You
          are sharing" is the state people forget and then leak a window they
          meant to close, so it stays on screen for as long as it is true and
          says what the exposure actually is.

          role="status" rather than an alert: it is inserted the moment sharing
          starts, so a reader hears it once, politely, without the call being
          interrupted to say it.

          Its Stop control duplicates the share toggle in the bar deliberately —
          somebody who has forgotten they are sharing is not looking at the
          control bar, and the warning has to carry its own way out. */}
      {screenSharing ? (
        <div className="hm-call-share" role="status">
          <MonitorIcon size={17} strokeWidth={1.85} className="hm-call-share__glyph" />
          <p className="hm-call-share__warning">{t("calls.share.warning")}</p>
          <p className="hm-call-share__detail">{t("calls.share.detail")}</p>
          <button type="button" className="hm-call-share__stop" onClick={onToggleScreenShare}>
            <XIcon size={16} strokeWidth={2.2} />
            {t("calls.share.stop")}
          </button>
        </div>
      ) : null}

      <div className="hm-call__stage">
        {status === "connecting" ? (
          <p className="hm-call__status">
            <LoaderCircleIcon size={20} className="hm-spin" />
            {t("calls.connecting")}
          </p>
        ) : null}
        {errorKey === null ? null : (
          <p className="hm-call__status hm-call__status--alert" role="alert">
            <CircleAlertIcon size={24} />
            {t(errorKey)}
          </p>
        )}

        {connected ? (
          <ul className="hm-call__grid" aria-label={t("calls.view.participants")}>
            {shown.map((participant) => (
              <CallTile key={participant.identity} participant={participant} />
            ))}
            {hidden.length === 0 ? null : <MoreCell hidden={hidden} />}
          </ul>
        ) : null}
      </div>

      {/* With no session left, the one control is the way to acknowledge a call
          that ended without this person's help. */}
      {status === "idle" ? (
        <div className="hm-call__actions">
          <button type="button" className="hm-call-button" onClick={onLeave}>
            {t("calls.dismiss")}
          </button>
        </div>
      ) : (
        <div className="hm-call__bar" role="group" aria-label={t("calls.view.controls")}>
          {/* The three toggles cluster at the logical inline-start; Leave sits
              at the opposite end behind a gap and its own rule, so a hand
              reaching for mute cannot arrive at it. The distance is logical and
              survives the Persian mirror, and the DOM order is what carries it.

              aria-pressed rather than a changing label: the control is one
              thing that is on or off, and a reader should hear which. Present
              and disabled while connecting, so the bar never arrives late. */}
          <div className="hm-call__toggles">
            <button
              type="button"
              className="hm-call-control"
              aria-label={t("calls.control.microphone")}
              aria-pressed={micEnabled}
              disabled={!canPublish}
              onClick={onToggleMicrophone}
            >
              {micEnabled ? <MicIcon size={19} strokeWidth={1.85} /> : <MicOffIcon size={19} strokeWidth={1.85} />}
            </button>
            <button
              type="button"
              className="hm-call-control"
              aria-label={t("calls.control.camera")}
              aria-pressed={cameraEnabled}
              disabled={!canPublish}
              onClick={onToggleCamera}
            >
              {cameraEnabled ? (
                <VideoIcon size={19} strokeWidth={1.85} />
              ) : (
                <VideoOffIcon size={19} strokeWidth={1.85} />
              )}
            </button>
            {/* The one toggle whose "on" is brand-filled: it is the state
                people forget they are in (call-states -> 03). */}
            <button
              type="button"
              className="hm-call-control hm-call-control--share"
              aria-label={t("calls.control.screenShare")}
              aria-pressed={screenSharing}
              disabled={!canPublish}
              onClick={onToggleScreenShare}
            >
              <MonitorIcon size={19} strokeWidth={1.85} />
            </button>
          </div>

          <span className="hm-call__rule" aria-hidden="true" />

          <p className="hm-call__count">
            <UsersIcon size={18} strokeWidth={1.85} />
            {/* ASCII digits in Persian too, like every app-generated number. */}
            <span dir="ltr">{formatCount(participants.length, i18n.language)}</span>
            <span className="hm-visually-hidden">{t("calls.view.participants")}</span>
          </p>

          <span className="hm-call__rule hm-call__rule--leave" aria-hidden="true" />

          {/* Leave ends the call for the person clicking and for nobody else.
              There is no end-for-everyone anywhere in this set. */}
          <button type="button" className="hm-call-leave" onClick={onLeave}>
            <PhoneOffIcon size={18} strokeWidth={1.85} />
            {t("calls.control.leave")}
          </button>
        </div>
      )}
    </section>
  );
}
