import type { RefObject } from "react";
import { useTranslation } from "react-i18next";

import { SavedMark } from "./SavedMark";
import { SettingsButton } from "./SettingsButton";
import { SsoCard } from "./SsoCard";
import { TwoFactorCard } from "./TwoFactorCard";
import { describeDevice } from "../../settings/device";
import type { useChangePassword } from "../../auth/useChangePassword";
import type { useSessions } from "../../settings/useSessions";
import type { useTotpStatus } from "../../settings/useTotpStatus";
import { NoticeBanner } from "../auth/NoticeBanner";
import { PasswordField } from "../auth/PasswordField";
import { PasswordRequirements } from "../auth/PasswordRequirements";
import { PrimaryButton } from "../auth/PrimaryButton";
import { ChevronRightIcon } from "../icons";

interface SecuritySectionProps {
  password: ReturnType<typeof useChangePassword>;
  /** Owned by the panel, which drives the hook that moves focus between them. */
  currentRef: RefObject<HTMLInputElement | null>;
  nextRef: RefObject<HTMLInputElement | null>;
  confirmRef: RefObject<HTMLInputElement | null>;
  alertRef: RefObject<HTMLDivElement | null>;
  /** True after a successful change, until the next edit. */
  passwordSaved: boolean;
  onSubmitPassword: () => void;
  totp: ReturnType<typeof useTotpStatus>;
  sessions: ReturnType<typeof useSessions>;
  onManageSessions: () => void;
  onSetUpTotp: () => void;
  onTotpDisabled: () => void;
  onRecoveryCodesRegenerated: (codes: readonly string[]) => void;
}

/**
 * "Security", artboard `settings-security`: the password form with the
 * instance policy beside it, two-step verification, and the session list
 * summarised rather than duplicated.
 *
 * The password form is the same `useChangePassword` the forced-change screen
 * drives — one implementation, two placements.
 */
export function SecuritySection({
  password,
  currentRef,
  nextRef,
  confirmRef,
  alertRef,
  passwordSaved,
  onSubmitPassword,
  totp,
  sessions,
  onManageSessions,
  onSetUpTotp,
  onTotpDisabled,
  onRecoveryCodesRegenerated,
}: SecuritySectionProps) {
  const { t } = useTranslation();

  const current = sessions.sessions.find((entry) => entry.current);
  const currentDevice = current === undefined ? null : describeDevice(current.user_agent);

  return (
    <>
      <div className="hm-settings__heading">
        <h3 className="hm-settings__title">{t("settings.nav.security")}</h3>
        <p className="hm-settings__lede">{t("settings.security.lede")}</p>
      </div>

      <div className="hm-settings__scroll">
        <section className="hm-settings-card">
          <div className="hm-settings-card__split">
            <form
              className="hm-settings-form"
              onSubmit={(event) => {
                event.preventDefault();
                onSubmitPassword();
              }}
            >
              <h4 className="hm-settings-card__title">{t("settings.security.changePassword")}</h4>
              {password.formError === undefined ? null : (
                <NoticeBanner
                  ref={alertRef}
                  tone="danger"
                  message={password.formError}
                />
              )}
              <PasswordField
                id="settings-current-password"
                ref={currentRef}
                label={t("changePassword.currentPasswordLabel")}
                autoComplete="current-password"
                value={password.currentPassword}
                onChange={password.setCurrentPassword}
                error={password.fieldError("current")}
              />
              <div className="hm-settings-form__pair">
                <PasswordField
                  id="settings-new-password"
                  ref={nextRef}
                  label={t("changePassword.newPasswordLabel")}
                  autoComplete="new-password"
                  value={password.newPassword}
                  onChange={password.setNewPassword}
                  error={password.fieldError("new")}
                  onBlur={() => {
                    password.validateOnBlur(
                      "new",
                      password.newPassword !== "" &&
                        password.newPassword.length < password.minimumLength,
                      "tooShort",
                    );
                  }}
                />
                <PasswordField
                  id="settings-confirm-password"
                  ref={confirmRef}
                  label={t("settings.security.confirmLabel")}
                  autoComplete="new-password"
                  value={password.confirmPassword}
                  onChange={password.setConfirmPassword}
                  error={password.fieldError("confirm")}
                  onBlur={() => {
                    password.validateOnBlur(
                      "confirm",
                      password.confirmPassword !== "" &&
                        password.confirmPassword !== password.newPassword,
                      "mismatch",
                    );
                  }}
                />
              </div>
              <div className="hm-settings__actions">
                <PrimaryButton
                  label={t("settings.security.changePassword")}
                  busyLabel={t("changePassword.submitting")}
                  busy={password.submitting}
                />
                {passwordSaved ? <SavedMark /> : null}
              </div>
            </form>

            <div className="hm-settings-policy">
              <span className="hm-settings-policy__title">
                {t("settings.security.requirementsTitle")}
              </span>
              {/* One requirement, not the artboard's three: the other two are
                  not enforced by the server, and drawing them as requirements
                  would state something untrue. Recorded in LOGIN_HANDOFF. */}
              <PasswordRequirements
                requirements={[
                  {
                    id: "minLength",
                    label: t("password.minLength", { minimum: password.minimumLength }),
                    met: password.newPassword.length >= password.minimumLength,
                  },
                ]}
              />
              <p className="hm-settings__note">{t("settings.security.otherSessionsNote")}</p>
            </div>
          </div>
        </section>

        <TwoFactorCard
          totp={totp.totp}
          onSetUp={onSetUpTotp}
          onDisabled={onTotpDisabled}
          onRegenerated={onRecoveryCodesRegenerated}
        />

        {/* Renders itself away when the instance has no provider and this
            account has nothing linked. Undesigned, so it is deliberately
            unstyled — see SsoCard. */}
        <SsoCard />

        <section className="hm-settings-card">
          <div className="hm-settings-card__row hm-settings-card__row--centred">
            <div className="hm-settings-card__body">
              <h4 className="hm-settings-card__title">{t("settings.sessions.title")}</h4>
              {/* Two sentences, each its own key: "4 devices signed in." and
                  "This one is a Linux desktop." — the second is dropped
                  entirely rather than guessed when the list has not arrived. */}
              <p className="hm-settings-card__lede">
                {[
                  t("settings.security.sessionsCount", { count: sessions.sessions.length }),
                  currentDevice === null
                    ? null
                    : t("settings.security.thisDevice", {
                        device: t(`settings.sessions.device.${currentDevice.titleKey}`),
                      }),
                ]
                  .filter((sentence) => sentence !== null)
                  .join(" ")}
              </p>
            </div>
            <SettingsButton
              label={t("settings.security.manageSessions")}
              iconEnd={
                <ChevronRightIcon size={16} strokeWidth={1.85} className="hm-mirror-glyph" />
              }
              onClick={onManageSessions}
            />
          </div>
        </section>
      </div>
    </>
  );
}
