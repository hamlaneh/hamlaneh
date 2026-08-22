import { useId, useRef } from "react";
import type { ClipboardEvent, KeyboardEvent, RefObject } from "react";
import { useTranslation } from "react-i18next";

import { fieldErrorId } from "./fieldErrorId";
import { CircleAlertIcon } from "../icons";

/** Everything that is not a digit; the design drops all of it on paste. */
const NON_DIGITS = /[^0-9]/gu;

interface OtpInputProps {
  /** Base id; cell N is `${id}-${N}`. */
  id: string;
  label: string;
  /** Digits typed so far, shortest-first — never longer than `length`. */
  value: string;
  onChange: (value: string) => void;
  length?: number;
  disabled?: boolean;
  /** Marks every cell invalid and renders the message beneath. */
  error?: string | undefined;
  /**
   * The panel draws a denser cell than the auth screens (42x50 vs 60x60).
   * Same component, two contexts — settings-components says so explicitly.
   */
  size?: "default" | "compact";
  /** Lets a screen put focus back on cell 1 after a rejected code. */
  firstCellRef?: RefObject<HTMLInputElement | null>;
}

/** Only digits survive a paste — the design drops everything else. */
function digitsOf(raw: string, limit: number): string {
  return raw.replace(NON_DIGITS, "").slice(0, limit);
}

/**
 * The six-cell one-time code, per `auth-foundations-and-components` §07: paste
 * into any cell distributes across all six and lands focus on the last, the
 * row stays `dir="ltr"` in Persian so the first digit typed is always the
 * leftmost cell, and backspace walks back through the cells.
 *
 * The value is a plain string so a caller can clear the whole input by
 * clearing it — which is exactly what a rejected code does.
 */
export function OtpInput({
  id,
  label,
  value,
  onChange,
  length = 6,
  disabled = false,
  error,
  size = "default",
  firstCellRef,
}: OtpInputProps) {
  const { t } = useTranslation();
  const labelId = useId();
  const cells = useRef<(HTMLInputElement | null)[]>([]);

  const digits = Array.from({ length }, (_, index) => value[index] ?? "");
  const complete = value.length === length;

  const focusCell = (index: number) => {
    cells.current[Math.min(Math.max(index, 0), length - 1)]?.focus();
  };

  /** Rewrites the whole value and lands focus where the design says it should. */
  const distribute = (from: number, raw: string) => {
    const incoming = digitsOf(raw, length - from);
    if (incoming === "") {
      return;
    }
    const next = `${value.slice(0, from)}${incoming}`.slice(0, length);
    onChange(next);
    focusCell(next.length);
  };

  const handleCellInput = (index: number, raw: string) => {
    if (raw === "") {
      // Deleting the cell's digit truncates: a code has no holes.
      onChange(value.slice(0, index));
      return;
    }
    // One digit advances by one; a whole code pasted or auto-filled into a
    // single cell distributes from there. Writing never starts past the end of
    // what has been typed, so the code can never grow a hole in the middle.
    distribute(Math.min(index, value.length), raw);
  };

  const handleKeyDown = (index: number, event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Backspace" && digits[index] === "" && index > 0) {
      event.preventDefault();
      onChange(value.slice(0, index - 1));
      focusCell(index - 1);
      return;
    }
    if (event.key === "ArrowLeft" && index > 0) {
      event.preventDefault();
      focusCell(index - 1);
      return;
    }
    if (event.key === "ArrowRight" && index < length - 1) {
      event.preventDefault();
      focusCell(index + 1);
    }
  };

  const handlePaste = (event: ClipboardEvent<HTMLInputElement>) => {
    event.preventDefault();
    // "A paste into any cell distributes across all six" — always from the
    // start, so pasting a code never depends on which cell had focus.
    distribute(0, event.clipboardData.getData("text"));
  };

  return (
    <div className="hm-field">
      <div className="hm-field__label-row">
        <span className="hm-label" id={labelId} data-disabled={disabled}>
          {label}
        </span>
      </div>
      {/* The row keeps its own direction inside an RTL page (Bidi rules). */}
      <div
        className="hm-otp"
        role="group"
        aria-labelledby={labelId}
        dir="ltr"
        data-size={size}
        data-complete={complete}
        data-invalid={error === undefined ? undefined : true}
      >
        {digits.map((digit, index) => (
          <input
            // Cells are positional and never reordered; the index IS the identity.
            key={`${id}-${String(index)}`}
            id={`${id}-${String(index + 1)}`}
            ref={(element) => {
              cells.current[index] = element;
              if (index === 0 && firstCellRef !== undefined) {
                firstCellRef.current = element;
              }
            }}
            className="hm-otp__cell"
            type="text"
            inputMode="numeric"
            // One field per code, on the first cell: the browser fills the
            // whole code there and `distribute` spreads it.
            autoComplete={index === 0 ? "one-time-code" : "off"}
            maxLength={1}
            disabled={disabled}
            aria-label={t("password.otpCell", { index: index + 1, total: length })}
            aria-invalid={error === undefined ? undefined : true}
            aria-describedby={error === undefined ? undefined : fieldErrorId(id)}
            value={digit}
            onChange={(event) => {
              handleCellInput(index, event.target.value);
            }}
            onKeyDown={(event) => {
              handleKeyDown(index, event);
            }}
            onPaste={handlePaste}
          />
        ))}
      </div>
      {error === undefined ? null : (
        <span className="hm-field__error" id={fieldErrorId(id)} role="alert">
          <CircleAlertIcon size={15} strokeWidth={2} className="hm-field__error-icon" />
          {error}
        </span>
      )}
    </div>
  );
}
