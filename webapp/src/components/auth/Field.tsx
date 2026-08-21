import type { ReactNode } from "react";

import { fieldErrorId } from "./fieldErrorId";
import { CircleAlertIcon } from "../icons";

export interface FieldProps {
  id: string;
  label: string;
  /** Rendered at the logical end of the label row (e.g. the reset link). */
  labelAction?: ReactNode;
  disabled?: boolean;
  /** Field-level message; also marks the control invalid. */
  error?: string | undefined;
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
      {error === undefined ? null : (
        <span className="hm-field__error" id={fieldErrorId(id)} role="alert">
          <CircleAlertIcon size={15} strokeWidth={2} className="hm-field__error-icon" />
          {error}
        </span>
      )}
    </div>
  );
}
