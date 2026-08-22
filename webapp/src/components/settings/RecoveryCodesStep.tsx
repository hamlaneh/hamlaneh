import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { RecoveryCodeList } from "./RecoveryCodeList";
import { SettingsButton } from "./SettingsButton";
import { CheckIcon, CopyIcon, DownloadIcon, PrinterIcon, TriangleAlertIcon } from "../icons";

/** One code per line — what a password manager and a printout both want. */
function asPlainText(codes: readonly string[]): string {
  return `${codes.join("\n")}\n`;
}

type CopyOutcome = "idle" | "copied" | "failed";

interface RecoveryCodesStepProps {
  codes: readonly string[];
  /** "Turn on two-step verification" at setup; "Done" after a regeneration. */
  confirmLabel: string;
  busyLabel: string;
  busy: boolean;
  onConfirm: () => void;
}

/**
 * The warning band, the ten codes and the three ways to keep them, per
 * `settings-2fa-recovery-codes`.
 *
 * The acknowledgement is the gate, not decoration: two-step verification is
 * only activated by the button below it, so an account can never end up
 * protected by a second factor whose only fallback was never displayed.
 */
export function RecoveryCodesStep({
  codes,
  confirmLabel,
  busyLabel,
  busy,
  onConfirm,
}: RecoveryCodesStepProps) {
  const { t } = useTranslation();
  const acknowledgeId = useId();
  const [acknowledged, setAcknowledged] = useState(false);
  const [copy, setCopy] = useState<CopyOutcome>("idle");

  const download = () => {
    // Guarded: a runtime without object URLs simply has no download, and the
    // codes are still on screen, copyable and printable.
    if (typeof URL.createObjectURL !== "function") {
      return;
    }
    const url = URL.createObjectURL(
      new Blob([asPlainText(codes)], { type: "text/plain;charset=utf-8" }),
    );
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "hamlaneh-recovery-codes.txt";
    anchor.click();
    URL.revokeObjectURL(url);
  };

  /**
   * The DOM lib types `navigator.clipboard` as always present; it is genuinely
   * absent outside a secure context, where the property access itself throws.
   * The try covers both that and a denied permission — either way the user is
   * told, rather than left thinking ten sign-in credentials are on the
   * clipboard when they are not.
   */
  const copyAll = async () => {
    try {
      await navigator.clipboard.writeText(asPlainText(codes));
      setCopy("copied");
    } catch (copyError) {
      console.warn("Copying the recovery codes failed:", copyError);
      setCopy("failed");
    }
  };

  return (
    <>
      <div className="hm-settings__heading">
        <h3 className="hm-settings__title">{t("settings.totp.codes.title")}</h3>
        <p className="hm-settings__lede">{t("settings.totp.codes.lede")}</p>
      </div>

      <div className="hm-settings__scroll">
        <div className="hm-settings-warning">
          <TriangleAlertIcon
            size={18}
            strokeWidth={1.85}
            className="hm-settings-warning__icon"
          />
          <span>{t("settings.totp.codes.warning")}</span>
        </div>

        <div className="hm-recovery">
          <RecoveryCodeList codes={codes} label={t("settings.totp.codes.listLabel")} />
          <div className="hm-recovery__actions">
            <div className="hm-recovery__buttons">
              <SettingsButton
                label={t("settings.totp.codes.download")}
                icon={<DownloadIcon size={16} strokeWidth={1.85} />}
                onClick={download}
              />
              <SettingsButton
                label={t("settings.totp.codes.copy")}
                icon={<CopyIcon size={16} strokeWidth={1.85} />}
                onClick={() => {
                  void copyAll();
                }}
              />
              <SettingsButton
                label={t("settings.totp.codes.print")}
                icon={<PrinterIcon size={16} strokeWidth={1.85} />}
                onClick={() => {
                  window.print();
                }}
              />
            </div>
            <p className="hm-settings__note">{t("settings.totp.codes.regenerateNote")}</p>
            {/* Not drawn: copying either works or it does not, and only an
                announcement can tell a user which. */}
            <span className="hm-visually-hidden" role="status">
              {copy === "copied"
                ? t("settings.totp.codes.copied")
                : copy === "failed"
                  ? t("settings.totp.codes.copyFailed")
                  : ""}
            </span>
          </div>
        </div>

        <div className="hm-settings__acknowledge">
          <label className="hm-settings-checkbox" htmlFor={acknowledgeId}>
            <input
              id={acknowledgeId}
              type="checkbox"
              checked={acknowledged}
              onChange={(event) => {
                setAcknowledged(event.target.checked);
              }}
            />
            <span className="hm-settings-checkbox__box" aria-hidden="true">
              <CheckIcon size={14} strokeWidth={2.6} />
            </span>
            <span>{t("settings.totp.codes.acknowledge")}</span>
          </label>
          <SettingsButton
            label={confirmLabel}
            busyLabel={busyLabel}
            busy={busy}
            tone="primary"
            disabled={!acknowledged}
            onClick={onConfirm}
          />
        </div>
      </div>
    </>
  );
}
