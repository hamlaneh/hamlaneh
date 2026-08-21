import type { ReactNode } from "react";

/**
 * Inline SVG glyphs transcribed from the delivered mockup
 * (auth-foundations-and-components -> "Icons - Lucide, 1.75 stroke"):
 * 24x24 viewBox, currentColor, round caps and joins. No icon dependency —
 * the drawn paths are the source of truth (see docs/design/LOGIN_HANDOFF.md).
 *
 * Only the glyphs this slice renders live here. The remaining four
 * (arrow-left, check, info, moon) arrive with the screens that use them.
 */

interface IconProps {
  /** Rendered square size in px; the viewBox is always 24x24. */
  size: number;
  strokeWidth?: number;
  className?: string;
}

function Glyph({
  size,
  strokeWidth = 1.75,
  className,
  children,
}: IconProps & { children: ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      {children}
    </svg>
  );
}

export function EyeIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M2.1 12.3a1 1 0 0 1 0-.6 10.8 10.8 0 0 1 19.8 0 1 1 0 0 1 0 .6 10.8 10.8 0 0 1-19.8 0" />
      <circle cx="12" cy="12" r="3" />
    </Glyph>
  );
}

export function EyeOffIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M10.7 5.1a10.7 10.7 0 0 1 11.2 6.6 1 1 0 0 1 0 .6 10.7 10.7 0 0 1-1.4 2.5" />
      <path d="M14.1 14.2a3 3 0 0 1-4.3-4.3" />
      <path d="M17.5 17.5A10.8 10.8 0 0 1 2.1 12.3a1 1 0 0 1 0-.6 10.8 10.8 0 0 1 4.4-5.2" />
      <path d="m2 2 20 20" />
    </Glyph>
  );
}

export function CircleAlertIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="12" r="10" />
      <path d="M12 8v4" />
      <path d="M12 16h.01" />
    </Glyph>
  );
}

export function TriangleAlertIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="m21.7 18-8-14a2 2 0 0 0-3.4 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.7-3" />
      <path d="M12 9v4" />
      <path d="M12 17h.01" />
    </Glyph>
  );
}

export function CircleCheckIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="12" r="10" />
      <path d="m9 12 2 2 4-4" />
    </Glyph>
  );
}

export function CircleIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="12" r="10" />
    </Glyph>
  );
}

export function LoaderCircleIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M21 12a9 9 0 1 1-6.2-8.6" />
    </Glyph>
  );
}

/**
 * The decorative nested-arc motif at the foot of the identity panel:
 * aria-hidden, removable, mirrored in RTL by the stylesheet.
 */
export function ArcMotif({ className }: { className?: string }) {
  return (
    <svg aria-hidden="true" focusable="false" viewBox="0 0 400 400" className={className}>
      <g fill="none" stroke="var(--color-motif)" strokeWidth="1.5">
        <path d="M40 340a160 160 0 0 1 320 0" />
        <path d="M92 340a108 108 0 0 1 216 0" />
        <path d="M144 340a56 56 0 0 1 112 0" />
      </g>
    </svg>
  );
}
