import { useTranslation } from "react-i18next";

import { formatCount } from "../../chat/format";
import type { ChannelCall } from "../../chat/types";
import { isolateAuto } from "../../i18n/bidi";
import { Avatar } from "../chat/Avatar";
import { UsersIcon } from "../icons";
import { PhoneOutgoingIcon } from "./icons";

/** This reader's own call, collapsed because they are reading elsewhere. */
export interface AwayCall {
  /** The conversation the call is in, already bidi-isolated. */
  title: string;
  onReturn: () => void;
}

interface CallStripProps {
  call: ChannelCall | undefined;
  /** A join is in flight for this channel. */
  busy: boolean;
  onJoin: () => void;
  /**
   * Set while this reader is in a call in another conversation. It takes the
   * slot over from the two channel states, because the way back to a call you
   * are in outranks an offer to start another one — and `useCallSession` holds
   * one call at a time, so accepting that offer would drop this one anyway.
   */
  away?: AwayCall | undefined;
}

/**
 * `call-banner`: one strip with two states, not two things — and a third that
 * is the same strip standing in for a call that is no longer on screen.
 *
 * A room is created by whoever joins first (ADR 005), so starting and joining
 * are the same act with two labels — and because the idle state is always
 * present, going live changes the strip's content and never the layout.
 * Nothing shoves the message list.
 *
 * It never steals focus, never animates position, carries no sound and never
 * becomes a dialog: entry is opacity and a 2px rise on the contents alone.
 * This is the only call surface a person meets without having chosen a call.
 */
export function CallStrip({ call, busy, onJoin, away }: CallStripProps) {
  const { t, i18n } = useTranslation();
  const active = call?.active === true;
  const participants = call?.participants ?? [];

  /* The third state. Navigating to another channel does not end the call: it
     collapses to this strip, which is the whole reason the call is not an
     overlay — a call somebody navigated away from must still be leavable, and
     the way back is the only route to Leave. */
  if (away !== undefined) {
    return (
      <section
        className="hm-call-strip"
        data-live="true"
        data-away="true"
        aria-label={t("calls.strip.label")}
      >
        <span className="hm-call-strip__dot" aria-hidden="true" />
        <p className="hm-call-strip__count">{t("calls.strip.away", { channel: away.title })}</p>
        <button type="button" className="hm-call-strip__action" onClick={away.onReturn}>
          <UsersIcon size={17} strokeWidth={1.85} />
          {t("calls.strip.return")}
        </button>
      </section>
    );
  }

  return (
    <section className="hm-call-strip" data-live={active} aria-label={t("calls.strip.label")}>
      {active ? (
        <>
          <span className="hm-call-strip__dot" aria-hidden="true" />
          {/* Decoration: every one of these people is named in the list
              below, so the stack is not read out twice. */}
          <span className="hm-call-strip__avatars" aria-hidden="true">
            {participants.map((participant) => (
              <Avatar
                key={participant.user.id}
                userId={participant.user.id}
                displayName={participant.user.display_name}
                size={26}
                typeSize={10}
              />
            ))}
          </span>
          <p className="hm-call-strip__count">
            {t("calls.strip.inProgress", {
              count: formatCount(participants.length, i18n.language),
            })}
          </p>
          <ul className="hm-call-strip__names">
            {participants.map((participant) => (
              <li key={participant.user.id} dir="auto">
                {isolateAuto(participant.user.display_name)}
                {participant.screen_sharing === true ? ` ${t("calls.tile.sharing")}` : ""}
              </li>
            ))}
          </ul>
        </>
      ) : (
        <>
          <PhoneOutgoingIcon size={17} strokeWidth={1.85} className="hm-call-strip__glyph" />
          <p className="hm-call-strip__count">{t("calls.strip.idle")}</p>
          <p className="hm-call-strip__hint">{t("calls.strip.idleHint")}</p>
        </>
      )}
      <button type="button" className="hm-call-strip__action" onClick={onJoin} disabled={busy}>
        {active ? <UsersIcon size={17} strokeWidth={1.85} /> : <PhoneOutgoingIcon size={17} strokeWidth={1.85} />}
        {busy ? t("calls.joining") : active ? t("calls.join") : t("calls.start")}
      </button>
    </section>
  );
}
