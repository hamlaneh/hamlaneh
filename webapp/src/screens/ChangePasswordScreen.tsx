import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { PASSWORD_MIN_LENGTH } from "../auth/passwordPolicy";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PasswordField } from "../components/auth/PasswordField";
import { PasswordRequirements } from "../components/auth/PasswordRequirements";
import { PasswordStrengthMeter } from "../components/auth/PasswordStrengthMeter";
import { PrimaryButton } from "../components/auth/PrimaryButton";

type ChangePasswordError =
  | "none"
  | "currentRequired"
  | "mismatch"
  | "invalidCurrentPassword"
  | "tooShort"
  | "networkError"
  | "unexpected";

/** Where an error is shown: under a field, or as a form-level banner. */
type ErrorLocation = "current" | "new" | "confirm" | "form";

const ERROR_LOCATION: Record<Exclude<ChangePasswordError, "none">, ErrorLocation> = {
  currentRequired: "current",
  invalidCurrentPassword: "current",
  tooShort: "new",
  mismatch: "confirm",
  networkError: "form",
  unexpected: "form",
};

/**
 * A failed submission, recorded so the effect below can move focus to where
 * the error is shown. `seq` is bumped on every failure so submitting the same
 * invalid form twice moves focus again.
 */
interface FocusRequest {
  location: ErrorLocation;
  seq: number;
}

type ChangePasswordScreenProps = {
  onSuccess: () => void;
} & (
  | { mode: "forced"; onSignOut: () => void }
  | { mode: "voluntary"; onCancel: () => void }
);

/** Maps a change-password failure to a localized-error key (never raw server text). */
function mapServerError(
  status: number,
  code: string | undefined,
): Exclude<ChangePasswordError, "none"> {
  if (status === 403 && code === "invalid_current_password") {
    return "invalidCurrentPassword";
  }
  if (status === 400) {
    // Fallback only: the pre-submit checks below already enforce the minimum,
    // but the server may still answer 400 (invalid_request) for policy it
    // alone knows; the length rule is the likeliest cause.
    return "tooShort";
  }
  // Any other status (500, 409, …): the server answered, so the copy must not
  // claim we could not reach it — that is `networkError`, set from the catch.
  return "unexpected";
}

/**
 * Change-password against POST /api/v1/auth/change-password, in the delivered
 * design (artboard login-force-password-change).
 *
 * Forced mode (must_change_password set) explains the requirement; the only
 * way out without changing the password is signing out (someone may have
 * entered the wrong account). Voluntary mode is reached from Home and can be
 * cancelled.
 */
export function ChangePasswordScreen(props: ChangePasswordScreenProps) {
  const { t } = useTranslation();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<ChangePasswordError>("none");
  const [submitting, setSubmitting] = useState(false);
  const [focusRequest, setFocusRequest] = useState<FocusRequest | null>(null);
  const currentPasswordRef = useRef<HTMLInputElement>(null);
  const newPasswordRef = useRef<HTMLInputElement>(null);
  const confirmPasswordRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Delivered behaviour: after an invalid submission focus moves to the
    // first invalid field, or to the alert when the failure is form-level.
    // Only submissions request focus — a blur-time error must not yank focus
    // back into the field the user is leaving.
    if (focusRequest === null) {
      return;
    }
    const target: Record<ErrorLocation, HTMLElement | null> = {
      current: currentPasswordRef.current,
      new: newPasswordRef.current,
      confirm: confirmPasswordRef.current,
      form: alertRef.current,
    };
    target[focusRequest.location]?.focus();
  }, [focusRequest]);

  /** Records a submit-time failure: shows it and moves focus to where it is shown. */
  const failSubmit = (kind: Exclude<ChangePasswordError, "none">) => {
    setError(kind);
    setFocusRequest((previous) => ({
      location: ERROR_LOCATION[kind],
      seq: (previous?.seq ?? 0) + 1,
    }));
  };

  /** Error text for a field, or undefined when the error is elsewhere. */
  const fieldError = (location: ErrorLocation): string | undefined =>
    error !== "none" && ERROR_LOCATION[error] === location
      ? t(`changePassword.error.${error}`, { minimum: PASSWORD_MIN_LENGTH })
      : undefined;

  /** Validation runs on blur and on submit — never on every keystroke. */
  const validateOnBlur = (
    location: ErrorLocation,
    failed: boolean,
    kind: ChangePasswordError,
  ) => {
    if (failed) {
      setError(kind);
    } else if (error !== "none" && ERROR_LOCATION[error] === location) {
      setError("none");
    }
  };

  const submit = async () => {
    if (submitting) {
      return;
    }
    // Pre-submit checks, in field order: everything the client can decide
    // locally never reaches the network (and never misreads a server 400).
    if (currentPassword === "") {
      failSubmit("currentRequired");
      return;
    }
    if (newPassword.length < PASSWORD_MIN_LENGTH) {
      failSubmit("tooShort");
      return;
    }
    if (newPassword !== confirmPassword) {
      failSubmit("mismatch");
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      const { error: apiError, response } = await api.POST("/api/v1/auth/change-password", {
        body: {
          current_password: currentPassword,
          new_password: newPassword,
        },
      });
      if (response.status === 204) {
        props.onSuccess();
        return;
      }
      failSubmit(mapServerError(response.status, apiError?.error.code));
    } catch (requestError) {
      console.warn("Change-password request failed:", requestError);
      failSubmit("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    // The taller form sits closer to the centre than the sign-in column.
    <AuthShell opticalRise={16}>
      <AuthForm
        title={t("changePassword.title")}
        {...(props.mode === "forced" ? { helper: t("changePassword.forcedNotice") } : {})}
        onSubmit={() => {
          void submit();
        }}
      >
        {error === "none" || ERROR_LOCATION[error] !== "form" ? null : (
          <NoticeBanner
            ref={alertRef}
            tone="danger"
            message={t(`changePassword.error.${error}`)}
          />
        )}
        <div className="hm-form__fields">
          <PasswordField
            id="current-password"
            ref={currentPasswordRef}
            label={t("changePassword.currentPasswordLabel")}
            autoComplete="current-password"
            value={currentPassword}
            onChange={setCurrentPassword}
            error={fieldError("current")}
          />
          <PasswordField
            id="new-password"
            ref={newPasswordRef}
            label={t("changePassword.newPasswordLabel")}
            autoComplete="new-password"
            value={newPassword}
            onChange={setNewPassword}
            error={fieldError("new")}
            onBlur={() => {
              validateOnBlur(
                "new",
                newPassword !== "" && newPassword.length < PASSWORD_MIN_LENGTH,
                "tooShort",
              );
            }}
          />
          {/* Between the new-password field and the requirements card, as
              drawn. Display only — it reflects every keystroke, while the
              "validate on blur" rule governs errors, not this. */}
          <PasswordStrengthMeter
            password={newPassword}
            minimumLength={PASSWORD_MIN_LENGTH}
          />
          <PasswordRequirements
            requirements={[
              {
                id: "minLength",
                label: t("password.minLength", { minimum: PASSWORD_MIN_LENGTH }),
                met: newPassword.length >= PASSWORD_MIN_LENGTH,
              },
            ]}
          />
          <PasswordField
            id="confirm-password"
            ref={confirmPasswordRef}
            label={t("changePassword.confirmPasswordLabel")}
            autoComplete="new-password"
            value={confirmPassword}
            onChange={setConfirmPassword}
            error={fieldError("confirm")}
            onBlur={() => {
              validateOnBlur(
                "confirm",
                confirmPassword !== "" && confirmPassword !== newPassword,
                "mismatch",
              );
            }}
          />
        </div>
        <PrimaryButton
          label={t("changePassword.submit")}
          busyLabel={t("changePassword.submitting")}
          busy={submitting}
        />
        {/* Not on the artboard: the escape hatch for someone who signed into
            the wrong account, and the way back out of a voluntary change. */}
        <button
          type="button"
          className="hm-text-button"
          onClick={props.mode === "forced" ? props.onSignOut : props.onCancel}
        >
          {props.mode === "forced" ? t("changePassword.signOut") : t("changePassword.cancel")}
        </button>
      </AuthForm>
    </AuthShell>
  );
}
