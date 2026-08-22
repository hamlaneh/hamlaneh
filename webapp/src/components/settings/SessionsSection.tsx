import { useState } from "react";
import { useTranslation } from "react-i18next";

import { ConfirmDialog } from "./ConfirmDialog";
import { SessionRow } from "./SessionRow";
import { SettingsButton } from "./SettingsButton";
import type { useSessions } from "../../settings/useSessions";
import { NoticeBanner } from "../auth/NoticeBanner";
import { InfoIcon } from "../icons";

interface SessionsSectionProps {
  sessions: ReturnType<typeof useSessions>;
}

/**
 * "Active sessions", artboard `settings-sessions`: every live session family,
 * current device first, with the two revocations the contract offers.
 *
 * With nothing else signed in, "Sign out everywhere else" is absent rather
 * than disabled — `settings-components` §04 says so explicitly: there is
 * nothing to explain.
 */
export function SessionsSection({ sessions }: SessionsSectionProps) {
  const { t } = useTranslation();
  const [pendingRevoke, setPendingRevoke] = useState<string | null>(null);
  const [confirmingOthers, setConfirmingOthers] = useState(false);
  const [signingOutOthers, setSigningOutOthers] = useState(false);

  const others = sessions.sessions.filter((entry) => !entry.current);

  const signOutOthers = async () => {
    setSigningOutOthers(true);
    await sessions.revokeOthers();
    setSigningOutOthers(false);
    setConfirmingOthers(false);
  };

  const signOutOne = async (familyId: string) => {
    setPendingRevoke(familyId);
    await sessions.revoke(familyId);
    setPendingRevoke(null);
  };

  return (
    <>
      <div className="hm-settings__section-head">
        <div className="hm-settings__heading">
          <h3 className="hm-settings__title">{t("settings.sessions.title")}</h3>
          <p className="hm-settings__lede">{t("settings.sessions.lede")}</p>
        </div>
        {others.length === 0 ? null : (
          <SettingsButton
            label={t("settings.sessions.signOutOthers")}
            onClick={() => {
              setConfirmingOthers(true);
            }}
          />
        )}
      </div>

      <div className="hm-settings__scroll">
        {sessions.actionFailed ? (
          <NoticeBanner tone="danger" message={t("settings.sessions.error.revokeFailed")} />
        ) : null}

        {sessions.status === "loading" ? (
          <p className="hm-settings__note" role="status">
            {t("common.loading")}
          </p>
        ) : sessions.status === "failed" ? (
          <NoticeBanner tone="danger" message={t("settings.sessions.error.loadFailed")} />
        ) : (
          <ul className="hm-session-list" aria-label={t("settings.sessions.title")}>
            {sessions.sessions.map((session) => (
              <SessionRow
                key={session.family_id}
                session={session}
                busy={pendingRevoke === session.family_id}
                onSignOut={(familyId) => {
                  void signOutOne(familyId);
                }}
              />
            ))}
          </ul>
        )}

        {/* Locations are IP-estimated; the design labels them approximate
            rather than presenting a guess as a fact. */}
        <div className="hm-settings-note-banner">
          <InfoIcon size={17} strokeWidth={1.85} className="hm-settings-note-banner__icon" />
          <span>{t("settings.sessions.locationNote")}</span>
        </div>
      </div>

      {confirmingOthers ? (
        <ConfirmDialog
          title={t("settings.sessions.confirmOthers.title", { count: others.length })}
          body={t("settings.sessions.confirmOthers.body")}
          confirmLabel={t("settings.sessions.confirmOthers.confirm")}
          busyLabel={t("settings.sessions.signingOut")}
          cancelLabel={t("settings.cancel")}
          busy={signingOutOthers}
          onConfirm={() => {
            void signOutOthers();
          }}
          onCancel={() => {
            setConfirmingOthers(false);
          }}
        />
      ) : null}
    </>
  );
}
