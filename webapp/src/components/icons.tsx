import type { ReactNode } from "react";

/**
 * Inline SVG glyphs transcribed from the delivered mockups (the auth set from
 * "Hamlaneh Auth.dc.html", the chat set from "Hamlaneh Chat.dc.html"):
 * Lucide family, 24x24 viewBox, currentColor, round caps and joins. No icon
 * dependency — the drawn paths are the source of truth.
 *
 * Stroke width defaults to 1.75, the family's weight. Call sites pass a
 * different weight only where an artboard draws one.
 */

export interface IconProps {
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

/* ── auth set ──────────────────────────────────────────────────────── */

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

/* ── chat set ──────────────────────────────────────────────────────── */

export function HashIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M4 9h16" />
      <path d="M4 15h16" />
      <path d="M10 3 8 21" />
      <path d="M16 3l-2 18" />
    </Glyph>
  );
}

export function LockIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="4" y="11" width="16" height="10" rx="2" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" />
    </Glyph>
  );
}

export function UsersIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.9" />
    </Glyph>
  );
}

export function UserPlusIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
      <circle cx="9" cy="7" r="4" />
      <path d="M19 8v6" />
      <path d="M22 11h-6" />
    </Glyph>
  );
}

export function SearchIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.5-3.5" />
    </Glyph>
  );
}

export function PaperclipIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="m21.4 11.1-9.2 9.2a5.5 5.5 0 0 1-7.8-7.8l8.5-8.5a3.7 3.7 0 0 1 5.2 5.2l-8.5 8.5a1.8 1.8 0 0 1-2.6-2.6l7.8-7.8" />
    </Glyph>
  );
}

export function SendIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M4.7 4.3 20 12 4.7 19.7 7 12Z" />
    </Glyph>
  );
}

export function PencilIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M12 20h9" />
      <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z" />
    </Glyph>
  );
}

export function TrashIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M3 6h18" />
      <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6" />
      <path d="M10 11v6" />
      <path d="M14 11v6" />
    </Glyph>
  );
}

export function LinkIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1" />
      <path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1" />
    </Glyph>
  );
}

export function FileTextIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z" />
      <path d="M14 2v5h6" />
    </Glyph>
  );
}

export function ImageIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <circle cx="9" cy="9" r="2" />
      <path d="m21 15-4.5-4.5L3 21" />
    </Glyph>
  );
}

export function DownloadIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
      <path d="m7 10 5 5 5-5" />
      <path d="M12 15V3" />
    </Glyph>
  );
}

export function MenuIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M4 6h16" />
      <path d="M4 12h16" />
      <path d="M4 18h16" />
    </Glyph>
  );
}

export function ShieldIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M20 13c0 5-3.5 7.5-7.7 9a1 1 0 0 1-.6 0C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.2-2.7a1 1 0 0 1 1.3 0C14.3 3.8 16.8 5 18.8 5a1 1 0 0 1 1 1Z" />
    </Glyph>
  );
}

export function SettingsIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="12" r="3" />
      <path d="M12.2 2h-.4a2 2 0 0 0-2 2 2 2 0 0 1-1 1.7l-.4.2a2 2 0 0 1-2 0 2 2 0 0 0-2.7.7l-.2.4a2 2 0 0 0 .7 2.7 2 2 0 0 1 1 1.7v.4a2 2 0 0 1-1 1.7 2 2 0 0 0-.7 2.7l.2.4a2 2 0 0 0 2.7.7 2 2 0 0 1 2 0l.4.2a2 2 0 0 1 1 1.7 2 2 0 0 0 2 2h.4a2 2 0 0 0 2-2 2 2 0 0 1 1-1.7l.4-.2a2 2 0 0 1 2 0 2 2 0 0 0 2.7-.7l.2-.4a2 2 0 0 0-.7-2.7 2 2 0 0 1-1-1.7v-.4a2 2 0 0 1 1-1.7 2 2 0 0 0 .7-2.7l-.2-.4a2 2 0 0 0-2.7-.7 2 2 0 0 1-2 0l-.4-.2a2 2 0 0 1-1-1.7 2 2 0 0 0-2-2Z" />
    </Glyph>
  );
}

export function EllipsisVerticalIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="5" r="1" />
      <circle cx="12" cy="12" r="1" />
      <circle cx="12" cy="19" r="1" />
    </Glyph>
  );
}

export function WifiOffIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M2 2l20 20" />
      <path d="M8.5 16.4a5 5 0 0 1 7 0" />
      <path d="M5 12.9a10 10 0 0 1 3-2" />
      <path d="M19 12.9a10 10 0 0 0-7.5-2.8" />
      <path d="M12 20h.01" />
    </Glyph>
  );
}

export function PlusIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M12 5v14" />
      <path d="M5 12h14" />
    </Glyph>
  );
}

export function XIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </Glyph>
  );
}

/** The "Waiting to send" marker beside a queued message. */
export function ClockIcon(props: IconProps) {
  return (
    <Glyph {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </Glyph>
  );
}

/** The instance mark in the sidebar header — two offset rounded squares. */
export function NestMark({ size, className }: { size: number; className?: string }) {
  return (
    <svg
      viewBox="0 0 32 32"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
      className={className}
    >
      <rect x="2" y="2" width="17" height="17" rx="4" fill="var(--color-accent-warm)" />
      <rect
        x="13"
        y="13"
        width="17"
        height="17"
        rx="4"
        fill="none"
        stroke="var(--color-brand-primary)"
        strokeWidth="2"
      />
    </svg>
  );
}
