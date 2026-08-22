import type { ReactNode, RefObject } from "react";

import { LoaderCircleIcon } from "../icons";

/**
 * primary     — the brand pill; the one action a card is for.
 * secondary   — surface with a control boundary: Manage, Cancel, Copy.
 * danger      — surface with a danger boundary: Sign out, Disable.
 * dangerSolid — the filled confirm inside a destructive dialog.
 */
export type SettingsButtonTone = "primary" | "secondary" | "danger" | "dangerSolid";

interface SettingsButtonProps {
  label: string;
  onClick: () => void;
  tone?: SettingsButtonTone;
  /** 44px in cards and dialogs; 36px inside a session row (as drawn). */
  size?: "md" | "sm";
  icon?: ReactNode;
  /** Trailing glyph — the chevron on "Manage", which the artboard puts last. */
  iconEnd?: ReactNode;
  disabled?: boolean;
  busy?: boolean;
  /** Stable busy string; required whenever `busy` can be true. */
  busyLabel?: string;
  ref?: RefObject<HTMLButtonElement | null>;
}

/**
 * The panel's own button treatments, transcribed from `settings-security`,
 * `settings-sessions` and the `settings-components` edge cases. The auth set
 * has only a full-width form CTA, which is `PrimaryButton` — this is every
 * other button the artboards draw, and it is never a submit.
 */
export function SettingsButton({
  label,
  onClick,
  tone = "secondary",
  size = "md",
  icon,
  iconEnd,
  disabled = false,
  busy = false,
  busyLabel,
  ref,
}: SettingsButtonProps) {
  return (
    <button
      type="button"
      ref={ref}
      className={`hm-settings-button hm-settings-button--${tone}`}
      data-size={size}
      data-busy={busy}
      disabled={disabled || busy}
      aria-busy={busy}
      onClick={onClick}
    >
      {busy ? (
        <>
          <LoaderCircleIcon size={15} strokeWidth={2} className="hm-spinner" />
          {busyLabel ?? label}
        </>
      ) : (
        <>
          {icon}
          {label}
          {iconEnd}
        </>
      )}
    </button>
  );
}
