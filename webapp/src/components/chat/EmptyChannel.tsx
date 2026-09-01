import { useTranslation } from "react-i18next";

import { isolateAuto, isolateLtr } from "../../i18n/bidi";
import type { Channel } from "../../chat/types";
import { HashIcon, LockIcon, UserPlusIcon } from "../icons";
import { Avatar } from "./Avatar";

interface EmptyChannelProps {
  channel: Channel;
  createdByYou: boolean;
  onInvite: () => void;
  onSetTopic: () => void;
}

/**
 * A freshly created conversation: guidance instead of a void.
 *
 * For a channel this is the delivered artboard — "Invite people" as the
 * primary action, and a closing line that is the design's own wording and
 * also the literal truth of the Phase 1.2 model: visibility is membership for
 * every kind, public included, so nothing here promises a channel anyone can
 * browse into.
 *
 * A direct message is not that screen, and rendering it as one produced three
 * false statements at once. A DM has no slug, so the title read "This is the
 * beginning of #". It has fixed membership and no topic, so both actions were
 * refused by the server with 400 — offered, and then rejected. And the
 * closing note said only the reader could see it until somebody was invited,
 * of a conversation whose other person was already in it.
 *
 * So the DM keeps the same shell and drops what does not apply: it is named
 * by its peer, whose avatar stands where the channel glyph does, and the two
 * refused actions are not offered rather than offered and refused. No
 * artboard draws this state — docs/design/STATUS.md carries the row.
 */
export function EmptyChannel({ channel, createdByYou, onInvite, onSetTopic }: EmptyChannelProps) {
  const { t } = useTranslation();
  const peer = channel.kind === "dm" ? channel.dm_peer : undefined;

  return (
    <div className="hm-empty">
      <span className="hm-empty__glyph">
        {peer !== undefined ? (
          // The same avatar the sidebar draws for this row, so the two name
          // the conversation the same way.
          <Avatar userId={peer.id} displayName={peer.display_name} size={26} typeSize={10} />
        ) : channel.kind === "private" ? (
          <LockIcon size={26} strokeWidth={1.6} />
        ) : (
          <HashIcon size={26} strokeWidth={1.6} />
        )}
      </span>
      <div className="hm-empty__text">
        <h2 className="hm-empty__title">
          {peer === undefined
            ? t("chat.empty.title", { channel: isolateLtr(`#${channel.slug ?? ""}`) })
            : // isolateAuto, not isolateLtr: a peer's name may be Persian or
              // Latin, and MessageList already isolates it first-strong.
              t("chat.empty.dmTitle", { name: isolateAuto(peer.display_name) })}
        </h2>
        <p className="hm-empty__body">
          {peer !== undefined
            ? t("chat.empty.dmBody")
            : createdByYou
              ? t("chat.empty.bodyOwner")
              : t("chat.empty.body")}
        </p>
      </div>
      {peer !== undefined ? null : (
        <>
          <div className="hm-empty__actions">
            <button
              type="button"
              className="hm-action-button hm-action-button--primary"
              onClick={onInvite}
            >
              <UserPlusIcon size={17} strokeWidth={1.85} />
              {t("chat.empty.invite")}
            </button>
            <button type="button" className="hm-action-button" onClick={onSetTopic}>
              {t("chat.empty.setTopic")}
            </button>
          </div>
          <p className="hm-empty__note">{t("chat.empty.onlyYou")}</p>
        </>
      )}
    </div>
  );
}
