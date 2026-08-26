import type { ReactNode } from "react";

import { fieldErrorId, fieldHintId } from "./fieldErrorId";
import { CircleAlertIcon } from "../icons";

export interface FieldProps {
  id: string;
  label: string;
  /** Rendered at the logical end of the label row (e.g. the reset link). */
  labelAction?: ReactNode;
  disabled?: boolean;
  /** Field-level message; also marks the control invalid. */
  error?: string | undefined;
  /**
   * A standing constraint line under the control ("Lowercase, no spaces.
   * Cannot be changed later."). It is described by the control, not merely
   * placed near it — the admin create-user modal draws one on most fields.
   */
  hint?: string | undefined;
}

/**
 * Label row, control slot and error line — shared by TextField and
 * PasswordField so both stay identical down to the pixel.
 */
export function Field({
  id,
  label,
  labelAction,
  disabled = false,
  error,
  hint,
  children,
}: FieldProps & { children: ReactNode }) {
  return (
    <div className="hm-field">
      <div className="hm-field__label-row">
        <label className="hm-label" htmlFor={id} data-disabled={disabled}>
          {label}
        </label>
        {labelAction}
      </div>
      <div className="hm-field__control">{children}</div>
      {hint === undefined ? null : (
        <span className="hm-field__hint" id={fieldHintId(id)}>
          {hint}
        </span>
      )}
      {error === undefined ? null : (
        <span className="hm-field__error" id={fieldErrorId(id)} role="alert">
          <CircleAlertIcon size={15} strokeWidth={2} className="hm-field__error-icon" />
          {error}
        </span>
      )}
    </div>
  );
}
