import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";

import {
  AdminError,
  forcePasswordReset,
  listUsers,
  updateUser,
} from "../../admin/adminApi";
import type { UserRow } from "../../admin/adminApi";
import { useAdminResource } from "../../admin/useAdminResource";
import { formatCount } from "../../chat/format";
import type { User } from "../../chat/types";
import { formatActivationDate } from "../../settings/sessionTime";
import { AdminLoadFailed, AdminStateCard, TableSkeleton } from "./AdminStates";
import { AdminShell } from "./AdminShell";
import { CreateUserDialog } from "./CreateUserDialog";
import type { CreatedAccount } from "./CreateUserDialog";
import { CredentialsPanel } from "./CredentialsPanel";
import { RowMenu } from "./RowMenu";
import type { RowMenuItem } from "./RowMenu";
import { Avatar } from "../chat/Avatar";
import {
  LoaderCircleIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  UserMinusIcon,
  UserPlusIcon,
} from "../icons";
import { ConfirmDialog } from "../settings/ConfirmDialog";
import { SettingsButton } from "../settings/SettingsButton";

const COLUMNS = 7;

type RoleFilter = "all" | "admin" | "member";
type StatusFilter = "all" | "active" | "inactive";

/** What is open over the table. */
type Overlay =
  | { kind: "none" }
  | { kind: "create" }
  | { kind: "credentials"; username: string; password: string; fromCreate: boolean; name: string }
  | { kind: "role"; user: UserRow }
  | { kind: "reset"; user: UserRow }
  | { kind: "deactivate"; user: UserRow }
  | { kind: "reactivate"; user: UserRow }
  /** The drawn "blocked, not warned" dialog: the action is named and refused. */
  | { kind: "blocked"; reason: "lastAdmin" | "selfDeactivate"; actionLabel: string };

interface AdminUsersProps {
  currentUser: User;
  organizationName: string;
}

function matchesQuery(row: UserRow, query: string): boolean {
  if (query === "") {
    return true;
  }
  const needle = query.toLocaleLowerCase();
  return [row.username, row.display_name, row.email ?? ""].some((field) =>
    field.toLocaleLowerCase().includes(needle),
  );
}

/**
 * "Users" — `admin-users`, `admin-users-empty` and the modal that opens over
 * them. Every account on the instance is born here or from an invite link.
 */
export function AdminUsers({ currentUser, organizationName }: AdminUsersProps) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const users = useAdminResource(useCallback(() => listUsers(), []));

  const [query, setQuery] = useState("");
  const [role, setRole] = useState<RoleFilter>("all");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [overlay, setOverlay] = useState<Overlay>({ kind: "none" });
  const [pendingId, setPendingId] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const rows = users.state.status === "ready" ? users.state.data : [];

  const visible = useMemo(
    () =>
      rows.filter(
        (row) =>
          matchesQuery(row, query.trim()) &&
          (role === "all" || (role === "admin") === row.is_admin) &&
          (status === "all" || (status === "active") === row.is_active),
      ),
    [rows, query, role, status],
  );

  /** An instance with one active admin cannot lose them — see the contract. */
  const activeAdmins = rows.filter((row) => row.is_admin && row.is_active).length;

  const closeOverlay = () => {
    setOverlay({ kind: "none" });
  };

  const run = (userId: string, action: () => Promise<UserRow>) => {
    setPendingId(userId);
    setFailure(null);
    closeOverlay();
    void (async () => {
      try {
        await action();
        users.reload();
      } catch (requestError) {
        if (requestError instanceof AdminError && requestError.code === "last_admin") {
          // Two admins, two tabs: the local guard was right a moment ago.
          setOverlay({
            kind: "blocked",
            reason: "lastAdmin",
            actionLabel: t("admin.users.action.changeRole"),
          });
        } else if (requestError instanceof AdminError && requestError.code === "self_deactivation") {
          setOverlay({
            kind: "blocked",
            reason: "selfDeactivate",
            actionLabel: t("admin.users.action.deactivate"),
          });
        } else {
          console.warn("The admin action failed:", requestError);
          setFailure(t("admin.error.actionFailed"));
        }
        users.reload();
      } finally {
        setPendingId(null);
      }
    })();
  };

  const menuFor = (row: UserRow): readonly RowMenuItem[] => {
    const isMe = row.id === currentUser.id;
    const isLastAdmin = row.is_admin && activeAdmins <= 1;
    const blocked = (reason: "lastAdmin" | "selfDeactivate", actionLabel: string) => () => {
      setOverlay({ kind: "blocked", reason, actionLabel });
    };

    return [
      {
        key: "role",
        label: t("admin.users.action.changeRole"),
        icon: <PencilIcon size={16} strokeWidth={1.85} />,
        onSelect: isLastAdmin
          ? blocked("lastAdmin", t("admin.users.action.changeRole"))
          : () => {
              setOverlay({ kind: "role", user: row });
            },
      },
      {
        key: "reset",
        label: t("admin.users.action.forceReset"),
        icon: <RefreshCwIcon size={16} strokeWidth={1.85} />,
        onSelect: () => {
          setOverlay({ kind: "reset", user: row });
        },
      },
      row.is_active
        ? {
            key: "deactivate",
            label: t("admin.users.action.deactivate"),
            icon: <UserMinusIcon size={16} strokeWidth={1.85} />,
            tone: "danger" as const,
            separated: true,
            onSelect:
              isMe || isLastAdmin
                ? blocked(
                    isMe ? "selfDeactivate" : "lastAdmin",
                    t("admin.users.action.deactivate"),
                  )
                : () => {
                    setOverlay({ kind: "deactivate", user: row });
                  },
          }
        : {
            // The deactivated row swaps ONLY the last item, so the menu never
            // reorders under the pointer. Reactivate is constructive.
            key: "reactivate",
            label: t("admin.users.action.reactivate"),
            icon: <UserPlusIcon size={16} strokeWidth={1.85} />,
            tone: "constructive" as const,
            separated: true,
            onSelect: () => {
              setOverlay({ kind: "reactivate", user: row });
            },
          },
    ];
  };

  const createAction = (
    <SettingsButton
      tone="primary"
      label={t("admin.users.create")}
      icon={<PlusIcon size={17} strokeWidth={2} />}
      onClick={() => {
        setOverlay({ kind: "create" });
      }}
    />
  );

  const onlyMe = users.state.status === "ready" && rows.length <= 1;

  return (
    <AdminShell
      currentUser={currentUser}
      organizationName={organizationName}
      counts={{ users: users.state.status === "ready" ? rows.length : undefined }}
      title={t("admin.users.title")}
      subtitle={t("admin.users.subtitle")}
      action={createAction}
      filters={
        onlyMe ? undefined : (
          <>
            <div className="hm-admin-search">
              <span className="hm-admin-search__icon">
                <SearchIcon size={16} />
              </span>
              <input
                className="hm-admin-search__input"
                type="search"
                aria-label={t("admin.users.searchLabel")}
                placeholder={t("admin.users.searchPlaceholder")}
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value);
                }}
              />
            </div>
            <select
              className="hm-admin-select"
              aria-label={t("admin.users.roleFilter")}
              value={role}
              onChange={(event) => {
                setRole(event.target.value as RoleFilter);
              }}
            >
              <option value="all">{t("admin.users.roleFilter.all")}</option>
              <option value="admin">{t("admin.role.admin")}</option>
              <option value="member">{t("admin.role.member")}</option>
            </select>
            <select
              className="hm-admin-select"
              aria-label={t("admin.users.statusFilter")}
              value={status}
              onChange={(event) => {
                setStatus(event.target.value as StatusFilter);
              }}
            >
              <option value="all">{t("admin.users.statusFilter.all")}</option>
              <option value="active">{t("admin.status.active")}</option>
              <option value="inactive">{t("admin.status.inactive")}</option>
            </select>
            <span className="hm-admin__tally">
              {t("admin.users.tally", {
                shown: formatCount(visible.length, i18n.language),
                total: formatCount(rows.length, i18n.language),
              })}
            </span>
          </>
        )
      }
    >
      {failure === null ? null : (
        <div className="hm-admin-note hm-admin-note--warning" role="alert">
          <span>{failure}</span>
        </div>
      )}

      {users.state.status === "error" ? (
        <AdminLoadFailed
          title={t("admin.users.loadFailed")}
          body={t("admin.error.loadBody")}
          retry={
            <SettingsButton
              tone="primary"
              label={t("admin.error.retry")}
              icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
              onClick={users.reload}
            />
          }
        />
      ) : onlyMe ? (
        <AdminStateCard
          wide
          icon={<UserPlusIcon size={26} strokeWidth={1.6} />}
          title={t("admin.users.empty.title")}
          body={t("admin.users.empty.body")}
          actions={
            <>
              <SettingsButton
                tone="primary"
                size="sm"
                label={t("admin.users.empty.createFirst")}
                icon={<PlusIcon size={16} strokeWidth={2} />}
                onClick={() => {
                  setOverlay({ kind: "create" });
                }}
              />
              <SettingsButton
                size="sm"
                label={t("admin.users.empty.createInvite")}
                onClick={() => {
                  void navigate("/admin/invites");
                }}
              />
            </>
          }
        />
      ) : users.state.status === "ready" && visible.length === 0 ? (
        <AdminStateCard
          icon={<SearchIcon size={22} strokeWidth={1.6} />}
          tone="neutral"
          title={t("admin.users.filteredEmpty.title")}
          body={t("admin.users.filteredEmpty.body", {
            total: formatCount(rows.length, i18n.language),
          })}
          actions={
            <SettingsButton
              size="sm"
              label={t("admin.users.filteredEmpty.clear")}
              onClick={() => {
                setQuery("");
                setRole("all");
                setStatus("all");
              }}
            />
          }
        />
      ) : (
        <div className="hm-admin-card hm-admin-scroll">
          <table className="hm-admin-table">
            <caption className="hm-visually-hidden">{t("admin.users.title")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("admin.users.column.user")}</th>
                <th scope="col">{t("admin.users.column.displayName")}</th>
                <th scope="col">{t("admin.users.column.email")}</th>
                <th scope="col">{t("admin.users.column.role")}</th>
                <th scope="col">{t("admin.users.column.status")}</th>
                <th scope="col">{t("admin.users.column.created")}</th>
                <th scope="col">{t("admin.users.column.actions")}</th>
              </tr>
            </thead>
            {users.state.status === "loading" ? (
              <TableSkeleton columns={COLUMNS} />
            ) : (
              <tbody>
                {visible.map((row) => {
                  const pending = pendingId === row.id;
                  return (
                    <tr key={row.id}>
                      <td>
                        <span className="hm-admin-cell">
                          <Avatar
                            userId={row.id}
                            displayName={row.display_name}
                            size={26}
                            typeSize={10}
                          />
                          <span className="hm-admin-username" dir="ltr">
                            {row.username}
                          </span>
                          {row.id === currentUser.id ? (
                            <span className="hm-admin-you">{t("admin.users.you")}</span>
                          ) : null}
                        </span>
                      </td>
                      <td>{row.display_name}</td>
                      <td dir="ltr" data-muted="true">
                        {row.email ?? "—"}
                      </td>
                      <td>
                        <span
                          className={`hm-admin-tag hm-admin-tag--${row.is_admin ? "admin" : "member"}`}
                        >
                          {t(row.is_admin ? "admin.role.admin" : "admin.role.member")}
                        </span>
                      </td>
                      <td>
                        {pending ? (
                          <span className="hm-admin-status" data-state="pending">
                            <LoaderCircleIcon size={13} strokeWidth={2.4} className="hm-spinner" />
                            {t("admin.status.saving")}
                          </span>
                        ) : (
                          <span
                            className="hm-admin-status"
                            data-state={row.is_active ? "active" : "inactive"}
                          >
                            <span className="hm-admin-status__dot" aria-hidden="true" />
                            {t(row.is_active ? "admin.status.active" : "admin.status.inactive")}
                          </span>
                        )}
                      </td>
                      <td data-muted="true">
                        {formatActivationDate(row.created_at, i18n.language)}
                      </td>
                      <td>
                        <span className="hm-admin-cell hm-admin-cell--end">
                          <RowMenu
                            label={t("admin.users.actionsFor", { name: row.username })}
                            items={menuFor(row)}
                            disabled={pending}
                          />
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            )}
          </table>
        </div>
      )}

      {overlay.kind === "create" ? (
        <CreateUserDialog
          onCancel={closeOverlay}
          onCreated={({ user, temporaryPassword }: CreatedAccount) => {
            users.reload();
            setOverlay({
              kind: "credentials",
              username: user.username,
              password: temporaryPassword,
              fromCreate: true,
              name: user.display_name,
            });
          }}
        />
      ) : null}

      {overlay.kind === "credentials" ? (
        <CredentialsPanel
          title={t(
            overlay.fromCreate ? "admin.credentials.created" : "admin.credentials.resetDone",
          )}
          lede={t(
            overlay.fromCreate ? "admin.credentials.createdLede" : "admin.credentials.resetLede",
            { name: overlay.name },
          )}
          warning={t("admin.credentials.warning")}
          note={t("admin.credentials.note")}
          filename={`hamlaneh-${overlay.username}`}
          values={[
            { label: t("admin.credentials.usernameLabel"), value: overlay.username },
            {
              label: t("admin.credentials.passwordLabel"),
              value: overlay.password,
              emphasis: true,
            },
          ]}
          {...(overlay.fromCreate
            ? {
                onCreateAnother: () => {
                  setOverlay({ kind: "create" });
                },
              }
            : {})}
          onClose={closeOverlay}
        />
      ) : null}

      {overlay.kind === "role" ? (
        <ConfirmDialog
          tone="primary"
          title={t(
            overlay.user.is_admin ? "admin.users.demote.title" : "admin.users.promote.title",
            { name: overlay.user.display_name },
          )}
          body={t(overlay.user.is_admin ? "admin.users.demote.body" : "admin.users.promote.body")}
          confirmLabel={t(
            overlay.user.is_admin ? "admin.users.demote.confirm" : "admin.users.promote.confirm",
          )}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={closeOverlay}
          onConfirm={() => {
            const target = overlay.user;
            run(target.id, () => updateUser(target.id, { is_admin: !target.is_admin }));
          }}
        />
      ) : null}

      {overlay.kind === "reset" ? (
        <ConfirmDialog
          tone="primary"
          title={t("admin.users.reset.title")}
          /* States that the session SURVIVES — that sentence is the whole
             difference between this and Deactivate. */
          body={t("admin.users.reset.body", { name: overlay.user.display_name })}
          confirmLabel={t("admin.users.reset.confirm")}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={closeOverlay}
          onConfirm={() => {
            const target = overlay.user;
            setPendingId(target.id);
            setFailure(null);
            closeOverlay();
            void (async () => {
              try {
                const credentials = await forcePasswordReset(target.id);
                setOverlay({
                  kind: "credentials",
                  username: credentials.username,
                  password: credentials.temporary_password,
                  fromCreate: false,
                  name: target.display_name,
                });
                users.reload();
              } catch (requestError) {
                console.warn("Forcing a password reset failed:", requestError);
                setFailure(t("admin.error.actionFailed"));
              } finally {
                setPendingId(null);
              }
            })();
          }}
        />
      ) : null}

      {overlay.kind === "deactivate" ? (
        <ConfirmDialog
          title={t("admin.users.deactivate.title", { name: overlay.user.display_name })}
          /* States that every session ENDS — the counterpart sentence. */
          body={t("admin.users.deactivate.body", { name: overlay.user.display_name })}
          confirmLabel={t("admin.users.action.deactivate")}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={closeOverlay}
          onConfirm={() => {
            const target = overlay.user;
            run(target.id, () => updateUser(target.id, { is_active: false }));
          }}
        />
      ) : null}

      {overlay.kind === "reactivate" ? (
        <ConfirmDialog
          tone="primary"
          title={t("admin.users.reactivate.title", { name: overlay.user.display_name })}
          body={t("admin.users.reactivate.body")}
          confirmLabel={t("admin.users.action.reactivate")}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={closeOverlay}
          onConfirm={() => {
            const target = overlay.user;
            run(target.id, () => updateUser(target.id, { is_active: true }));
          }}
        />
      ) : null}

      {overlay.kind === "blocked" ? (
        <ConfirmDialog
          tone="primary"
          confirmDisabled
          title={t(`admin.users.blocked.${overlay.reason}.title`)}
          body={t(`admin.users.blocked.${overlay.reason}.body`)}
          confirmLabel={overlay.actionLabel}
          busyLabel={overlay.actionLabel}
          cancelLabel={t("chat.common.close")}
          onCancel={closeOverlay}
          onConfirm={closeOverlay}
        />
      ) : null}
    </AdminShell>
  );
}
