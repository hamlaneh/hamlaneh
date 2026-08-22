import { useId, useRef, useState } from "react";

import { SettingsButton } from "./SettingsButton";
import { useFocusTrap } from "./useFocusTrap";
import { NoticeBanner } from "../auth/NoticeBanner";
import { PasswordField } from "../auth/PasswordField";

interface ConfirmDialogProps {
  title: string;
  body: string;
  confirmLabel: string;
  busyLabel: string;
  cancelLabel: string;
  /** Filled danger for a destructive confirm, brand for an ordinary one. */
  tone?: "danger" | "primary";
  /**
   * Re-authentication, for the actions the contract gates on the password:
   * disabling two-step verification and minting fresh recovery codes.
   */
  passwordLabel?: string;
  /** Shown above the actions; already localized. */
  error?: string | undefined;
  busy?: boolean;
  onConfirm: (password: string) => void;
  onCancel: () => void;
  /**
   * What Escape does, when that is not what the cancel button does. The
   * leave-without-saving dialog needs this: its second button discards the
   * edits, and Escape must never be a way to lose them by accident.
   */
  onDismiss?: () => void;
}

/**
 * The confirm dialog drawn on `settings-components` §03 and §04: a title, one
 * plain sentence, an optional password field, and two buttons.
 *
 * It traps focus and hands it back to whatever raised it — which is always a
 * button still on screen behind the dialog.
 */
export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  busyLabel,
  cancelLabel,
  tone = "danger",
  passwordLabel,
  error,
  busy = false,
  onConfirm,
  onCancel,
  onDismiss,
}: ConfirmDialogProps) {
  const titleId = useId();
  const bodyId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);
  const [password, setPassword] = useState("");

  return (
    <div className="hm-settings-confirm__scrim">
      <div
        ref={dialogRef}
        className="hm-settings-confirm"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            (onDismiss ?? onCancel)();
            return;
          }
          handleTrapKey(event);
        }}
      >
        <h3 className="hm-settings-confirm__title" id={titleId}>
          {title}
        </h3>
        <p className="hm-settings-confirm__body" id={bodyId}>
          {body}
        </p>
        {passwordLabel === undefined ? null : (
          <PasswordField
            id="confirm-password-prompt"
            label={passwordLabel}
            autoComplete="current-password"
            value={password}
            onChange={setPassword}
          />
        )}
        {error === undefined ? null : <NoticeBanner tone="danger" message={error} />}
        <div className="hm-settings-confirm__actions">
          <SettingsButton
            label={confirmLabel}
            busyLabel={busyLabel}
            busy={busy}
            tone={tone === "danger" ? "dangerSolid" : "primary"}
            size="sm"
            onClick={() => {
              onConfirm(password);
            }}
          />
          <SettingsButton
            label={cancelLabel}
            size="sm"
            disabled={busy}
            onClick={onCancel}
          />
        </div>
      </div>
    </div>
  );
}
