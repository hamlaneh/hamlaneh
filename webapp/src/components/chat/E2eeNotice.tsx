import { useTranslation } from "react-i18next";

import type { Channel } from "../../chat/types";
import type { ChannelMlsState, MlsDeviceState } from "../../mls/types";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * No artboard draws anything about encryption, so per the UI pipeline
 * (CLAUDE.md -> docs/design/STATUS.md) this is functional plumbing only, and
 * every row it can draw is marked `awaiting-design` there.
 *
 * It carries the whole encryption story for the open conversation, because
 * splitting four honest sentences across four styled components before anyone
 * has drawn one of them would be inventing a design.
 *
 * No directional CSS anywhere in here: it is semantic HTML, so RTL is whatever
 * the document direction already says.
 */

interface E2eeNoticeProps {
  channel: Channel;
  device: MlsDeviceState;
  /** Undefined until this channel has been opened. */
  channelState: ChannelMlsState | undefined;
  /** Resolves a member id to a name, for the "cannot add yet" list. */
  resolveName: (userId: string) => string | null;
}

export function E2eeNotice({ channel, device, channelState, resolveName }: E2eeNoticeProps) {
  const { t } = useTranslation();
  if (!channel.e2ee) {
    return null;
  }

  return (
    <section className="hm-plumbing hm-plumbing--inline" aria-label={t("chat.e2ee.regionLabel")}>
      {/* The indicator itself: this conversation is encrypted, stated plainly
          and without any of the words engineering principle 4 forbids. */}
      <p>{t("chat.e2ee.indicator")}</p>

      {device.status === "unavailable" ? (
        <p role="alert">{t("chat.e2ee.unavailable")}</p>
      ) : device.status === "starting" || channelState === undefined ? (
        <p>{t("chat.e2ee.preparing")}</p>
      ) : channelState.status === "opening" ? (
        <p>{t("chat.e2ee.preparing")}</p>
      ) : channelState.status === "waiting" ? (
        <p>{t("chat.e2ee.waitingToJoin")}</p>
      ) : channelState.status === "failed" ? (
        <p role="alert">{t("chat.e2ee.channelFailed")}</p>
      ) : channelState.status === "incomplete" ? (
        <p role="status">
          {t("chat.e2ee.cannotAddYet", {
            names: channelState.unreachableUserIds
              .map((userId) => resolveName(userId) ?? t("chat.messages.unknownMember"))
              .join(t("chat.e2ee.nameSeparator")),
          })}
        </p>
      ) : null}
    </section>
  );
}
