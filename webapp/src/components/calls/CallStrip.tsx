import { useTranslation } from "react-i18next";

import { formatCount } from "../../chat/format";
import type { ChannelCall } from "../../chat/types";
import { isolateAuto } from "../../i18n/bidi";

interface CallStripProps {
  call: ChannelCall | undefined;
  /** A join is in flight for this channel. */
  busy: boolean;
  onJoin: () => void;
}

/**
 * `call-banner`: a call is happening in this channel and the reader is not in
 * it — who is in it, and a way in. When there is no call it is only the way in,
 * because a room is created by the first person who joins (ADR 005) and so
 * starting and joining are the same act with two labels.
 *
 * UNDESIGNED SURFACE — `docs/design/STATUS.md` has the call view PENDING, so
 * this is plain semantic HTML with no styling beyond structure and none of the
 * delivered chat treatments borrowed. The identity treatment a tile or a
 * participant row should carry is named in BRIEFS.md §5 as the designer's call
 * between borrowing the sidebar's and restating it, so neither is assumed here:
 * participants are their names, as text.
 */
export function CallStrip({ call, busy, onJoin }: CallStripProps) {
  const { t, i18n } = useTranslation();
  const active = call?.active === true;
  const participants = call?.participants ?? [];

  return (
    <section aria-label={t("calls.strip.label")}>
      {active ? (
        <>
          <p>
            {t("calls.strip.inProgress", {
              count: formatCount(participants.length, i18n.language),
            })}
          </p>
          <ul>
            {participants.map((participant) => (
              <li key={participant.user.id}>
                {isolateAuto(participant.user.display_name)}
                {participant.screen_sharing === true ? ` ${t("calls.tile.sharing")}` : ""}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      <button type="button" onClick={onJoin} disabled={busy}>
        {busy ? t("calls.joining") : active ? t("calls.join") : t("calls.start")}
      </button>
    </section>
  );
}
