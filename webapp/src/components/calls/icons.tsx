import type { IconProps } from "../icons";

/**
 * The call glyphs, transcribed from "Hamlaneh Chat Calls.dc.html" — the same
 * Lucide family, 24x24 viewBox, currentColor, round caps and joins as
 * `components/icons.tsx`.
 *
 * They live beside the surface that uses them rather than in the shared set
 * only because this slice owns `components/calls/` and nothing else; folding
 * them into `components/icons.tsx` is a later tidy, not a decision.
 *
 * Not here on purpose: monitor (screen share), users, loader and circle-alert
 * are already in the shared set and are imported from it.
 *
 * NONE OF THESE MIRROR. The handoff is explicit: microphone, camera, monitor,
 * users and phone-off are objects or states, not directions, and the slash on
 * an off-glyph is a fixed diagonal that reads as a different mark reversed. So
 * no call glyph ever carries `hm-mirror-glyph`.
 */

function CallGlyph({
  size,
  strokeWidth = 1.75,
  className,
  children,
}: IconProps & { children: React.ReactNode }) {
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

export function MicIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="M12 19v3" />
      <path d="M8 22h8" />
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M5 10a7 7 0 0 0 14 0" />
    </CallGlyph>
  );
}

export function MicOffIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="m2 2 20 20" />
      <path d="M12 19v3" />
      <path d="M8 22h8" />
      <path d="M15 9.3V5a3 3 0 0 0-5.7-1.3" />
      <path d="M9 9v2a3 3 0 0 0 4.6 2.5" />
      <path d="M5 10a7 7 0 0 0 10.7 6" />
    </CallGlyph>
  );
}

export function VideoIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="m22 8-6 4 6 4V8Z" />
      <rect x="2" y="6" width="14" height="12" rx="2" />
    </CallGlyph>
  );
}

export function VideoOffIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="m2 2 20 20" />
      <path d="M22 8l-6 4 6 4V8Z" />
      <path d="M14 6h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h8" />
    </CallGlyph>
  );
}

export function PhoneOffIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="M13.8 2.6a2 2 0 0 0-2.4.6l-1.3 1.7a2 2 0 0 0 .1 2.5l1.4 1.4a12 12 0 0 1-2.9 2.9L7.3 10.3a2 2 0 0 0-2.5-.1l-1.7 1.3a2 2 0 0 0-.6 2.4 16 16 0 0 0 8 8" />
      <path d="m2 2 20 20" />
      <path d="M22 9a12 12 0 0 0-9-7" />
    </CallGlyph>
  );
}

/** The glyph for a call being placed — the idle banner, and the ring. */
export function PhoneOutgoingIcon(props: IconProps) {
  return (
    <CallGlyph {...props}>
      <path d="M13.8 2.6a2 2 0 0 0-2.4.6l-1.3 1.7a2 2 0 0 0 .1 2.5l1.4 1.4a12 12 0 0 1-2.9 2.9L7.3 10.3a2 2 0 0 0-2.5-.1l-1.7 1.3a2 2 0 0 0-.6 2.4 16 16 0 0 0 8 8 2 2 0 0 0 2.4-.6l1.3-1.7a2 2 0 0 0-.1-2.5l-1.4-1.4" />
      <path d="M14.5 9.5 22 2" />
      <path d="M16 2h6v6" />
    </CallGlyph>
  );
}
