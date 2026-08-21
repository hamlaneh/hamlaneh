import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { Channel } from "../../../chat/types";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup draws a "Channel actions" control in the header but no menu
 * behind it. The two actions the Phase 1.2 contract supports are here: invite
 * somebody, and set the topic (a direct message has neither).
 */

interface ChannelMenuProps {
  channel: Channel;
  onInvite: () => void;
  onSetTopic: (topic: string) => Promise<boolean>;
  onClose: () => void;
}

export function ChannelMenu({ channel, onInvite, onSetTopic, onClose }: ChannelMenuProps) {
  const { t } = useTranslation();
  const [topic, setTopic] = useState(channel.topic);

  if (channel.kind === "dm") {
    return (
      <section
        className="hm-plumbing"
        role="dialog"
        aria-modal="false"
        aria-label={t("chat.header.channelActions")}
      >
        <p>{t("chat.channelMenu.dmNote")}</p>
        <button type="button" onClick={onClose}>
          {t("chat.common.close")}
        </button>
      </section>
    );
  }

  return (
    <section
      className="hm-plumbing"
      role="dialog"
      aria-modal="false"
      aria-label={t("chat.header.channelActions")}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <p>
        <button type="button" onClick={onInvite}>
          {t("chat.empty.invite")}
        </button>
      </p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void onSetTopic(topic).then((ok) => {
            if (ok) {
              onClose();
            }
          });
        }}
      >
        <p>
          <label>
            {t("chat.channelMenu.topicLabel")}
            <input
              name="topic"
              value={topic}
              onChange={(event) => {
                setTopic(event.target.value);
              }}
            />
          </label>
        </p>
        <p>
          <button type="submit">{t("chat.channelMenu.saveTopic")}</button>
          <button type="button" onClick={onClose}>
            {t("chat.common.close")}
          </button>
        </p>
      </form>
    </section>
  );
}
