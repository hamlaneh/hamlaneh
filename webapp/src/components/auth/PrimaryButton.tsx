import { LoaderCircleIcon } from "./icons";

interface PrimaryButtonProps {
  label: string;
  /** Stable busy string shown while the request is in flight. */
  busyLabel: string;
  busy: boolean;
  disabled?: boolean;
}

/**
 * The single call to action of an auth form. While busy it keeps the brand
 * fill (not the disabled treatment) and stays disabled, which is what blocks
 * duplicate submissions; the button is full-width, so the label swap never
 * resizes it.
 */
export function PrimaryButton({ label, busyLabel, busy, disabled = false }: PrimaryButtonProps) {
  return (
    <button
      type="submit"
      className="hm-button"
      disabled={busy || disabled}
      data-busy={busy}
      aria-busy={busy}
    >
      {busy ? (
        <>
          <LoaderCircleIcon size={18} strokeWidth={2} className="hm-spinner" />
          {busyLabel}
        </>
      ) : (
        label
      )}
    </button>
  );
}
