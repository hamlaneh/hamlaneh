import { useTranslation } from "react-i18next";

import { isolateLtr } from "../../chat/format";
import type { Channel } from "../../chat/types";
import { HashIcon, LockIcon, UserPlusIcon } from "../icons";

interface EmptyChannelProps {
  channel: Channel;
  createdByYou: boolean;
  onInvite: () => void;
  onSetTopic: () => void;
}

/**
 * A freshly created channel: guidance instead of a void, with "Invite people"
 * as the primary action.
 *
 * The closing line is the design's own wording and is also the literal truth
 * of the Phase 1.2 model — visibility is membership for every kind, public
 * included, so nothing here promises a channel anyone can browse into.
 */
export function EmptyChannel({ channel, createdByYou, onInvite, onSetTopic }: EmptyChannelProps) {
  const { t } = useTranslation();
  const name = isolateLtr(`#${channel.slug ?? ""}`);

  return (
    <div className="hm-empty">
      <span className="hm-empty__glyph">
        {channel.kind === "private" ? (
          <LockIcon size={26} strokeWidth={1.6} />
        ) : (
          <HashIcon size={26} strokeWidth={1.6} />
        )}
      </span>
      <div className="hm-empty__text">
        <h2 className="hm-empty__title">{t("chat.empty.title", { channel: name })}</h2>
        <p className="hm-empty__body">
          {createdByYou ? t("chat.empty.bodyOwner") : t("chat.empty.body")}
        </p>
      </div>
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
    </div>
  );
}
