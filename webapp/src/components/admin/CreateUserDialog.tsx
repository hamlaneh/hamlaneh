import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { createUser, AdminError } from "../../admin/adminApi";
import type { UserRow } from "../../admin/adminApi";
import { generateTemporaryPassword } from "../../admin/password";
import { useInstance } from "../../instance/instanceInfo";
import { NoticeBanner } from "../auth/NoticeBanner";
import { TextField } from "../auth/TextField";
import { RefreshCwIcon, XIcon } from "../icons";
import { SettingsButton } from "../settings/SettingsButton";
import { useFocusTrap } from "../settings/useFocusTrap";

/** The contract's own rule (AdminCreateUserRequest.username). */
const USERNAME_PATTERN = /^[a-z0-9][a-z0-9_.-]*$/u;
const USERNAME_MIN = 3;
const USERNAME_MAX = 32;

export interface CreatedAccount {
  user: UserRow;
  temporaryPassword: string;
}

interface CreateUserDialogProps {
  onCreated: (account: CreatedAccount) => void;
  onCancel: () => void;
}

type FormError = "none" | "username" | "taken" | "invalid" | "unexpected";

/**
 * "Create user", the centred modal over the table (`admin-create-user`).
 *
 * The temporary password is generated in the browser because the contract
 * puts it in the request, not the response — the server never invents one for
 * this endpoint. It is shown once on the step that follows, and the caller
 * owns that step: this dialog's job ends at a created account.
 */
export function CreateUserDialog({ onCreated, onCancel }: CreateUserDialogProps) {
  const { t } = useTranslation();
  const { info } = useInstance();
  const titleId = useId();
  const fieldId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);

  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState(() =>
    generateTemporaryPassword(info.password_min_length),
  );
  const [isAdmin, setIsAdmin] = useState(false);
  const [locale, setLocale] = useState<"en" | "fa">("en");
  const [error, setError] = useState<FormError>("none");
  const [busy, setBusy] = useState(false);

  const submit = () => {
    if (busy) {
      return;
    }
    const trimmed = username.trim();
    if (
      trimmed.length < USERNAME_MIN ||
      trimmed.length > USERNAME_MAX ||
      !USERNAME_PATTERN.test(trimmed)
    ) {
      setError("username");
      return;
    }
    setBusy(true);
    setError("none");
    void (async () => {
      try {
        const user = await createUser({
          username: trimmed,
          password,
          locale,
          is_admin: isAdmin,
          ...(displayName.trim() === "" ? {} : { display_name: displayName.trim() }),
          ...(email.trim() === "" ? {} : { email: email.trim() }),
        });
        onCreated({ user, temporaryPassword: password });
      } catch (requestError) {
        if (requestError instanceof AdminError) {
          setError(requestError.status === 409 ? "taken" : "invalid");
        } else {
          console.warn("Creating the user failed:", requestError);
          setError("unexpected");
        }
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
              {t("admin.createUser.title")}
            </h2>
            <p className="hm-admin-modal__subtitle">{t("admin.createUser.subtitle")}</p>
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
          <TextField
            id={`${fieldId}-username`}
            label={t("admin.createUser.usernameLabel")}
            hint={t("admin.createUser.usernameHint")}
            error={error === "username" ? t("admin.createUser.error.username") : undefined}
            autoComplete="off"
            dir="ltr"
            value={username}
            onChange={setUsername}
          />
          <TextField
            id={`${fieldId}-display`}
            label={t("admin.createUser.displayNameLabel")}
            autoComplete="off"
            value={displayName}
            onChange={setDisplayName}
          />
          <TextField
            id={`${fieldId}-email`}
            label={t("admin.createUser.emailLabel")}
            hint={t("admin.createUser.emailHint")}
            type="email"
            autoComplete="off"
            dir="ltr"
            value={email}
            onChange={setEmail}
          />

          <div className="hm-field">
            <div className="hm-field__label-row">
              <label className="hm-label" htmlFor={`${fieldId}-password`}>
                {t("admin.createUser.passwordLabel")}
              </label>
            </div>
            {/* Generated value: mono and dir="ltr", so it reads correctly in
                a Persian interface and no character is ambiguous. */}
            <div className="hm-admin-generated">
              <output
                className="hm-admin-generated__value"
                id={`${fieldId}-password`}
                dir="ltr"
              >
                {password}
              </output>
              <SettingsButton
                label={t("admin.createUser.generate")}
                icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
                onClick={() => {
                  setPassword(generateTemporaryPassword(info.password_min_length));
                }}
              />
            </div>
            <span className="hm-admin-hint">
              {t("admin.createUser.passwordHint", { min: info.password_min_length })}
            </span>
          </div>

          <div className="hm-admin-modal__row">
            <div className="hm-admin-field">
              <label className="hm-admin-field__label" htmlFor={`${fieldId}-role`}>
                {t("admin.createUser.roleLabel")}
              </label>
              <select
                className="hm-admin-select"
                id={`${fieldId}-role`}
                value={isAdmin ? "admin" : "member"}
                onChange={(event) => {
                  setIsAdmin(event.target.value === "admin");
                }}
              >
                <option value="member">{t("admin.role.member")}</option>
                <option value="admin">{t("admin.role.admin")}</option>
              </select>
            </div>
            <div className="hm-admin-field">
              <label className="hm-admin-field__label" htmlFor={`${fieldId}-locale`}>
                {t("admin.createUser.languageLabel")}
              </label>
              <select
                className="hm-admin-select"
                id={`${fieldId}-locale`}
                value={locale}
                onChange={(event) => {
                  setLocale(event.target.value === "fa" ? "fa" : "en");
                }}
              >
                <option value="en">{t("settings.language.name.en")}</option>
                <option value="fa">{t("settings.language.name.fa")}</option>
              </select>
            </div>
          </div>

          {error === "taken" || error === "invalid" || error === "unexpected" ? (
            <NoticeBanner tone="danger" message={t(`admin.createUser.error.${error}`)} />
          ) : null}
          </div>

          {/* The submit lives in the footer, so the form wraps body and footer. */}
          <div className="hm-admin-modal__footer">
            <button
              type="submit"
              className="hm-settings-button hm-settings-button--primary"
              data-size="md"
              data-busy={busy}
              disabled={busy}
              aria-busy={busy}
            >
              {busy ? t("admin.createUser.submitBusy") : t("admin.createUser.submit")}
            </button>
            <SettingsButton
              label={t("chat.common.cancel")}
              disabled={busy}
              onClick={onCancel}
            />
          </div>
        </form>
      </div>
    </div>
  );
}
