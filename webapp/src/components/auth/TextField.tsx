import type { FieldProps } from "./Field";
import { Field } from "./Field";
import { fieldErrorId } from "./fieldErrorId";

interface TextFieldProps extends FieldProps {
  value: string;
  onChange: (value: string) => void;
  autoComplete: string;
  /** "auto" for the identifier, which accepts a username or an email. */
  dir?: "auto" | "ltr";
}

/** Single-line text control, 48px tall, in the delivered field treatment. */
export function TextField({ value, onChange, autoComplete, dir, ...field }: TextFieldProps) {
  return (
    <Field {...field}>
      <input
        className="hm-input"
        id={field.id}
        type="text"
        dir={dir}
        autoComplete={autoComplete}
        disabled={field.disabled}
        aria-invalid={field.error === undefined ? undefined : true}
        aria-describedby={field.error === undefined ? undefined : fieldErrorId(field.id)}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
    </Field>
  );
}
