import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { CircleCheckIcon, CopyIcon, DownloadIcon, InfoIcon, TriangleAlertIcon } from "../icons";
import { SettingsButton } from "../settings/SettingsButton";
import { useFocusTrap } from "../settings/useFocusTrap";

export interface OneTimeValue {
  label: string;
  value: string;
  /** The value the panel exists for, set one step larger (the password). */
  emphasis?: boolean;
}

interface CredentialsPanelProps {
  title: string;
  lede: string;
  /** The band above everything; states that this is the only showing. */
  warning: string;
  /** The standing advice under the actions. */
  note: string;
  values: readonly OneTimeValue[];
  /** Base name for the downloaded text file, without an extension. */
  filename: string;
  /** "Create another", offered only after creating an account. */
  onCreateAnother?: () => void;
  onClose: () => void;
}

/** Writes a text file to the user's disk without touching the network. */
function downloadText(filename: string, body: string): void {
  const url = URL.createObjectURL(new Blob([body], { type: "text/plain;charset=utf-8" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}

/**
 * The one-time step (`admin-create-user-credentials`), used by every value
 * the server can never show again: an account's temporary password, a forced
 * reset's replacement, and an invite link.
 *
 * All three are in their response and nowhere else — the server stores only a
 * hash — so the panel is built around that fact: a warning band above
 * everything, copy and download offered BEFORE the only close button, and
 * **Escape deliberately does not close it**. This is the one dialog in the
 * product that needs a deliberate acknowledgement.
 */
export function CredentialsPanel({
  title,
  lede,
  warning,
  note,
  values,
  filename,
  onCreateAnother,
  onClose,
}: CredentialsPanelProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const bandId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);
  const [copied, setCopied] = useState<"none" | "done" | "failed">("none");

  const asText = `${values.map((entry) => `${entry.label}: ${entry.value}`).join("\n")}\n`;

  const copyAll = () => {
    void navigator.clipboard.writeText(asText).then(
      () => {
        setCopied("done");
      },
      (copyError: unknown) => {
        // Clipboard access can be refused outright (insecure context, or a
        // permission the user declined). The values are on screen either
        // way, so say so rather than pretend it worked.
        console.warn("Copying the one-time values failed:", copyError);
        setCopied("failed");
      },
    );
  };

  return (
    <div className="hm-admin-modal__scrim">
      <div
        ref={dialogRef}
        className="hm-admin-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bandId}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            // Deliberate: this panel is shown once and the server cannot show
            // it again, so dismissing it must be an act, not a reflex.
            event.stopPropagation();
            return;
          }
          handleTrapKey(event);
        }}
      >
        <div className="hm-admin-credentials__band" id={bandId}>
          <TriangleAlertIcon
            size={18}
            strokeWidth={1.85}
            className="hm-admin-credentials__band-icon"
          />
          <span>{warning}</span>
        </div>

        <div className="hm-admin-modal__body">
          <div className="hm-admin-credentials__lede">
            <span className="hm-admin-credentials__mark">
              <CircleCheckIcon size={20} strokeWidth={1.85} />
            </span>
            <span>
              <h2 className="hm-admin-credentials__title" id={titleId}>
                {title}
              </h2>
              <p className="hm-admin-modal__subtitle">{lede}</p>
            </span>
          </div>

          {/* Generated values are mono and dir="ltr", so they read correctly
              in a Persian interface and no character is ambiguous. */}
          <dl className="hm-admin-credentials__values">
            {values.map((entry, index) => (
              <div key={entry.label}>
                {index === 0 ? null : (
                  <span className="hm-admin-credentials__rule" aria-hidden="true" />
                )}
                <div
                  className={`hm-admin-credentials__pair${
                    entry.emphasis === true ? " hm-admin-credentials__pair--emphasis" : ""
                  }`}
                >
                  <dt>{entry.label}</dt>
                  <dd dir="ltr">{entry.value}</dd>
                </div>
              </div>
            ))}
          </dl>

          <div className="hm-admin-state__actions">
            <SettingsButton
              tone="primary"
              label={t("admin.credentials.copy")}
              icon={<CopyIcon size={15} strokeWidth={1.85} />}
              onClick={copyAll}
            />
            <SettingsButton
              label={t("admin.credentials.download")}
              icon={<DownloadIcon size={17} strokeWidth={1.85} />}
              onClick={() => {
                downloadText(`${filename}.txt`, asText);
              }}
            />
          </div>
          {copied === "none" ? null : (
            <span className="hm-admin-hint" role="status">
              {t(copied === "done" ? "admin.credentials.copied" : "admin.credentials.copyFailed")}
            </span>
          )}

          <div className="hm-admin-note">
            <InfoIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
            <span>{note}</span>
          </div>
        </div>

        <div className="hm-admin-modal__footer">
          <SettingsButton label={t("admin.credentials.acknowledge")} onClick={onClose} />
          {onCreateAnother === undefined ? null : (
            <button type="button" className="hm-admin__exit" onClick={onCreateAnother}>
              {t("admin.credentials.createAnother")}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
