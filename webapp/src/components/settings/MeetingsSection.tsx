import { useCallback, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { ConfirmDialog } from "./ConfirmDialog";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
// The generic loading / ready / failed machine the admin tables run on.
// Nothing in it is admin-specific — it is the retry-in-place state the design
// system draws — and a fourth copy of the same four lines here would only be a
// copy that can disagree.
import { useAdminResource } from "../../admin/useAdminResource";
import { formatActivationDate } from "../../settings/sessionTime";
import { CredentialsPanel } from "../admin/CredentialsPanel";

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
 * UNDESIGNED SURFACE — no artboard draws this, so the body is plain semantic
 * HTML with none of the delivered card, table or button treatments
 * (docs/design/STATUS.md, "Meetings"). Two things are reused, and only because
 * each is an act already drawn: `CredentialsPanel` is the show-once step every
 * value the server keeps only a hash of goes through, and `ConfirmDialog` is
 * the confirm invite and token revocation already use.
 */
export function MeetingsSection() {
  const { t, i18n } = useTranslation();
  const conferences = useAdminResource(useCallback(() => listConferences(), []));
  const titleId = useId();

  const [title, setTitle] = useState("");
  /**
   * The link, held for exactly as long as the panel is open. Clearing it on
   * acknowledgement IS the show-once gate: the server stores only a hash and
   * the list endpoint never carries a url, so once this is null there is
   * nothing on this instance that can put the link back on screen.
   */
  const [created, setCreated] = useState<CreatedConference | null>(null);
  const [confirming, setConfirming] = useState<Conference | null>(null);
  const [busy, setBusy] = useState(false);
  /** A flag rather than a message, so the failure follows a language switch. */
  const [failed, setFailed] = useState(false);

  const rows = conferences.state.status === "ready" ? conferences.state.data : [];

  const create = () => {
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
        setTitle("");
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
    <section>
      <h3>{t("settings.meetings.title")}</h3>
      <p>{t("settings.meetings.lede")}</p>

      <form
        onSubmit={(event) => {
          event.preventDefault();
          create();
        }}
      >
        <label htmlFor={titleId}>{t("settings.meetings.titleLabel")}</label>
        <input
          id={titleId}
          type="text"
          required
          autoComplete="off"
          maxLength={TITLE_MAX}
          value={title}
          onChange={(event) => {
            setTitle(event.target.value);
          }}
        />
        <button type="submit" disabled={busy}>
          {busy ? t("settings.working") : t("settings.meetings.create")}
        </button>
      </form>

      {failed ? <p role="alert">{t("settings.meetings.actionFailed")}</p> : null}

      {conferences.state.status === "loading" ? (
        <p role="status">{t("common.loading")}</p>
      ) : conferences.state.status === "error" ? (
        <>
          <p role="alert">{t("settings.meetings.loadFailed")}</p>
          <button type="button" onClick={conferences.reload}>
            {t("admin.error.retry")}
          </button>
        </>
      ) : rows.length === 0 ? (
        <p>{t("settings.meetings.empty")}</p>
      ) : (
        <>
          <table>
            <caption>{t("settings.meetings.title")}</caption>
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
                  <td dir="auto">{conference.title}</td>
                  {/* Null when the account that made it is gone; an
                      administrator's list can hold those. */}
                  <td>
                    {conference.created_by?.display_name ?? t("settings.meetings.creatorGone")}
                  </td>
                  <td>{formatActivationDate(conference.created_at, i18n.language)}</td>
                  <td>
                    {conference.expires_at == null
                      ? t("settings.meetings.noExpiry")
                      : formatActivationDate(conference.expires_at, i18n.language)}
                  </td>
                  <td>
                    {t(conference.active ? "settings.meetings.live" : "settings.meetings.idle")}
                  </td>
                  <td>
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => {
                        setConfirming(conference);
                      }}
                    >
                      {t("settings.meetings.revoke")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* Said on the page, because the missing column is the one people
              look for: there is no "copy link" here and there cannot be. */}
          <p>{t("settings.meetings.linkOnceNote")}</p>
        </>
      )}

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
    </section>
  );
}
