import { useCallback, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { createInvite, listInvites, revokeInvite } from "../../admin/adminApi";
import type { CreatedInvite, Invite } from "../../admin/adminApi";
import { expiryLabel } from "../../admin/expiry";
import { useAdminResource } from "../../admin/useAdminResource";
import { formatCount } from "../../chat/format";
import type { User } from "../../chat/types";
import { formatActivationDate } from "../../settings/sessionTime";
import { AdminLoadFailed, AdminStateCard, TableSkeleton } from "./AdminStates";
import { AdminShell } from "./AdminShell";
import { CredentialsPanel } from "./CredentialsPanel";
import { NoticeBanner } from "../auth/NoticeBanner";
import { TextField } from "../auth/TextField";
import { InfoIcon, LinkIcon, PlusIcon, RefreshCwIcon, XIcon } from "../icons";
import { ConfirmDialog } from "../settings/ConfirmDialog";
import { SettingsButton } from "../settings/SettingsButton";
import { useFocusTrap } from "../settings/useFocusTrap";

const COLUMNS = 6;

/** The three lifetimes the dialog offers, inside the contract's 1..720 hours. */
const LIFETIMES = [24, 168, 720] as const;

/**
 * The label key for each offered lifetime, written out rather than built from
 * the number.
 *
 * A template-literal key types as `admin.invites.expiry.${string}`, which no
 * amount of correct JSON can make assignable to i18next's literal key union —
 * so the compiler cannot tell a key that exists from one that does not, and a
 * lifetime added here would render its own key string to the user. Spelled
 * out, adding one without its translation is a build error.
 */
const EXPIRY_LABELS = {
  24: "admin.invites.expiry.24",
  168: "admin.invites.expiry.168",
  720: "admin.invites.expiry.720",
} as const;

type Overlay =
  | { kind: "none" }
  | { kind: "create" }
  | { kind: "link"; invite: CreatedInvite }
  | { kind: "revoke"; invite: Invite };

interface AdminInvitesProps {
  currentUser: User;
  organizationName: string;
}

/**
 * "Invites" — `admin-invites`. Only links that are still usable are listed:
 * an accepted or expired invite leaves, because the table answers "what can
 * still be redeemed" and the audit log is where the history lives.
 */
export function AdminInvites({ currentUser, organizationName }: AdminInvitesProps) {
  const { t, i18n } = useTranslation();
  const invites = useAdminResource(useCallback(() => listInvites(), []));
  const [overlay, setOverlay] = useState<Overlay>({ kind: "none" });
  const [pendingId, setPendingId] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const rows = invites.state.status === "ready" ? invites.state.data : [];
  const closeOverlay = () => {
    setOverlay({ kind: "none" });
  };

  const revoke = (invite: Invite) => {
    setPendingId(invite.id);
    setFailure(null);
    closeOverlay();
    void (async () => {
      try {
        await revokeInvite(invite.id);
      } catch (requestError) {
        console.warn("Revoking the invite failed:", requestError);
        setFailure(t("admin.error.actionFailed"));
      } finally {
        setPendingId(null);
        invites.reload();
      }
    })();
  };

  const createAction = (
    <SettingsButton
      tone="primary"
      label={t("admin.invites.create")}
      icon={<PlusIcon size={17} strokeWidth={2} />}
      onClick={() => {
        setOverlay({ kind: "create" });
      }}
    />
  );

  return (
    <AdminShell
      currentUser={currentUser}
      organizationName={organizationName}
      counts={{ invites: invites.state.status === "ready" ? rows.length : undefined }}
      title={t("admin.invites.title")}
      subtitle={t("admin.invites.subtitle")}
      action={createAction}
      filters={
        invites.state.status === "ready" && rows.length > 0 ? (
          <span className="hm-admin__tally">
            {t("admin.invites.tally", { open: formatCount(rows.length, i18n.language) })}
          </span>
        ) : undefined
      }
    >
      {failure === null ? null : <NoticeBanner tone="danger" message={failure} />}

      {invites.state.status === "error" ? (
        <AdminLoadFailed
          title={t("admin.invites.loadFailed")}
          body={t("admin.error.loadBody")}
          retry={
            <SettingsButton
              tone="primary"
              label={t("admin.error.retry")}
              icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
              onClick={invites.reload}
            />
          }
        />
      ) : invites.state.status === "ready" && rows.length === 0 ? (
        <AdminStateCard
          icon={<LinkIcon size={22} strokeWidth={1.6} />}
          title={t("admin.invites.empty.title")}
          body={t("admin.invites.empty.body")}
          actions={
            <SettingsButton
              tone="primary"
              size="sm"
              label={t("admin.invites.create")}
              icon={<PlusIcon size={16} strokeWidth={2} />}
              onClick={() => {
                setOverlay({ kind: "create" });
              }}
            />
          }
        />
      ) : (
        <div className="hm-admin-card hm-admin-scroll">
          <table className="hm-admin-table">
            <caption className="hm-visually-hidden">{t("admin.invites.title")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("admin.invites.column.note")}</th>
                <th scope="col">{t("admin.invites.column.createdBy")}</th>
                <th scope="col">{t("admin.invites.column.created")}</th>
                <th scope="col">{t("admin.invites.column.expires")}</th>
                <th scope="col">{t("admin.invites.column.uses")}</th>
                <th scope="col">{t("admin.invites.column.actions")}</th>
              </tr>
            </thead>
            {invites.state.status === "loading" ? (
              <TableSkeleton columns={COLUMNS} rows={3} />
            ) : (
              <tbody>
                {rows.map((invite) => {
                  const expiry = expiryLabel(invite.expires_at, i18n.language);
                  return (
                    <tr key={invite.id}>
                      <td>
                        <span className="hm-admin-cell__truncate" dir="auto">
                          {invite.note ?? t("admin.invites.noNote")}
                        </span>
                      </td>
                      <td>{invite.created_by.display_name}</td>
                      <td data-muted="true">
                        {formatActivationDate(invite.created_at, i18n.language)}
                      </td>
                      <td data-muted={expiry.near ? undefined : "true"}>
                        {expiry.near ? (
                          <span className="hm-admin-tag hm-admin-tag--warning">{expiry.text}</span>
                        ) : (
                          expiry.text
                        )}
                      </td>
                      <td data-muted="true">{t("admin.invites.singleUse")}</td>
                      <td>
                        <span className="hm-admin-cell hm-admin-cell--end">
                          {/* Outlined danger, never a filled red button in a row. */}
                          <SettingsButton
                            tone="danger"
                            size="sm"
                            label={t("admin.invites.revoke")}
                            busy={pendingId === invite.id}
                            busyLabel={t("admin.status.saving")}
                            onClick={() => {
                              setOverlay({ kind: "revoke", invite });
                            }}
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

      <div className="hm-admin-note">
        <InfoIcon size={17} strokeWidth={1.85} className="hm-admin-note__icon" />
        <span>{t("admin.invites.note")}</span>
      </div>

      {overlay.kind === "create" ? (
        <CreateInviteDialog
          onCancel={closeOverlay}
          onCreated={(invite) => {
            invites.reload();
            setOverlay({ kind: "link", invite });
          }}
        />
      ) : null}

      {overlay.kind === "link" ? (
        <CredentialsPanel
          title={t("admin.invites.link.title")}
          lede={t("admin.invites.link.lede", {
            when: formatActivationDate(overlay.invite.expires_at, i18n.language),
          })}
          warning={t("admin.invites.link.warning")}
          note={t("admin.invites.link.note")}
          filename="hamlaneh-invite"
          values={[
            {
              label: t("admin.invites.link.label"),
              value: overlay.invite.url,
              emphasis: true,
            },
          ]}
          onClose={closeOverlay}
        />
      ) : null}

      {overlay.kind === "revoke" ? (
        <ConfirmDialog
          title={t("admin.invites.revokeConfirm.title")}
          body={t("admin.invites.revokeConfirm.body")}
          confirmLabel={t("admin.invites.revoke")}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={closeOverlay}
          onConfirm={() => {
            revoke(overlay.invite);
          }}
        />
      ) : null}
    </AdminShell>
  );
}

interface CreateInviteDialogProps {
  onCreated: (invite: CreatedInvite) => void;
  onCancel: () => void;
}

/** "Create invite link": a lifetime and an optional note, nothing else. */
function CreateInviteDialog({ onCreated, onCancel }: CreateInviteDialogProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const fieldId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);

  const [hours, setHours] = useState<number>(168);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  const submit = () => {
    if (busy) {
      return;
    }
    setBusy(true);
    setFailed(false);
    void (async () => {
      try {
        onCreated(
          await createInvite({
            expires_in_hours: hours,
            ...(note.trim() === "" ? {} : { note: note.trim() }),
          }),
        );
      } catch (requestError) {
        console.warn("Creating the invite failed:", requestError);
        setFailed(true);
        setBusy(false);
      }
    })();
  };

  return (
    <div className="hm-admin-modal__scrim">
      <div
        ref={dialogRef}
        className="hm-admin-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onCancel();
            return;
          }
          handleTrapKey(event);
        }}
      >
        <div className="hm-admin-modal__header">
          <div>
            <h2 className="hm-admin-modal__title" id={titleId}>
              {t("admin.invites.create")}
            </h2>
            <p className="hm-admin-modal__subtitle">{t("admin.invites.createSubtitle")}</p>
          </div>
          <button
            type="button"
            className="hm-admin-modal__close"
            aria-label={t("chat.common.close")}
            onClick={onCancel}
          >
            <XIcon size={18} strokeWidth={1.85} />
          </button>
        </div>

        <form
          className="hm-admin-modal__form"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="hm-admin-modal__body">
            <div className="hm-admin-field">
              <label className="hm-admin-field__label" htmlFor={`${fieldId}-hours`}>
                {t("admin.invites.expiryLabel")}
              </label>
              <select
                className="hm-admin-select"
                id={`${fieldId}-hours`}
                value={hours}
                onChange={(event) => {
                  setHours(Number(event.target.value));
                }}
              >
                {LIFETIMES.map((option) => (
                  <option key={option} value={option}>
                    {t(EXPIRY_LABELS[option])}
                  </option>
                ))}
              </select>
            </div>
            <TextField
              id={`${fieldId}-note`}
              label={t("admin.invites.noteLabel")}
              hint={t("admin.invites.noteHint")}
              autoComplete="off"
              value={note}
              onChange={setNote}
            />
            {failed ? <NoticeBanner tone="danger" message={t("admin.error.actionFailed")} /> : null}
          </div>
          <div className="hm-admin-modal__footer">
            <button
              type="submit"
              className="hm-settings-button hm-settings-button--primary"
              data-size="md"
              data-busy={busy}
              disabled={busy}
              aria-busy={busy}
            >
              {busy ? t("admin.status.saving") : t("admin.invites.createSubmit")}
            </button>
            <SettingsButton label={t("chat.common.cancel")} disabled={busy} onClick={onCancel} />
          </div>
        </form>
      </div>
    </div>
  );
}
