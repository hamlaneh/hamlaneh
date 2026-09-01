import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { CircleAlertIcon } from "../icons";

interface AdminStateCardProps {
  icon: ReactNode;
  tone?: "brand" | "neutral" | "danger";
  title: string;
  body: string;
  actions?: ReactNode;
  /** The fresh-install state, which the artboard draws at modal width. */
  wide?: boolean;
}

/**
 * The empty / filtered-to-nothing / load-failed card from
 * `admin-components` §04. Empty-because-nothing-exists and
 * empty-because-filtered are different states with different copy and
 * different actions, so this only draws the card — the caller decides which.
 */
export function AdminStateCard({
  icon,
  tone = "brand",
  title,
  body,
  actions,
  wide = false,
}: AdminStateCardProps) {
  return (
    <div className={`hm-admin-state${wide ? " hm-admin-state--wide" : ""}`}>
      <span className="hm-admin-state__glyph" data-tone={tone}>
        {icon}
      </span>
      <div className="hm-admin-state__text">
        <h2 className="hm-admin-state__title">{title}</h2>
        <p className="hm-admin-state__body">{body}</p>
      </div>
      {actions === undefined ? null : <div className="hm-admin-state__actions">{actions}</div>}
    </div>
  );
}

/** The load-failed card, which is the same on every table. */
export function AdminLoadFailed({ title, body, retry }: {
  title: string;
  body: string;
  retry: ReactNode;
}) {
  return (
    <AdminStateCard
      icon={<CircleAlertIcon size={22} strokeWidth={1.6} />}
      tone="danger"
      title={title}
      body={body}
      actions={retry}
    />
  );
}

/**
 * Skeleton rows for a table whose header has already rendered — which is the
 * point of drawing them per row rather than replacing the whole card: column
 * widths never shift when the real rows arrive.
 */
export function TableSkeleton({ columns, rows = 5 }: { columns: number; rows?: number }) {
  const { t } = useTranslation();
  const widths = [130, 100, 120, 70, 96, 60];

  return (
    <tbody aria-busy="true">
      <tr>
        <td colSpan={columns}>
          <span className="hm-admin-hint" role="status">
            {t("common.loading")}
          </span>
        </td>
      </tr>
      {Array.from({ length: rows }, (_unused, row) => (
        <tr key={row} aria-hidden="true">
          {Array.from({ length: columns }, (_cell, column) => (
            <td key={column}>
              <span
                className="hm-admin-skeleton"
                style={{ inlineSize: `${String(widths[(row + column) % widths.length] ?? 90)}px` }}
              />
            </td>
          ))}
        </tr>
      ))}
    </tbody>
  );
}
