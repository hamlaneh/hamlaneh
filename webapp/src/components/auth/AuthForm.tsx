import { useId } from "react";
import type { ReactNode, SubmitEvent } from "react";

interface AuthFormProps {
  title: string;
  helper?: string;
  /** Called on submit — Enter in any field submits, as does the button. */
  onSubmit: () => void;
  children: ReactNode;
}

/** The 424px form column: heading block plus whatever the screen composes. */
export function AuthForm({ title, helper, onSubmit, children }: AuthFormProps) {
  const titleId = useId();

  const handleSubmit = (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    onSubmit();
  };

  return (
    <form className="hm-form" aria-labelledby={titleId} onSubmit={handleSubmit}>
      <div>
        <h2 id={titleId} className="hm-form__title">
          {title}
        </h2>
        {helper === undefined ? null : <p className="hm-form__helper">{helper}</p>}
      </div>
      {children}
    </form>
  );
}
