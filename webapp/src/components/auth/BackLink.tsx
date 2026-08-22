import { ArrowLeftIcon } from "../icons";

interface BackLinkProps {
  label: string;
  onClick: () => void;
}

/**
 * The low-emphasis way back out of a secondary auth screen — "Back to sign in"
 * on the reset screens, "Back" on the two-step step. A button, not an anchor:
 * these screens are chosen by state, not by URL, so there is no href to give.
 *
 * `arrow-left` is the one glyph the handoff mirrors in RTL.
 */
export function BackLink({ label, onClick }: BackLinkProps) {
  return (
    <button type="button" className="hm-back-link" onClick={onClick}>
      <ArrowLeftIcon size={18} className="hm-mirror-glyph" />
      {label}
    </button>
  );
}
