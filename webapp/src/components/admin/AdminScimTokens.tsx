import { useCallback, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { createScimToken, listScimTokens, revokeScimToken } from "../../admin/adminApi";
import type { CreatedScimToken, ScimToken } from "../../admin/adminApi";
import { useAdminResource } from "../../admin/useAdminResource";
import type { User } from "../../chat/types";
import { formatActivationDate } from "../../settings/sessionTime";
import { AdminShell } from "./AdminShell";
import { CredentialsPanel } from "./CredentialsPanel";
import { ConfirmDialog } from "../settings/ConfirmDialog";

interface AdminScimTokensProps {
  currentUser: User;
  organizationName: string;
}

/**
 * "Provisioning" — the bearer tokens an identity provider's sync engine
 * authenticates with (docs/api/scim.md §3). The one credential in this product
 * that belongs to a machine rather than a person: no cookie, no CSRF header,
 * refused everywhere under /api, and a session cookie is equally worthless at
 * the provisioning endpoints.
 *
 * UNDESIGNED SURFACE — no artboard draws this section, so the body is plain
 * semantic HTML with no styling beyond structure and none of the delivered
 * table, card or button treatments (docs/design/STATUS.md, "SCIM provisioning
 * tokens"). The two things that are reused are reused because they are the
 * same act, already drawn: `CredentialsPanel` is the show-once step every
 * value the server cannot redisplay goes through, and `ConfirmDialog` is the
 * confirm an invite revocation already uses.
 */
export function AdminScimTokens({ currentUser, organizationName }: AdminScimTokensProps) {
  const { t, i18n } = useTranslation();
  const tokens = useAdminResource(useCallback(() => listScimTokens(), []));
  const fieldId = useId();
  const hintId = useId();

  const [note, setNote] = useState("");
  /**
   * The minted token, held for exactly as long as the panel is open. Clearing
   * it on acknowledgement is the show-once gate: the value exists nowhere else
   * — not in the list, which the contract never sends it in — so once this is
   * null nothing can put it back on screen.
   */
  const [minted, setMinted] = useState<CreatedScimToken | null>(null);
  const [confirming, setConfirming] = useState<ScimToken | null>(null);
  const [busy, setBusy] = useState(false);
  /** A flag rather than a message, so the failure follows a language switch. */
  const [failed, setFailed] = useState(false);

  const rows = tokens.state.status === "ready" ? tokens.state.data : [];
  /** Dates are numbers, and every number the app generates is ASCII in Persian. */

  const mint = () => {
    setBusy(true);
    setFailed(false);
    void (async () => {
      try {
        const trimmed = note.trim();
        setMinted(await createScimToken(trimmed === "" ? {} : { note: trimmed }));
        setNote("");
        tokens.reload();
      } catch (requestError) {
        console.warn("Minting the provisioning token failed:", requestError);
        setFailed(true);
      } finally {
        setBusy(false);
      }
    })();
  };

  const revoke = (token: ScimToken) => {
    setBusy(true);
    setFailed(false);
    setConfirming(null);
    void (async () => {
      try {
        await revokeScimToken(token.id);
      } catch (requestError) {
        console.warn("Revoking the provisioning token failed:", requestError);
        setFailed(true);
      } finally {
        setBusy(false);
        tokens.reload();
      }
    })();
  };

  return (
    <AdminShell
      currentUser={currentUser}
      organizationName={organizationName}
      counts={{ scim: tokens.state.status === "ready" ? rows.length : undefined }}
      title={t("admin.scim.title")}
      subtitle={t("admin.scim.subtitle")}
    >
      <section>
        {/* Several live tokens at once is the supported way to rotate one, so
            the screen says so rather than letting it read as an oversight. */}
        <p>{t("admin.scim.overlap")}</p>

        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!busy) {
              mint();
            }
          }}
        >
          <label htmlFor={fieldId}>{t("admin.scim.noteLabel")}</label>
          <input
            id={fieldId}
            type="text"
            autoComplete="off"
            maxLength={200}
            aria-describedby={hintId}
            value={note}
            onChange={(event) => {
              setNote(event.target.value);
            }}
          />
          <p id={hintId}>{t("admin.scim.noteHint")}</p>
          <button type="submit" disabled={busy}>
            {busy ? t("admin.status.saving") : t("admin.scim.create")}
          </button>
        </form>

        {failed ? <p role="alert">{t("admin.error.actionFailed")}</p> : null}

        {tokens.state.status === "loading" ? (
          <p role="status">{t("common.loading")}</p>
        ) : tokens.state.status === "error" ? (
          <>
            <p role="alert">{t("admin.scim.loadFailed")}</p>
            <p>{t("admin.error.loadBody")}</p>
            <button type="button" onClick={tokens.reload}>
              {t("admin.error.retry")}
            </button>
          </>
        ) : rows.length === 0 ? (
          <p>{t("admin.scim.empty")}</p>
        ) : (
          <>
            <table>
              <caption>{t("admin.scim.title")}</caption>
              <thead>
                <tr>
                  <th scope="col">{t("admin.scim.column.note")}</th>
                  <th scope="col">{t("admin.scim.column.createdBy")}</th>
                  <th scope="col">{t("admin.scim.column.created")}</th>
                  <th scope="col">{t("admin.scim.column.lastUsed")}</th>
                  <th scope="col">{t("admin.scim.column.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((token) => {
                  // Null until the provider first authenticates: the one
                  // column that separates a token somebody configured from
                  // one that was minted and forgotten.
                  const lastUsed = token.last_used_at ?? null;
                  return (
                    <tr key={token.id}>
                      <td dir="auto">{token.note ?? t("admin.scim.noNote")}</td>
                      <td>{token.created_by.display_name}</td>
                      <td>{formatActivationDate(token.created_at, i18n.language)}</td>
                      <td>
                        {lastUsed === null
                          ? t("admin.scim.neverUsed")
                          : formatActivationDate(lastUsed, i18n.language)}
                      </td>
                      <td>
                        <button
                          type="button"
                          disabled={busy}
                          onClick={() => {
                            setConfirming(token);
                          }}
                        >
                          {t("admin.scim.revoke")}
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            <p>{t("admin.scim.lastUsedNote")}</p>
          </>
        )}
      </section>

      {minted === null ? null : (
        <CredentialsPanel
          title={t("admin.scim.created.title")}
          lede={t("admin.scim.created.lede")}
          warning={t("admin.scim.created.warning")}
          note={t("admin.scim.created.note")}
          filename="hamlaneh-scim-token"
          values={[
            {
              label: t("admin.scim.created.label"),
              value: minted.token,
              emphasis: true,
            },
          ]}
          onClose={() => {
            setMinted(null);
          }}
        />
      )}

      {confirming === null ? null : (
        <ConfirmDialog
          title={t("admin.scim.revokeConfirm.title")}
          body={t("admin.scim.revokeConfirm.body")}
          confirmLabel={t("admin.scim.revoke")}
          busyLabel={t("admin.status.saving")}
          cancelLabel={t("chat.common.cancel")}
          onCancel={() => {
            setConfirming(null);
          }}
          onConfirm={() => {
            revoke(confirming);
          }}
        />
      )}
    </AdminShell>
  );
}
