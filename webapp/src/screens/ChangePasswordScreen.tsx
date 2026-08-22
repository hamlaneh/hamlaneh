import { useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";

import { useChangePassword } from "../auth/useChangePassword";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PasswordField } from "../components/auth/PasswordField";
import { PasswordRequirements } from "../components/auth/PasswordRequirements";
import { PasswordStrengthMeter } from "../components/auth/PasswordStrengthMeter";
import { PrimaryButton } from "../components/auth/PrimaryButton";

interface ChangePasswordScreenProps {
  onSuccess: () => void;
  onSignOut: () => void;
}

/**
 * The forced password replacement, in the delivered design (artboard
 * `login-force-password-change`). Reached only while `must_change_password`
 * is set; the sole way out without changing the password is signing out,
 * because someone may have entered the wrong account.
 *
 * A voluntary change is not this screen — it is the Change-password card in
 * Settings → Security, drawn on `settings-security`. Both drive the same
 * `useChangePassword` hook.
 */
export function ChangePasswordScreen({ onSuccess, onSignOut }: ChangePasswordScreenProps) {
  const { t } = useTranslation();
  const currentRef = useRef<HTMLInputElement>(null);
  const nextRef = useRef<HTMLInputElement>(null);
  const confirmRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);
  // Ref objects are stable for the component's life, so the holder can be too.
  const refs = useMemo(
    () => ({ current: currentRef, next: nextRef, confirm: confirmRef, alert: alertRef }),
    [],
  );
  const form = useChangePassword(refs);

  const submit = async () => {
    if (await form.submit()) {
      onSuccess();
    }
  };

  return (
    // The taller form sits closer to the centre than the sign-in column.
    <AuthShell opticalRise={16}>
      <AuthForm
        title={t("changePassword.title")}
        helper={t("changePassword.forcedNotice")}
        onSubmit={() => {
          void submit();
        }}
      >
        {form.formError === undefined ? null : (
          <NoticeBanner ref={alertRef} tone="danger" message={form.formError} />
        )}
        <div className="hm-form__fields">
          <PasswordField
            id="current-password"
            ref={currentRef}
            label={t("changePassword.currentPasswordLabel")}
            autoComplete="current-password"
            value={form.currentPassword}
            onChange={form.setCurrentPassword}
            error={form.fieldError("current")}
          />
          <PasswordField
            id="new-password"
            ref={nextRef}
            label={t("changePassword.newPasswordLabel")}
            autoComplete="new-password"
            value={form.newPassword}
            onChange={form.setNewPassword}
            error={form.fieldError("new")}
            onBlur={() => {
              form.validateOnBlur(
                "new",
                form.newPassword !== "" && form.newPassword.length < form.minimumLength,
                "tooShort",
              );
            }}
          />
          {/* Between the new-password field and the requirements card, as
              drawn. Display only — it reflects every keystroke, while the
              "validate on blur" rule governs errors, not this. */}
          <PasswordStrengthMeter
            password={form.newPassword}
            minimumLength={form.minimumLength}
          />
          <PasswordRequirements
            requirements={[
              {
                id: "minLength",
                label: t("password.minLength", { minimum: form.minimumLength }),
                met: form.newPassword.length >= form.minimumLength,
              },
            ]}
          />
          <PasswordField
            id="confirm-password"
            ref={confirmRef}
            label={t("changePassword.confirmPasswordLabel")}
            autoComplete="new-password"
            value={form.confirmPassword}
            onChange={form.setConfirmPassword}
            error={form.fieldError("confirm")}
            onBlur={() => {
              form.validateOnBlur(
                "confirm",
                form.confirmPassword !== "" && form.confirmPassword !== form.newPassword,
                "mismatch",
              );
            }}
          />
        </div>
        <PrimaryButton
          label={t("changePassword.submit")}
          busyLabel={t("changePassword.submitting")}
          busy={form.submitting}
        />
        {/* Not on the artboard: the escape hatch for someone who signed into
            the wrong account (BRIEFS §1). */}
        <button type="button" className="hm-text-button" onClick={onSignOut}>
          {t("changePassword.signOut")}
        </button>
      </AuthForm>
    </AuthShell>
  );
}
