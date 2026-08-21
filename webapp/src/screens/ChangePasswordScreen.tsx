import { useState } from "react";
import type { SubmitEvent } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";

type ChangePasswordError =
  | "none"
  | "currentRequired"
  | "mismatch"
  | "invalidCurrentPassword"
  | "tooShort"
  | "unexpected";

/** Contract minimum for a new password (spec: ChangePasswordRequest.new_password). */
const MIN_NEW_PASSWORD_LENGTH = 12;

type ChangePasswordScreenProps = {
  onSuccess: () => void;
} & (
  | { mode: "forced"; onSignOut: () => void }
  | { mode: "voluntary"; onCancel: () => void }
);

/** Maps a change-password failure to a localized-error key (never raw server text). */
function mapServerError(status: number, code: string | undefined): ChangePasswordError {
  if (status === 403 && code === "invalid_current_password") {
    return "invalidCurrentPassword";
  }
  if (status === 400) {
    // Fallback only: the pre-submit checks below already enforce the 12-char
    // minimum, but the server may still answer 400 (invalid_request) for
    // policy it alone knows; the length rule is the likeliest cause.
    return "tooShort";
  }
  return "unexpected";
}

/**
 * Change-password screen against POST /api/v1/auth/change-password.
 * Forced mode (must_change_password set) explains the requirement; the only
 * way out without changing the password is signing out (someone may have
 * entered the wrong account). Voluntary mode is reached from Home and can be
 * cancelled.
 * Unstyled on purpose: docs/design/STATUS.md marks this screen PENDING,
 * so this is functional plumbing only (awaiting-design).
 */
export function ChangePasswordScreen(props: ChangePasswordScreenProps) {
  const { t } = useTranslation();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<ChangePasswordError>("none");
  const [submitting, setSubmitting] = useState(false);

  const submit = async () => {
    // Pre-submit checks, in field order: everything the client can decide
    // locally never reaches the network (and never misreads a server 400).
    if (currentPassword === "") {
      setError("currentRequired");
      return;
    }
    if (newPassword.length < MIN_NEW_PASSWORD_LENGTH) {
      setError("tooShort");
      return;
    }
    if (newPassword !== confirmPassword) {
      setError("mismatch");
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      const { error: apiError, response } = await api.POST(
        "/api/v1/auth/change-password",
        {
          body: {
            current_password: currentPassword,
            new_password: newPassword,
          },
        },
      );
      if (response.status === 204) {
        props.onSuccess();
        return;
      }
      setError(mapServerError(response.status, apiError?.error.code));
    } catch (requestError) {
      console.warn("Change-password request failed:", requestError);
      setError("unexpected");
    } finally {
      setSubmitting(false);
    }
  };

  const handleSubmit = (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    void submit();
  };

  return (
    <section aria-labelledby="change-password-title">
      <h2 id="change-password-title">{t("changePassword.title")}</h2>
      {props.mode === "forced" ? <p>{t("changePassword.forcedNotice")}</p> : null}
      <form onSubmit={handleSubmit}>
        <div>
          <label htmlFor="current-password">
            {t("changePassword.currentPasswordLabel")}
          </label>
          <input
            id="current-password"
            name="current_password"
            type="password"
            autoComplete="current-password"
            value={currentPassword}
            onChange={(event) => {
              setCurrentPassword(event.target.value);
            }}
          />
        </div>
        <div>
          <label htmlFor="new-password">
            {t("changePassword.newPasswordLabel")}
          </label>
          <input
            id="new-password"
            name="new_password"
            type="password"
            autoComplete="new-password"
            aria-describedby="new-password-hint"
            value={newPassword}
            onChange={(event) => {
              setNewPassword(event.target.value);
            }}
          />
          <p id="new-password-hint">{t("changePassword.minLengthHint")}</p>
        </div>
        <div>
          <label htmlFor="confirm-password">
            {t("changePassword.confirmPasswordLabel")}
          </label>
          <input
            id="confirm-password"
            name="confirm_password"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(event) => {
              setConfirmPassword(event.target.value);
            }}
          />
        </div>
        {error === "none" ? null : (
          <p role="alert">{t(`changePassword.error.${error}`)}</p>
        )}
        <button type="submit" disabled={submitting}>
          {t("changePassword.submit")}
        </button>
        {props.mode === "voluntary" ? (
          <button type="button" onClick={props.onCancel}>
            {t("changePassword.cancel")}
          </button>
        ) : (
          <button type="button" onClick={props.onSignOut}>
            {t("changePassword.signOut")}
          </button>
        )}
      </form>
    </section>
  );
}
