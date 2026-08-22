import type { Ref } from "react";

import { CircleAlertIcon, CircleCheckIcon, InfoIcon, TriangleAlertIcon } from "../icons";

/**
 * danger  — a failure the user must act on, announced with role="alert".
 * warning — a non-dismissable condition, announced politely.
 * success — an outcome to confirm, announced politely.
 * info    — a standing fact about the screen, announced politely.
 *
 * Only `danger` interrupts: the reset confirmation must not steal focus
 * (LOGIN_HANDOFF: `role="status"` — does not steal focus).
 */
export type NoticeTone = "danger" | "warning" | "success" | "info";

interface NoticeBannerProps {
  tone: NoticeTone;
  message: string;
  ref?: Ref<HTMLDivElement>;
}

const TONE_ICON = {
  danger: CircleAlertIcon,
  warning: TriangleAlertIcon,
  success: CircleCheckIcon,
  info: InfoIcon,
} as const;

/**
 * Form-level notice. Always icon plus text, so the meaning never rests on
 * colour. There is no close control: a notice disappears when the condition
 * that produced it does.
 */
export function NoticeBanner({ tone, message, ref }: NoticeBannerProps) {
  const Icon = TONE_ICON[tone];

  return (
    <div
      ref={ref}
      // tabIndex lets a failed submission move focus here without adding the
      // banner to the tab order.
      tabIndex={-1}
      role={tone === "danger" ? "alert" : "status"}
      className={`hm-banner hm-banner--${tone}`}
    >
      <Icon size={18} className="hm-banner__icon" />
      <span className="hm-banner__text">{message}</span>
    </div>
  );
}
