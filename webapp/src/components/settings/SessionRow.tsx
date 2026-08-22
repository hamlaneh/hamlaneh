import { useTranslation } from "react-i18next";

import { SettingsButton } from "./SettingsButton";
import { describeDevice } from "../../settings/device";
import { lastActiveLabel } from "../../settings/sessionTime";
import type { SessionFamily } from "../../settings/useSessions";
import { AppWindowIcon, MonitorIcon, SmartphoneIcon } from "../icons";

const DEVICE_ICON = {
  monitor: MonitorIcon,
  smartphone: SmartphoneIcon,
  appWindow: AppWindowIcon,
} as const;

interface SessionRowProps {
  session: SessionFamily;
  busy: boolean;
  onSignOut: (familyId: string) => void;
}

/**
 * One signed-in device, per `settings-sessions`.
 *
 * Three fields of the contract are optional in ways this row has to survive:
 * `location` is absent until a geolocation source exists (drawn as "Unknown
 * location"), `ip` may be absent, and `user_agent` can be an empty string when
 * a client sent no header — which is why the device label always resolves to
 * something rather than to nothing.
 *
 * The current device has no sign-out control of its own: signing this device
 * out is logout, and the design gives the row the word "Current" instead.
 */
export function SessionRow({ session, busy, onSignOut }: SessionRowProps) {
  const { t, i18n } = useTranslation();
  const device = describeDevice(session.user_agent);
  const Icon = DEVICE_ICON[device.icon];
  const activity = lastActiveLabel(session.last_active_at, i18n.language);

  // "Firefox 141" for a browser, "Tauri 2.0 on macOS" for the desktop app, and
  // nothing at all when the agent said nothing usable.
  const client =
    device.runtime === null
      ? device.browser
      : device.runtime.platform === ""
        ? device.runtime.name
        : t("settings.sessions.runtimeOn", {
            app: device.runtime.name,
            platform: device.runtime.platform,
          });

  const activityText =
    activity.kind === "now"
      ? t("settings.sessions.activeNow")
      : activity.kind === "active"
        ? t("settings.sessions.activeWhen", { when: activity.when })
        : activity.kind === "last"
          ? t("settings.sessions.lastActiveWhen", { when: activity.when })
          : t("settings.sessions.lastActiveUnknown");

  return (
    <li className="hm-session" data-current={session.current}>
      <span className="hm-session__icon">
        <Icon size={20} />
      </span>
      <div className="hm-session__identity">
        <div className="hm-session__title-row">
          <span className="hm-session__title">
            {t(`settings.sessions.device.${device.titleKey}`)}
          </span>
          {session.current ? (
            <span className="hm-session__badge">
              <span className="hm-session__badge-dot" />
              {t("settings.sessions.thisDevice")}
            </span>
          ) : null}
        </div>
        <span className="hm-session__meta">
          {client === null ? null : <>{client} · </>}
          {session.location ?? t("settings.sessions.unknownLocation")}
          {session.ip === null || session.ip === undefined ? null : (
            <>
              {" · "}
              {/* An address is an LTR run whatever the page direction. */}
              <span className="hm-session__ip" dir="ltr">
                {session.ip}
              </span>
            </>
          )}
        </span>
      </div>
      <span className="hm-session__activity">{activityText}</span>
      <span className="hm-session__action">
        {session.current ? (
          // Paired with the badge on purpose: the state is never badge-only.
          <span className="hm-session__current">{t("settings.sessions.current")}</span>
        ) : (
          <SettingsButton
            label={t("settings.sessions.signOut")}
            busyLabel={t("settings.sessions.signingOut")}
            busy={busy}
            tone="danger"
            size="sm"
            onClick={() => {
              onSignOut(session.family_id);
            }}
          />
        )}
      </span>
    </li>
  );
}
