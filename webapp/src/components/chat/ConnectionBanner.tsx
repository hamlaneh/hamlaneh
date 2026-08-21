import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";

import { formatCount, formatTime } from "../../chat/format";
import type { ConnectionState } from "../../chat/types";
import { CircleCheckIcon, LoaderCircleIcon, WifiOffIcon } from "../icons";

/** "Back online" auto-dismisses after 3 s; the reconnecting banner never does. */
const BACK_ONLINE_MS = 3000;

interface ConnectionBannerProps {
  connection: ConnectionState;
  /** The connection came back and the success banner has not expired yet. */
  justReconnected: boolean;
  onSettled: () => void;
}

/**
 * Connection state, announced through `role="status"`. It never takes focus —
 * the composer keeps it — and it never covers the conversation.
 */
export function ConnectionBanner({
  connection,
  justReconnected,
  onSettled,
}: ConnectionBannerProps) {
  const { t, i18n } = useTranslation();

  useEffect(() => {
    if (!justReconnected) {
      return undefined;
    }
    const timer = setTimeout(onSettled, BACK_ONLINE_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [justReconnected, onSettled]);

  let content: ReactNode = null;

  if (connection.status === "reconnecting") {
    content = (
      <span className="hm-connection__pill" data-tone="warning">
        <LoaderCircleIcon size={17} strokeWidth={2} className="hm-connection__icon hm-spin" />
        <span>{t("chat.connection.reconnecting")}</span>
        {connection.lastConnectedAt === null ? null : (
          <span className="hm-connection__detail">
            {t("chat.connection.lastConnected", {
              time: formatTime(connection.lastConnectedAt, i18n.language),
            })}
          </span>
        )}
      </span>
    );
  } else if (connection.status === "offline") {
    content = (
      <span className="hm-connection__pill" data-tone="danger">
        <WifiOffIcon size={16} strokeWidth={1.85} className="hm-connection__icon" />
        <span>
          {t("chat.connection.offline", {
            seconds: formatCount(connection.retryInSeconds, i18n.language),
          })}
        </span>
      </span>
    );
  } else if (justReconnected) {
    content = (
      <span className="hm-connection__pill" data-tone="success">
        <CircleCheckIcon size={16} strokeWidth={1.85} className="hm-connection__icon" />
        <span>{t("chat.connection.backOnline")}</span>
      </span>
    );
  }

  return (
    <div className="hm-connection" role="status">
      {content}
    </div>
  );
}
