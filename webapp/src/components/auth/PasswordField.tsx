import { useState } from "react";
import type { Ref } from "react";
import { useTranslation } from "react-i18next";

import type { FieldProps } from "./Field";
import { Field } from "./Field";
import { fieldErrorId } from "./fieldErrorId";
import { EyeIcon, EyeOffIcon } from "../icons";

interface PasswordFieldProps extends FieldProps {
  value: string;
  onChange: (value: string) => void;
  autoComplete: "current-password" | "new-password";
  onBlur?: () => void;
  /** Lets a screen move focus here after an invalid submission. */
  ref?: Ref<HTMLInputElement>;
}

/**
 * Password control with the reveal toggle inside its trailing edge. The
 * toggle's accessible name flips between Show and Hide, focus stays on it,
 * and it is disabled (and out of the tab order) with the field.
 */
export function PasswordField({
  value,
  onChange,
  autoComplete,
  onBlur,
  ref,
  ...field
}: PasswordFieldProps) {
  const { t } = useTranslation();
  const [revealed, setRevealed] = useState(false);
  const toggleLabel = revealed ? t("password.hide") : t("password.show");

  return (
    <Field {...field}>
      <input
        className="hm-input hm-input--with-toggle"
        id={field.id}
        ref={ref}
        type={revealed ? "text" : "password"}
        autoComplete={autoComplete}
        disabled={field.disabled}
        aria-invalid={field.error === undefined ? undefined : true}
        aria-describedby={field.error === undefined ? undefined : fieldErrorId(field.id)}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        onBlur={onBlur}
      />
      <button
        type="button"
        className="hm-field__toggle"
        data-revealed={revealed}
        disabled={field.disabled}
        aria-label={toggleLabel}
        title={toggleLabel}
        onClick={() => {
          setRevealed((current) => !current);
        }}
      >
        {revealed ? <EyeOffIcon size={19} /> : <EyeIcon size={19} />}
      </button>
    </Field>
  );
}
