import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { listAuditEntries } from "../../admin/adminApi";
import type { AuditEntry } from "../../admin/adminApi";
import { useAdminResource } from "../../admin/useAdminResource";
import type { User } from "../../chat/types";
import { AdminLoadFailed, AdminStateCard, TableSkeleton } from "./AdminStates";
import { AdminShell } from "./AdminShell";
import { NoticeBanner } from "../auth/NoticeBanner";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  DownloadIcon,
  RefreshCwIcon,
  ScrollTextIcon,
} from "../icons";
import { SettingsButton } from "../settings/SettingsButton";

const COLUMNS = 5;

/** Actions whose tint the design calls out: destructive red, changes amber. */
const DANGER = /\.(deleted|deactivated|revoked|removed|failed)$/u;
const WARNING = /\.(role_changed|settings_changed|changed|reset)$/u;

function actionTone(action: string): "danger" | "warning" | "neutral" {
  if (DANGER.test(action)) {
    return "danger";
  }
  return WARNING.test(action) ? "warning" : "neutral";
}

/**
 * "21 Aug 09:41:07" — the log's own stamp, in Latin digits and mono, as the
 * Persian artboard sets every clock reading.
 */
function auditStamp(iso: string, locale: string): string {
  const value = new Date(iso);
  if (Number.isNaN(value.getTime())) {
    return "";
  }
  return new Intl.DateTimeFormat(locale.startsWith("fa") ? "fa-u-nu-latn" : locale, {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(value);
}

/** One CSV field: quoted, with embedded quotes doubled (RFC 4180). */
function csvField(value: string): string {
  return `"${value.replaceAll('"', '""')}"`;
}

interface AdminAuditLogProps {
  currentUser: User;
  organizationName: string;
}

/**
 * "Audit log" — `admin-audit-log`. Append-only and hash-chained: `chain_valid`
 * false is not a display concern, it means somebody reached the database, so
 * the screen says so at the top rather than tinting a row.
 *
 * Newest first, older/newer paging rather than numbered pages, because
 * entries arrive while a page is being read.
 */
export function AdminAuditLog({ currentUser, organizationName }: AdminAuditLogProps) {
  const { t, i18n } = useTranslation();

  /** Cursors already used, newest page first — the "Newer" path back. */
  const [trail, setTrail] = useState<(string | undefined)[]>([undefined]);
  const [action, setAction] = useState("");
  const [actorId, setActorId] = useState("");

  const cursor = trail.at(-1);
  const page = useAdminResource(
    useCallback(
      () =>
        listAuditEntries({
          cursor,
          ...(action === "" ? {} : { action }),
          ...(actorId === "" ? {} : { actorId }),
        }),
      [cursor, action, actorId],
    ),
  );

  const entries: readonly AuditEntry[] =
    page.state.status === "ready" ? page.state.data.entries : [];

  /* The contract has no vocabulary endpoint, so the filters offer what the
   * loaded page actually contains.
   *
   * Derived, not accumulated. Carrying a running union across pages meant
   * either an effect that setStates on every load — a second render to show
   * what the first already had — or a ref read during render, which React
   * forbids outright. Both were solving a problem the screen does not have:
   * the filters exist to narrow what is on screen, and an option that
   * matches nothing here would filter to an empty table. What it costs is
   * that paging changes the options; what it buys is that they are always
   * true of what the reader is looking at.
   */
  const seenActions = useMemo(
    () =>
      [...new Set(entries.map((entry) => entry.action))].sort((left, right) =>
        left.localeCompare(right),
      ),
    [entries],
  );

  const seenActors = useMemo(() => {
    const byId = new Map<string, { id: string; name: string }>();
    for (const entry of entries) {
      if (entry.actor != null) {
        byId.set(entry.actor.id, { id: entry.actor.id, name: entry.actor.display_name });
      }
    }
    return [...byId.values()].sort((left, right) => left.name.localeCompare(right.name));
  }, [entries]);

  const nextCursor = page.state.status === "ready" ? page.state.data.next_cursor : undefined;
  const chainValid = page.state.status !== "ready" || page.state.data.chain_valid;

  const csv = useMemo(() => {
    const header = [
      t("admin.audit.column.time"),
      t("admin.audit.column.actor"),
      t("admin.audit.column.action"),
      t("admin.audit.column.target"),
      t("admin.audit.column.ip"),
    ];
    const rows = entries.map((entry) => [
      entry.occurred_at,
      entry.actor?.display_name ?? t("admin.audit.system"),
      entry.action,
      entry.target_label ?? entry.target_id ?? "",
      entry.ip ?? "",
    ]);
    return [header, ...rows].map((row) => row.map(csvField).join(",")).join("\r\n");
  }, [entries, t]);

  const exportCsv = () => {
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "hamlaneh-audit.csv";
    anchor.click();
    URL.revokeObjectURL(url);
  };

  const resetPaging = () => {
    setTrail([undefined]);
  };

  return (
    <AdminShell
      currentUser={currentUser}
      organizationName={organizationName}
      title={t("admin.audit.title")}
      subtitle={t("admin.audit.subtitle")}
      action={
        <SettingsButton
          tone="primary"
          label={t("admin.audit.export")}
          icon={<DownloadIcon size={17} strokeWidth={1.85} />}
          disabled={entries.length === 0}
          onClick={exportCsv}
        />
      }
      filters={
        <>
          <select
            className="hm-admin-select"
            aria-label={t("admin.audit.actorFilter")}
            value={actorId}
            onChange={(event) => {
              setActorId(event.target.value);
              resetPaging();
            }}
          >
            <option value="">{t("admin.audit.actorAny")}</option>
            {seenActors.map((actor) => (
              <option key={actor.id} value={actor.id}>
                {actor.name}
              </option>
            ))}
          </select>
          <select
            className="hm-admin-select"
            aria-label={t("admin.audit.actionFilter")}
            value={action}
            onChange={(event) => {
              setAction(event.target.value);
              resetPaging();
            }}
          >
            <option value="">{t("admin.audit.actionAll")}</option>
            {seenActions.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </>
      }
    >
      {chainValid ? null : (
        <NoticeBanner tone="danger" message={t("admin.audit.chainBroken")} />
      )}

      {page.state.status === "error" ? (
        <AdminLoadFailed
          title={t("admin.audit.loadFailed")}
          body={t("admin.error.loadBody")}
          retry={
            <SettingsButton
              tone="primary"
              label={t("admin.error.retry")}
              icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
              onClick={page.reload}
            />
          }
        />
      ) : page.state.status === "ready" && entries.length === 0 ? (
        <AdminStateCard
          icon={<ScrollTextIcon size={22} strokeWidth={1.6} />}
          tone="neutral"
          title={t("admin.audit.empty.title")}
          body={t("admin.audit.empty.body")}
        />
      ) : (
        <div className="hm-admin-card hm-admin-scroll">
          <table className="hm-admin-table">
            <caption className="hm-visually-hidden">{t("admin.audit.title")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("admin.audit.column.time")}</th>
                <th scope="col">{t("admin.audit.column.actor")}</th>
                <th scope="col">{t("admin.audit.column.action")}</th>
                <th scope="col">{t("admin.audit.column.target")}</th>
                <th scope="col">{t("admin.audit.column.ip")}</th>
              </tr>
            </thead>
            {page.state.status === "loading" ? (
              <TableSkeleton columns={COLUMNS} />
            ) : (
              <tbody>
                {entries.map((entry) => (
                  <tr key={entry.id}>
                    <td data-muted="true">
                      <span className="hm-admin-mono" dir="ltr">
                        {auditStamp(entry.occurred_at, i18n.language)}
                      </span>
                    </td>
                    <td>{entry.actor?.display_name ?? t("admin.audit.system")}</td>
                    <td>
                      <span
                        className={`hm-admin-tag hm-admin-tag--${actionTone(entry.action)}`}
                        dir="ltr"
                      >
                        {entry.action}
                      </span>
                    </td>
                    <td>
                      <span className="hm-admin-cell__truncate" dir="auto">
                        {entry.target_label ?? entry.target_id ?? "—"}
                      </span>
                    </td>
                    <td data-muted="true">
                      <span className="hm-admin-mono" dir="ltr">
                        {entry.ip ?? "—"}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            )}
          </table>
        </div>
      )}

      <div className="hm-admin-paging">
        <span className="hm-admin__tally">{t("admin.audit.pageLabel")}</span>
        <div className="hm-admin-paging__buttons">
          <SettingsButton
            size="sm"
            label={t("admin.audit.newer")}
            icon={<ChevronLeftIcon size={14} strokeWidth={2} />}
            disabled={trail.length <= 1}
            onClick={() => {
              setTrail((current) => current.slice(0, -1));
            }}
          />
          <SettingsButton
            size="sm"
            label={t("admin.audit.older")}
            iconEnd={<ChevronRightIcon size={14} strokeWidth={2} />}
            disabled={nextCursor === undefined}
            onClick={() => {
              if (nextCursor !== undefined) {
                setTrail((current) => [...current, nextCursor]);
              }
            }}
          />
        </div>
      </div>

      <span className="hm-admin__footnote">{t("admin.audit.footnote")}</span>
    </AdminShell>
  );
}
