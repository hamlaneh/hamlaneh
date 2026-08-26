import type { Ref } from "react";

import type { FieldProps } from "./Field";
import { Field } from "./Field";
import { fieldDescribedBy } from "./fieldErrorId";

interface TextFieldProps extends FieldProps {
  value: string;
  onChange: (value: string) => void;
  autoComplete: string;
  /** "auto" for the identifier, which accepts a username or an email. */
  dir?: "auto" | "ltr";
  /** "email" on the reset-request artboard; "text" everywhere else. */
  type?: "text" | "email";
  ref?: Ref<HTMLInputElement>;
}

/** Single-line text control, 48px tall, in the delivered field treatment. */
export function TextField({
  value,
  onChange,
  autoComplete,
  dir,
  type = "text",
  ref,
  ...field
}: TextFieldProps) {
  return (
    <Field {...field}>
      <input
        className="hm-input"
        id={field.id}
        ref={ref}
        type={type}
        dir={dir}
        autoComplete={autoComplete}
        disabled={field.disabled}
        aria-invalid={field.error === undefined ? undefined : true}
        aria-describedby={fieldDescribedBy(field.id, field)}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
    </Field>
  );
}
