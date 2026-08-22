import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { BackLink } from "../components/auth/BackLink";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PasswordField } from "../components/auth/PasswordField";
import { PasswordRequirements } from "../components/auth/PasswordRequirements";
import { PrimaryButton } from "../components/auth/PrimaryButton";
import { useInstance } from "../instance/instanceInfo";

type ResetError =
  | "none"
  | "tooShort"
  | "mismatch"
  | "invalidToken"
  | "rateLimited"
  | "networkError"
  | "unexpected";

const FIELD_OF: Partial<Record<ResetError, "new" | "confirm">> = {
  tooShort: "new",
  mismatch: "confirm",
};

interface ResetPasswordScreenProps {
  /** The opaque token from the emailed link's fragment. */
  token: string;
  /** The reset landed: every session was revoked, so the user signs in fresh. */
  onComplete: () => void;
  onBackToSignIn: () => void;
  onRequestNewLink: () => void;
}

/**
 * "Create a new password", artboard `reset-new-password`, reached only from
 * the emailed link.
 *
 * The contract answers unknown, expired and already-used tokens identically,
 * so this screen shows one message for all three and offers the one action
 * that helps — requesting a new link, which the mockup's localization table
 * names ("Request a new link" / «درخواست پیوند جدید»).
 */
export function ResetPasswordScreen({
  token,
  onComplete,
  onBackToSignIn,
  onRequestNewLink,
}: ResetPasswordScreenProps) {
  const { t } = useTranslation();
  const { info } = useInstance();
  const minimumLength = info.password_min_length;

  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<ResetError>("none");
  const [submitting, setSubmitting] = useState(false);
  /**
   * Where the last failed submission should put focus. `seq` is bumped every
   * time so a repeated identical failure still moves focus.
   */
  const [focusRequest, setFocusRequest] = useState<{
    field: "new" | "confirm" | "form";
    seq: number;
  } | null>(null);
  const newPasswordRef = useRef<HTMLInputElement>(null);
  const confirmPasswordRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Only submissions move focus — never a keystroke.
    if (focusRequest === null) {
      return;
    }
    const target = {
      new: newPasswordRef.current,
      confirm: confirmPasswordRef.current,
      form: alertRef.current,
    }[focusRequest.field];
    target?.focus();
  }, [focusRequest]);

  const fail = (kind: Exclude<ResetError, "none">) => {
    setError(kind);
    setFocusRequest((previous) => ({
      field: FIELD_OF[kind] ?? "form",
      seq: (previous?.seq ?? 0) + 1,
    }));
  };

  const submit = async () => {
    if (submitting) {
      return;
    }
    if (newPassword.length < minimumLength) {
      fail("tooShort");
      return;
    }
    if (newPassword !== confirmPassword) {
      fail("mismatch");
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      const { response } = await api.POST("/api/v1/auth/reset-complete", {
        body: { token, new_password: newPassword },
      });
      if (response.status === 204) {
        onComplete();
        return;
      }
      if (response.status === 401) {
        fail("invalidToken");
      } else if (response.status === 429) {
        fail("rateLimited");
      } else if (response.status === 400) {
        // The client already enforced the minimum; a 400 here is policy only
        // the server knows, and length is the likeliest cause.
        fail("tooShort");
      } else {
        fail("unexpected");
      }
    } catch (requestError) {
      console.warn("Password-reset completion failed:", requestError);
      fail("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  const fieldError = (field: "new" | "confirm"): string | undefined =>
    error !== "none" && FIELD_OF[error] === field
      ? t(`resetPassword.error.${error}`, { minimum: minimumLength })
      : undefined;

  const formError =
    error !== "none" && FIELD_OF[error] === undefined
      ? t(`resetPassword.error.${error}`)
      : undefined;

  return (
    <AuthShell opticalRise={24}>
      <AuthForm
        title={t("resetPassword.title")}
        helper={t("resetPassword.helper")}
        onSubmit={() => {
          void submit();
        }}
      >
        {formError === undefined ? null : (
          <NoticeBanner
            ref={alertRef}
            tone={error === "rateLimited" ? "warning" : "danger"}
            message={formError}
          />
        )}
        <div className="hm-form__fields">
          <PasswordField
            id="reset-new-password"
            ref={newPasswordRef}
            label={t("resetPassword.newPasswordLabel")}
            autoComplete="new-password"
            value={newPassword}
            onChange={setNewPassword}
            error={fieldError("new")}
          />
          <PasswordRequirements
            requirements={[
              {
                id: "minLength",
                label: t("password.minLength", { minimum: minimumLength }),
                met: newPassword.length >= minimumLength,
              },
            ]}
          />
          <PasswordField
            id="reset-confirm-password"
            ref={confirmPasswordRef}
            label={t("resetPassword.confirmPasswordLabel")}
            autoComplete="new-password"
            value={confirmPassword}
            onChange={setConfirmPassword}
            error={fieldError("confirm")}
          />
        </div>
        <PrimaryButton
          label={t("resetPassword.submit")}
          busyLabel={t("resetPassword.submitting")}
          busy={submitting}
        />
        {error === "invalidToken" ? (
          <button type="button" className="hm-text-button" onClick={onRequestNewLink}>
            {t("resetPassword.requestNewLink")}
          </button>
        ) : null}
        <BackLink label={t("resetRequest.backToSignIn")} onClick={onBackToSignIn} />
      </AuthForm>
    </AuthShell>
  );
}
