import { useState } from "react";
import { useTranslation } from "react-i18next";

import { ConfirmDialog } from "./ConfirmDialog";
import { SettingsButton } from "./SettingsButton";
import { api } from "../../api/client";
import { formatActivationDate } from "../../settings/sessionTime";
import type { TotpStatus } from "../../settings/useTotpStatus";

/** How few codes left before the count is worth reading as a warning. */
const LOW_CODES = 2;

type Prompt = "none" | "disable" | "regenerate";

type PromptError = "none" | "invalidPassword" | "notEnabled" | "rateLimited" | "unexpected";

interface TwoFactorCardProps {
  totp: TotpStatus;
  onSetUp: () => void;
  onDisabled: () => void;
  onRegenerated: (codes: readonly string[]) => void;
}

/**
 * The Security card's two-step block: the off state that offers setup
 * (`settings-security`) and the on state that reports the date, the codes left
 * and the two ways to change it (`settings-components` §03).
 *
 * Both of those actions re-ask for the password. The session alone is not
 * enough: a hijacked session must not be able to remove the second factor or
 * mint fresh sign-in credentials.
 */
export function TwoFactorCard({ totp, onSetUp, onDisabled, onRegenerated }: TwoFactorCardProps) {
  const { t, i18n } = useTranslation();
  const [prompt, setPrompt] = useState<Prompt>("none");
  const [promptError, setPromptError] = useState<PromptError>("none");
  const [busy, setBusy] = useState(false);

  const closePrompt = () => {
    setPrompt("none");
    setPromptError("none");
  };

  /** Maps the two password-gated endpoints' shared failure vocabulary. */
  const mapFailure = (status: number, code: string | undefined): PromptError =>
    status === 403 && code === "invalid_current_password"
      ? "invalidPassword"
      : status === 409
        ? "notEnabled"
        : status === 429
          ? "rateLimited"
          : "unexpected";

  const disable = async (password: string) => {
    setBusy(true);
    setPromptError("none");
    try {
      const { error: apiError, response } = await api.POST("/api/v1/users/me/totp/disable", {
        body: { password },
      });
      if (response.status === 204) {
        closePrompt();
        onDisabled();
        return;
      }
      setPromptError(mapFailure(response.status, apiError?.error.code));
    } catch (requestError) {
      console.warn("Disabling two-step verification failed:", requestError);
      setPromptError("unexpected");
    } finally {
      setBusy(false);
    }
  };

  const regenerate = async (password: string) => {
    setBusy(true);
    setPromptError("none");
    try {
      const { data, error: apiError, response } = await api.POST(
        "/api/v1/users/me/totp/recovery-codes",
        { body: { password } },
      );
      if (response.status === 200 && data !== undefined) {
        closePrompt();
        onRegenerated(data.codes);
        return;
      }
      setPromptError(mapFailure(response.status, apiError?.error.code));
    } catch (requestError) {
      console.warn("Regenerating the recovery codes failed:", requestError);
      setPromptError("unexpected");
    } finally {
      setBusy(false);
    }
  };

  const remaining = totp.recovery_codes_remaining;
  const total = totp.recovery_codes_total;

  return (
    <section className="hm-settings-card">
      <div className="hm-settings-card__row">
        <div className="hm-settings-card__body">
          <div className="hm-settings-card__title-row">
            <h4 className="hm-settings-card__title">{t("settings.totp.cardTitle")}</h4>
            <span className="hm-settings-state" data-on={totp.enabled}>
              <span className="hm-settings-state__dot" />
              {totp.enabled ? t("settings.totp.on") : t("settings.totp.off")}
            </span>
          </div>
          {totp.enabled ? (
            <dl className="hm-settings-facts">
              {/* Both fields are omitted, not null, while two-step is off —
                  and nothing guarantees a row exists just because it is on. */}
              {totp.activated_at === null || totp.activated_at === undefined ? null : (
                <div className="hm-settings-facts__row">
                  <dt>{t("settings.totp.turnedOn")}</dt>
                  <dd>{formatActivationDate(totp.activated_at, i18n.language)}</dd>
                </div>
              )}
              {typeof remaining === "number" && typeof total === "number" ? (
                <div className="hm-settings-facts__row">
                  <dt>{t("settings.totp.codesLeft")}</dt>
                  {/* The useful number, not a tick — and low reads as a warning. */}
                  <dd data-low={remaining <= LOW_CODES}>
                    {t("settings.totp.codesLeftValue", { remaining, total })}
                  </dd>
                </div>
              ) : null}
            </dl>
          ) : (
            <p className="hm-settings-card__lede">{t("settings.totp.explainer")}</p>
          )}
        </div>
        {totp.enabled ? null : (
          <SettingsButton label={t("settings.totp.setUp")} tone="primary" onClick={onSetUp} />
        )}
      </div>

      {totp.enabled ? (
        <div className="hm-settings-card__actions">
          <SettingsButton
            label={t("settings.totp.newCodes")}
            size="sm"
            onClick={() => {
              setPrompt("regenerate");
            }}
          />
          <SettingsButton
            label={t("settings.totp.disable")}
            tone="danger"
            size="sm"
            onClick={() => {
              setPrompt("disable");
            }}
          />
        </div>
      ) : null}

      {prompt === "none" ? null : (
        <ConfirmDialog
          title={t(`settings.totp.${prompt}Prompt.title`)}
          body={t(`settings.totp.${prompt}Prompt.body`)}
          confirmLabel={t(`settings.totp.${prompt}Prompt.confirm`)}
          busyLabel={t("settings.working")}
          cancelLabel={t("settings.cancel")}
          tone={prompt === "disable" ? "danger" : "primary"}
          passwordLabel={t("settings.totp.confirmPasswordLabel")}
          busy={busy}
          error={
            promptError === "none" ? undefined : t(`settings.totp.error.${promptError}`)
          }
          onConfirm={(password) => {
            void (prompt === "disable" ? disable(password) : regenerate(password));
          }}
          onCancel={closePrompt}
        />
      )}
    </section>
  );
}
