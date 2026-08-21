import type { Ref } from "react";

import { CircleAlertIcon, TriangleAlertIcon } from "../icons";

interface NoticeBannerProps {
  /**
   * danger — a failure the user must act on, announced with role="alert".
   * warning — a non-dismissable condition, announced politely.
   */
  tone: "danger" | "warning";
  message: string;
  ref?: Ref<HTMLDivElement>;
}

/**
 * Form-level notice. Always icon plus text, so the meaning never rests on
 * colour. There is no close control: a notice disappears when the condition
 * that produced it does.
 */
export function NoticeBanner({ tone, message, ref }: NoticeBannerProps) {
  const Icon = tone === "danger" ? CircleAlertIcon : TriangleAlertIcon;

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
