import { useCallback, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { ConfirmDialog } from "./ConfirmDialog";
import { SettingsButton } from "./SettingsButton";
import { useFocusTrap } from "./useFocusTrap";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
// The generic loading / ready / failed machine the admin tables run on.
// Nothing in it is admin-specific — it is the retry-in-place state the design
// system draws — and a fourth copy of the same four lines here would only be a
// copy that can disagree.
import { useAdminResource } from "../../admin/useAdminResource";
import { formatActivationDate } from "../../settings/sessionTime";
import { CredentialsPanel } from "../admin/CredentialsPanel";
import { NoticeBanner } from "../auth/NoticeBanner";
import { EyeOffIcon, PlusIcon, RefreshCwIcon, XIcon } from "../icons";

type Conference = components["schemas"]["Conference"];
type CreatedConference = components["schemas"]["CreatedConference"];

/** The contract's own bound (openapi.yaml → CreateConferenceRequest.title). */
const TITLE_MAX = 120;

async function listConferences(): Promise<Conference[]> {
  const { data, response } = await api.GET("/api/v1/conferences");
  if (data === undefined) {
    throw new Error(`listing conferences failed: ${String(response.status)}`);
  }
  return data.conferences;
}

async function createConference(title: string): Promise<CreatedConference> {
  const { data, response } = await api.POST("/api/v1/conferences", { body: { title } });
  if (data === undefined) {
    throw new Error(`creating a conference failed: ${String(response.status)}`);
  }
  return data;
}

async function revokeConference(conferenceId: string): Promise<void> {
  const { response } = await api.DELETE("/api/v1/conferences/{conferenceId}", {
    params: { path: { conferenceId } },
  });
  if (response.status !== 204) {
    throw new Error(`revoking a conference failed: ${String(response.status)}`);
  }
}

/**
 * Whether anybody is in the room, as a shape and a word rather than a colour:
 * a filled dot with a ring plus "Someone is in it", a hollow dot plus "Nobody
 * in it".
 *
 * It deliberately does not pulse or animate. Nothing feeds this screen live —
 * no event, no poll — so movement would promise a liveness the value does not
 * have. The header above the table says when it was read instead.
 */
function Presence({ active }: { active: boolean }) {
  const { t } = useTranslation();

  return (
    <span className="hm-meetings-presence" data-active={active}>
      <span className="hm-meetings-presence__dot" aria-hidden="true" />
      {t(active ? "settings.meetings.live" : "settings.meetings.idle")}
    </span>
  );
}

/**
 * Creating a meeting link, as the header action's dialog rather than an inline
 * form — exactly as `Create invite link` does on the admin rail, and wearing
 * that dialog's delivered treatment. Creation ends in the show-once
 * credentials panel, so the act is already modal; an inline form would keep a
 * text field permanently open above a table whose whole point is the links
 * that already exist.
 */
function CreateMeetingDialog({
  busy,
  failed,
  onCreate,
  onCancel,
}: {
  busy: boolean;
  failed: boolean;
  onCreate: (title: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const titleId = useId();
  const fieldId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);
  const [title, setTitle] = useState("");

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
            <h3 className="hm-admin-modal__title" id={titleId}>
              {t("settings.meetings.create")}
            </h3>
            <p className="hm-admin-modal__subtitle">{t("settings.meetings.lede")}</p>
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
            onCreate(title);
          }}
        >
          <div className="hm-admin-modal__body">
            <div className="hm-field">
              <label className="hm-label" htmlFor={fieldId}>
                {t("settings.meetings.titleLabel")}
              </label>
              <input
                className="hm-input"
                id={fieldId}
                type="text"
                dir="auto"
                required
                autoComplete="off"
                maxLength={TITLE_MAX}
                value={title}
                onChange={(event) => {
                  setTitle(event.target.value);
                }}
              />
            </div>
            {failed ? (
              <NoticeBanner tone="danger" message={t("settings.meetings.actionFailed")} />
            ) : null}
          </div>
          <div className="hm-admin-modal__footer">
            <button
              type="submit"
              className="hm-settings-button hm-settings-button--primary"
              data-busy={busy}
              disabled={busy}
              aria-busy={busy}
            >
              {busy ? t("settings.working") : t("settings.meetings.createSubmit")}
            </button>
            <SettingsButton label={t("settings.cancel")} disabled={busy} onClick={onCancel} />
          </div>
        </form>
      </div>
    </div>
  );
}

/**
 * "Meetings" — the conference rooms this account owns, and their links.
 *
 * WHERE THIS LIVES, AND WHY HERE. Creating one is not an administrator's act:
 * any member may make a room and send the link (openapi.yaml → createConference
 * takes an ordinary session), so the admin dashboard — where invite links and
 * provisioning tokens live — is the one place it must not be. Settings is the
 * only other per-account surface in the product that every member can already
 * reach, and it is where a person goes to manage things that are *theirs*.
 * The row is appended after the drawn three, so `language`, `security` and
 * `appearance` keep the positions the artboard gives them; that is the same
 * move the SCIM section made on the admin rail.
 *
 * A RECORDS TABLE IS RIGHT, ONCE THE ABSENCE IS STATED. What people do here is
 * audit and revoke, which is row work. The link can never be a column — the
 * server keeps only a hash — so the table carries no blank link cell, no
 * disabled Copy and no tooltip explaining a missing value. The note sits above
 * the table, in the reader's path, before they start hunting for the column
 * (artboard `settings-meetings`).
 *
 * Everything visual is delivered: the admin table treatment, the destructive
 * row outline, the admin retry-in-place control, `CredentialsPanel` for the
 * show-once step every hash-only value goes through, and `ConfirmDialog` for a
 * revocation that disconnects whoever is in the room.
 */
export function MeetingsSection() {
  const { t, i18n } = useTranslation();
  const conferences = useAdminResource(useCallback(() => listConferences(), []));

  /**
   * The link, held for exactly as long as the panel is open. Clearing it on
   * acknowledgement IS the show-once gate: the server stores only a hash and
   * the list endpoint never carries a url, so once this is null there is
   * nothing on this instance that can put the link back on screen.
   */
  const [created, setCreated] = useState<CreatedConference | null>(null);
  const [creating, setCreating] = useState(false);
  const [confirming, setConfirming] = useState<Conference | null>(null);
  const [busy, setBusy] = useState(false);
  /** A flag rather than a message, so the failure follows a language switch. */
  const [failed, setFailed] = useState(false);

  const rows = conferences.state.status === "ready" ? conferences.state.data : [];

  const create = (title: string) => {
    const trimmed = title.trim();
    // `required` blocks an empty field; a field of spaces gets past it.
    if (busy || trimmed === "") {
      return;
    }
    setBusy(true);
    setFailed(false);
    void (async () => {
      try {
        setCreated(await createConference(trimmed));
        setCreating(false);
        conferences.reload();
      } catch (requestError) {
        console.warn("Creating the meeting failed:", requestError);
        setFailed(true);
      } finally {
        setBusy(false);
      }
    })();
  };

  const revoke = (conference: Conference) => {
    setBusy(true);
    setFailed(false);
    setConfirming(null);
    void (async () => {
      try {
        await revokeConference(conference.id);
      } catch (requestError) {
        console.warn("Revoking the meeting failed:", requestError);
        setFailed(true);
      } finally {
        setBusy(false);
        conferences.reload();
      }
    })();
  };

  return (
    <>
      <div className="hm-settings__section-head">
        <div className="hm-settings__heading">
          <h3 className="hm-settings__title">{t("settings.meetings.title")}</h3>
          <p className="hm-settings__lede">{t("settings.meetings.lede")}</p>
        </div>
        <SettingsButton
          label={t("settings.meetings.create")}
          tone="primary"
          icon={<PlusIcon size={17} strokeWidth={2.1} />}
          disabled={busy}
          onClick={() => {
            setFailed(false);
            setCreating(true);
          }}
        />
      </div>

      <div className="hm-settings__scroll">
        {/* The missing column is the one people look for, so the page says why
            before they hunt for it: there is no "copy link" here, and there
            cannot be. */}
        <div className="hm-settings-note-banner" data-tone="neutral">
          <EyeOffIcon size={17} strokeWidth={1.75} className="hm-settings-note-banner__icon" />
          <span>
            <strong>{t("settings.meetings.noLinkColumn")}</strong>{" "}
            <span>{t("settings.meetings.linkOnceNote")}</span>
          </span>
        </div>

        {failed && !creating ? (
          <NoticeBanner tone="danger" message={t("settings.meetings.actionFailed")} />
        ) : null}

        {conferences.state.status === "loading" ? (
          <p className="hm-settings__note" role="status">
            {t("common.loading")}
          </p>
        ) : conferences.state.status === "error" ? (
          <>
            <NoticeBanner tone="danger" message={t("settings.meetings.loadFailed")} />
            <SettingsButton label={t("admin.error.retry")} onClick={conferences.reload} />
          </>
        ) : rows.length === 0 ? (
          <p className="hm-settings__note">{t("settings.meetings.empty")}</p>
        ) : (
          <>
            {/* Read once, when this panel opened — said in words, with the way
                to read it again beside them. */}
            <div className="hm-meetings-tablehead">
              <span className="hm-meetings-tablehead__label">
                {t("settings.meetings.yourLinks")}
              </span>
              <span className="hm-meetings-tablehead__divider" aria-hidden="true" />
              <span className="hm-meetings-tablehead__read">
                {t("settings.meetings.readWhenOpened")}
              </span>
              <SettingsButton
                label={t("settings.meetings.refresh")}
                icon={<RefreshCwIcon size={15} strokeWidth={2} />}
                onClick={conferences.reload}
              />
            </div>

            <div className="hm-admin-card">
              <div className="hm-admin-scroll">
                <table
                  className="hm-admin-table hm-meetings-table"
                  aria-label={t("settings.meetings.yourLinks")}
                >
                  <thead>
                    <tr>
                      <th scope="col">{t("settings.meetings.column.title")}</th>
                      <th scope="col">{t("settings.meetings.column.createdBy")}</th>
                      <th scope="col">{t("settings.meetings.column.created")}</th>
                      <th scope="col">{t("settings.meetings.column.expires")}</th>
                      <th scope="col">{t("settings.meetings.column.status")}</th>
                      <th scope="col">{t("settings.meetings.column.actions")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((conference) => (
                      <tr key={conference.id}>
                        <td dir="auto">
                          <span className="hm-meetings-title">{conference.title}</span>
                        </td>
                        {/* Null when the account that made it is gone; an
                            administrator's list can hold those. */}
                        <td>
                          {conference.created_by?.display_name ??
                            t("settings.meetings.creatorGone")}
                        </td>
                        <td>
                          <span className="hm-admin-mono" dir="ltr">
                            {formatActivationDate(conference.created_at, i18n.language)}
                          </span>
                        </td>
                        {conference.expires_at == null ? (
                          <td data-muted="true">{t("settings.meetings.noExpiry")}</td>
                        ) : (
                          <td>
                            <span className="hm-admin-mono" dir="ltr">
                              {formatActivationDate(conference.expires_at, i18n.language)}
                            </span>
                          </td>
                        )}
                        <td>
                          <Presence active={conference.active} />
                        </td>
                        <td>
                          <SettingsButton
                            label={t("settings.meetings.revoke")}
                            tone="danger"
                            disabled={busy}
                            onClick={() => {
                              setConfirming(conference);
                            }}
                          />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          </>
        )}
      </div>

      {creating ? (
        <CreateMeetingDialog
          busy={busy}
          failed={failed}
          onCreate={create}
          onCancel={() => {
            setCreating(false);
            setFailed(false);
          }}
        />
      ) : null}

      {created === null ? null : (
        <CredentialsPanel
          title={t("settings.meetings.created.title")}
          lede={t("settings.meetings.created.lede", { title: created.conference.title })}
          warning={t("settings.meetings.created.warning")}
          note={t("settings.meetings.created.note")}
          filename="hamlaneh-meeting-link"
          values={[
            {
              label: t("settings.meetings.created.linkLabel"),
              value: created.url,
              emphasis: true,
            },
          ]}
          onClose={() => {
            setCreated(null);
          }}
        />
      )}

      {confirming === null ? null : (
        <ConfirmDialog
          title={t("settings.meetings.revokeConfirm.title")}
          body={t("settings.meetings.revokeConfirm.body")}
          confirmLabel={t("settings.meetings.revoke")}
          busyLabel={t("settings.working")}
          cancelLabel={t("settings.cancel")}
          onCancel={() => {
            setConfirming(null);
          }}
          onConfirm={() => {
            revoke(confirming);
          }}
        />
      )}
    </>
  );
}
