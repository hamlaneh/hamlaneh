import { useEffect, useState } from "react";
import type { RefObject } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { useInstance } from "../instance/instanceInfo";

export type ChangePasswordError =
  | "none"
  | "currentRequired"
  | "mismatch"
  | "invalidCurrentPassword"
  | "tooShort"
  | "networkError"
  | "unexpected";

/** Where an error is shown: under a field, or as a form-level banner. */
export type ErrorLocation = "current" | "new" | "confirm" | "form";

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
 * The controls a failed submission may move focus to. The screens own these
 * refs — a hook that handed them back would be handing back ref values, which
 * a component may not read while rendering.
 */
export interface ChangePasswordRefs {
  current: RefObject<HTMLInputElement | null>;
  next: RefObject<HTMLInputElement | null>;
  confirm: RefObject<HTMLInputElement | null>;
  alert: RefObject<HTMLDivElement | null>;
}

/**
 * POST /api/v1/auth/change-password with the delivered validation, error
 * mapping and focus behaviour — shared by the forced-change screen
 * (`login-force-password-change`) and the Security section of the settings
 * panel (`settings-security`), which draw the same form in two places.
 *
 * Layout and what happens after success belong to the screens; everything
 * about *changing a password* lives here exactly once.
 */
export function useChangePassword(refs: ChangePasswordRefs) {
  const { t } = useTranslation();
  const { info } = useInstance();
  const minimumLength = info.password_min_length;

  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<ChangePasswordError>("none");
  const [submitting, setSubmitting] = useState(false);
  const [saved, setSaved] = useState(false);
  const [focusRequest, setFocusRequest] = useState<FocusRequest | null>(null);

  useEffect(() => {
    // Delivered behaviour: after an invalid submission focus moves to the
    // first invalid field, or to the alert when the failure is form-level.
    // Only submissions request focus — a blur-time error must not yank focus
    // back into the field the user is leaving.
    if (focusRequest === null) {
      return;
    }
    const target: Record<ErrorLocation, HTMLElement | null> = {
      current: refs.current.current,
      new: refs.next.current,
      confirm: refs.confirm.current,
      form: refs.alert.current,
    };
    target[focusRequest.location]?.focus();
  }, [focusRequest, refs]);

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
      ? t(`changePassword.error.${error}`, { minimum: minimumLength })
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

  const clear = () => {
    setCurrentPassword("");
    setNewPassword("");
    setConfirmPassword("");
    setError("none");
  };

  /**
   * The Saved mark describes the form as it stands, so the first keystroke
   * after a save retires it. Wrapping the setters keeps that an event, not an
   * effect watching state it just set.
   */
  const edit = (setter: (value: string) => void) => (value: string) => {
    setSaved(false);
    setter(value);
  };

  const submit = async (): Promise<boolean> => {
    if (submitting) {
      return false;
    }
    // Pre-submit checks, in field order: everything the client can decide
    // locally never reaches the network (and never misreads a server 400).
    if (currentPassword === "") {
      failSubmit("currentRequired");
      return false;
    }
    if (newPassword.length < minimumLength) {
      failSubmit("tooShort");
      return false;
    }
    if (newPassword !== confirmPassword) {
      failSubmit("mismatch");
      return false;
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
        setSaved(true);
        return true;
      }
      failSubmit(mapServerError(response.status, apiError?.error.code));
      return false;
    } catch (requestError) {
      console.warn("Change-password request failed:", requestError);
      failSubmit("networkError");
      return false;
    } finally {
      setSubmitting(false);
    }
  };

  return {
    minimumLength,
    currentPassword,
    setCurrentPassword: edit(setCurrentPassword),
    newPassword,
    setNewPassword: edit(setNewPassword),
    confirmPassword,
    setConfirmPassword: edit(setConfirmPassword),
    error,
    submitting,
    /** True from a successful change until the next keystroke. */
    saved,
    /** True while anything has been typed — what the unsaved-changes guard asks. */
    dirty: currentPassword !== "" || newPassword !== "" || confirmPassword !== "",
    formError:
      error !== "none" && ERROR_LOCATION[error] === "form"
        ? t(`changePassword.error.${error}`, { minimum: minimumLength })
        : undefined,
    fieldError,
    validateOnBlur,
    clear,
    submit,
  };
}
