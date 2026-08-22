import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { RecoveryCodesStep } from "./RecoveryCodesStep";
import { SettingsButton } from "./SettingsButton";
import { StepHeader } from "./StepHeader";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { groupSecret } from "../../settings/sessionTime";
import { NoticeBanner } from "../auth/NoticeBanner";
import { OtpInput } from "../auth/OtpInput";
import { PrimaryButton } from "../auth/PrimaryButton";
import { CopyIcon } from "../icons";

type TotpSetup = components["schemas"]["TotpSetup"];

const CODE_LENGTH = 6;

type Step =
  | { kind: "starting" }
  | { kind: "scan"; setup: TotpSetup }
  | { kind: "verify"; setup: TotpSetup }
  | { kind: "codes"; codes: readonly string[] };

type SetupError =
  | "none"
  | "alreadyEnabled"
  | "invalidCode"
  | "incomplete"
  | "setupExpired"
  | "rateLimited"
  | "networkError"
  | "unexpected";

/** The account line under the manual key: "issuer: account" from the URI. */
function accountFromUri(otpauthUri: string): string {
  const label = /^otpauth:\/\/totp\/([^?]+)/u.exec(otpauthUri)?.[1];
  if (label === undefined) {
    return "";
  }
  try {
    return decodeURIComponent(label);
  } catch {
    // A malformed escape is not worth failing the screen over: the manual key
    // above it is what the user actually needs.
    return label;
  }
}

interface TwoFactorSetupProps {
  onCancel: () => void;
  onActivated: () => void;
}

/**
 * Two-step verification setup, artboards `settings-2fa-setup`,
 * `settings-components` §03 and `settings-2fa-recovery-codes`.
 *
 * Three steps against three calls, in that order, because the account stays
 * password-only until the last of them: /totp/setup mints the secret,
 * /totp/verify proves the authenticator and returns the recovery codes, and
 * only /totp/activate turns two-step verification on.
 *
 * A wrong code in step 2 does not restart setup — the secret stays valid, the
 * cells clear and focus returns to the first.
 */
export function TwoFactorSetup({ onCancel, onActivated }: TwoFactorSetupProps) {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>({ kind: "starting" });
  const [error, setError] = useState<SetupError>("none");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [failureCount, setFailureCount] = useState(0);
  const firstCellRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (failureCount > 0) {
      firstCellRef.current?.focus();
    }
  }, [failureCount]);

  useEffect(() => {
    let live = true;
    // Step 1 starts the moment the flow opens: /totp/setup mints (or replaces)
    // the pending secret, which is also why Cancel needs no endpoint.
    void api.POST("/api/v1/users/me/totp/setup").then(
      ({ data, error: apiError, response }) => {
        if (!live) {
          return;
        }
        if (response.status === 200 && data !== undefined) {
          setStep({ kind: "scan", setup: data });
          return;
        }
        setError(
          response.status === 409 && apiError?.error.code === "totp_already_enabled"
            ? "alreadyEnabled"
            : response.status === 429
              ? "rateLimited"
              : "unexpected",
        );
      },
      (requestError: unknown) => {
        console.warn("Two-step setup request failed:", requestError);
        if (live) {
          setError("networkError");
        }
      },
    );
    return () => {
      live = false;
    };
  }, []);

  const rejectCode = (kind: Exclude<SetupError, "none">) => {
    setError(kind);
    setCode("");
    setFailureCount((count) => count + 1);
  };

  /** Step 1 again after the pending setup expired, with a fresh secret. */
  const restart = async () => {
    try {
      const { data, response } = await api.POST("/api/v1/users/me/totp/setup");
      if (response.status === 200 && data !== undefined) {
        setStep({ kind: "scan", setup: data });
      }
    } catch (requestError) {
      console.warn("Two-step setup restart failed:", requestError);
      setError("networkError");
    }
  };

  const verify = async () => {
    if (busy) {
      return;
    }
    if (code.length < CODE_LENGTH) {
      setError("incomplete");
      firstCellRef.current?.focus();
      return;
    }
    setBusy(true);
    setError("none");
    try {
      const { data, error: apiError, response } = await api.POST(
        "/api/v1/users/me/totp/verify",
        { body: { code } },
      );
      if (response.status === 200 && data !== undefined) {
        setStep({ kind: "codes", codes: data.codes });
        setCode("");
        return;
      }
      if (response.status === 403 && apiError?.error.code === "invalid_totp_code") {
        rejectCode("invalidCode");
        return;
      }
      if (response.status === 409) {
        // The pending setup is gone (expired, or the attempt cap revoked it):
        // step 1 again, with the secret it hands back.
        setStep({ kind: "starting" });
        rejectCode("setupExpired");
        void restart();
        return;
      }
      rejectCode(response.status === 429 ? "rateLimited" : "unexpected");
    } catch (requestError) {
      console.warn("Two-step verification request failed:", requestError);
      rejectCode("networkError");
    } finally {
      setBusy(false);
    }
  };

  const activate = async () => {
    if (busy) {
      return;
    }
    setBusy(true);
    setError("none");
    try {
      const { response } = await api.POST("/api/v1/users/me/totp/activate");
      if (response.status === 204) {
        onActivated();
        return;
      }
      setError(response.status === 409 ? "setupExpired" : "unexpected");
    } catch (requestError) {
      console.warn("Two-step activation failed:", requestError);
      setError("networkError");
    } finally {
      setBusy(false);
    }
  };

  const stepNumber = step.kind === "codes" ? 3 : step.kind === "verify" ? 2 : 1;
  const cellError =
    error === "invalidCode" || error === "incomplete"
      ? t(`settings.totp.error.${error}`)
      : undefined;
  const formError =
    error === "alreadyEnabled" ||
    error === "setupExpired" ||
    error === "rateLimited" ||
    error === "networkError" ||
    error === "unexpected"
      ? t(`settings.totp.error.${error}`)
      : undefined;

  return (
    <>
      <StepHeader
        backLabel={t("settings.nav.security")}
        onBack={onCancel}
        step={stepNumber}
        total={3}
      />

      {formError === undefined ? null : (
        <NoticeBanner tone={error === "rateLimited" ? "warning" : "danger"} message={formError} />
      )}

      {step.kind === "starting" ? (
        <p className="hm-settings__note" role="status">
          {t("common.loading")}
        </p>
      ) : step.kind === "codes" ? (
        <RecoveryCodesStep
          codes={step.codes}
          confirmLabel={t("settings.totp.codes.activate")}
          busyLabel={t("settings.totp.codes.activating")}
          busy={busy}
          onConfirm={() => {
            void activate();
          }}
        />
      ) : step.kind === "scan" ? (
        <ScanStep
          setup={step.setup}
          onContinue={() => {
            setError("none");
            setStep({ kind: "verify", setup: step.setup });
          }}
          onCancel={onCancel}
        />
      ) : (
        <>
          <div className="hm-settings__heading">
            <h3 className="hm-settings__title">{t("settings.totp.verify.title")}</h3>
            <p className="hm-settings__lede">{t("settings.totp.verify.lede")}</p>
          </div>
          <form
            className="hm-settings__form"
            onSubmit={(event) => {
              event.preventDefault();
              void verify();
            }}
          >
            <OtpInput
              id="totp-setup-code"
              label={t("settings.totp.verify.codeLabel")}
              value={code}
              size="compact"
              length={CODE_LENGTH}
              firstCellRef={firstCellRef}
              error={cellError}
              onChange={(value) => {
                setCode(value);
                if (error !== "none") {
                  setError("none");
                }
              }}
            />
            <div className="hm-settings__actions">
              <PrimaryButton
                label={t("settings.totp.verify.submit")}
                busyLabel={t("settings.totp.verify.submitting")}
                busy={busy}
              />
              <SettingsButton label={t("settings.cancel")} onClick={onCancel} />
            </div>
          </form>
        </>
      )}
    </>
  );
}

interface ScanStepProps {
  setup: TotpSetup;
  onContinue: () => void;
  onCancel: () => void;
}

/** Step 1: the QR, the always-visible manual key, and what to do with them. */
function ScanStep({ setup, onContinue, onCancel }: ScanStepProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle");

  // Outside a secure context the property access itself throws; the catch
  // covers that as well as a denied permission.
  const copyKey = async () => {
    try {
      await navigator.clipboard.writeText(setup.secret);
      setCopied("copied");
    } catch (copyError) {
      console.warn("Copying the two-step key failed:", copyError);
      setCopied("failed");
    }
  };

  return (
    <>
      <div className="hm-settings__heading">
        <h3 className="hm-settings__title">{t("settings.totp.scan.title")}</h3>
        <p className="hm-settings__lede">{t("settings.totp.scan.lede")}</p>
      </div>

      <div className="hm-settings__scroll hm-totp-scan">
        <div className="hm-totp-scan__qr-column">
          {/* The QR is rendered per request by our own server from the secret
              below it (contract: never a stored image). Injecting it is the
              shape the contract chose; the deployed CSP is what makes it safe
              — script-src 'self' with no 'unsafe-inline' means nothing inside
              this markup can execute, and default-src 'self' means it cannot
              fetch. The manual key beneath is the fallback if it fails. */}
          <div
            className="hm-totp-scan__qr"
            role="img"
            aria-label={t("settings.totp.scan.qrLabel")}
            dangerouslySetInnerHTML={{ __html: setup.qr_svg }}
          />
          <p className="hm-settings__note">{t("settings.totp.scan.qrNote")}</p>
        </div>

        <div className="hm-totp-scan__instructions">
          <ol className="hm-totp-steps">
            <li className="hm-totp-steps__item">
              <span className="hm-totp-steps__number" aria-hidden="true">
                1
              </span>
              <span>{t("settings.totp.scan.step1")}</span>
            </li>
            <li className="hm-totp-steps__item">
              <span className="hm-totp-steps__number" aria-hidden="true">
                2
              </span>
              <span>{t("settings.totp.scan.step2")}</span>
            </li>
            <li className="hm-totp-steps__item">
              <span className="hm-totp-steps__number" aria-hidden="true">
                3
              </span>
              <span>{t("settings.totp.scan.step3")}</span>
            </li>
          </ol>

          {/* Always visible, never behind a "Can't scan?" disclosure: someone
              without a working camera must not have to hunt for it. */}
          <div className="hm-totp-key">
            <span className="hm-totp-key__label" id="totp-manual-key-label">
              {t("settings.totp.scan.manualKeyLabel")}
            </span>
            <div className="hm-totp-key__row">
              <output
                className="hm-totp-key__value"
                dir="ltr"
                aria-labelledby="totp-manual-key-label"
              >
                {groupSecret(setup.secret)}
              </output>
              <SettingsButton
                label={t("settings.totp.scan.copyKey")}
                icon={<CopyIcon size={16} strokeWidth={1.85} />}
                onClick={() => {
                  void copyKey();
                }}
              />
            </div>
            <p className="hm-settings__note">
              {t("settings.totp.scan.accountLine", { account: accountFromUri(setup.otpauth_uri) })}
            </p>
            <span className="hm-visually-hidden" role="status">
              {copied === "copied"
                ? t("settings.totp.scan.copied")
                : copied === "failed"
                  ? t("settings.totp.codes.copyFailed")
                  : ""}
            </span>
          </div>

          <div className="hm-settings__actions">
            <SettingsButton
              label={t("settings.totp.scan.continue")}
              tone="primary"
              onClick={onContinue}
            />
            <SettingsButton label={t("settings.cancel")} onClick={onCancel} />
          </div>
        </div>
      </div>
    </>
  );
}
