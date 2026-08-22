import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PasswordField } from "../components/auth/PasswordField";
import { PrimaryButton } from "../components/auth/PrimaryButton";
import { TextField } from "../components/auth/TextField";
import { useInstance } from "../instance/instanceInfo";

type User = components["schemas"]["User"];

type LoginError =
  | "none"
  | "invalidCredentials"
  | "rateLimited"
  | "networkError"
  | "unexpected";

interface LoginScreenProps {
  onAuthenticated: (user: User) => void;
  /** Password accepted, second factor still owed: continue at /auth/login/totp. */
  onTwoStepRequired: () => void;
  onForgotPassword: () => void;
  /**
   * A standing message from wherever the user arrived — a completed reset, or
   * a two-step challenge that ran out. Announced politely: it did not fail
   * here, so it must not take focus from the form.
   */
  notice?: LoginNotice | undefined;
}

export interface LoginNotice {
  tone: "success" | "warning";
  message: string;
}

/**
 * Sign-in against POST /api/v1/auth/login, in the delivered design
 * (artboards login-default, login-error, login-rate-limited, and their dark
 * and Persian counterparts).
 *
 * One deviation from `login-rate-limited`, recorded in the handoff's open
 * questions: that artboard disables the fields and the toggle as well as the
 * button, but the mockup's async panel also requires every failure to "leave
 * an edit-and-retry path", and the 429 carries no Retry-After to end the state
 * on. Disabling everything with no timer bricks the screen until a reload, so
 * only the button is disabled and editing a field lifts the notice.
 */
export function LoginScreen({
  onAuthenticated,
  onTwoStepRequired,
  onForgotPassword,
  notice,
}: LoginScreenProps) {
  const { t } = useTranslation();
  const { info, loaded } = useInstance();
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<LoginError>("none");
  // Bumped on every failure so a repeated identical failure still moves focus.
  const [failureCount, setFailureCount] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const alertRef = useRef<HTMLDivElement>(null);

  const rateLimited = error === "rateLimited";

  useEffect(() => {
    // A form-level failure is announced by role="alert" and takes focus, so
    // the reason is the first thing a keyboard or screen-reader user meets.
    if (failureCount > 0) {
      alertRef.current?.focus();
    }
  }, [failureCount]);

  const fail = (kind: Exclude<LoginError, "none">) => {
    setError(kind);
    // The identifier survives a failed sign-in; the password never does.
    setPassword("");
    if (kind !== "rateLimited") {
      // The rate-limit notice is announced politely and does not steal focus.
      setFailureCount((count) => count + 1);
    }
  };

  /**
   * The 429 carries no Retry-After yet (slice 1.1b), so a countdown would be
   * invented. The honest exit is the edit-and-retry path the design requires
   * of every failure: touching either field drops the notice and re-enables
   * submit. A server that is still limiting simply answers 429 again.
   */
  const clearRateLimitNotice = () => {
    if (rateLimited) {
      setError("none");
    }
  };

  const submit = async () => {
    // Duplicate submissions are blocked while a request is in flight.
    if (submitting || rateLimited) {
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      const { data, response } = await api.POST("/api/v1/auth/login", {
        body: { identifier, password },
      });
      if (data !== undefined) {
        if (response.status === 202) {
          // Password accepted, but the account has two-step verification on:
          // there is NO session yet. A distinct status precisely so this can
          // never be mistaken for a signed-in state — hand over to the code
          // screen, which completes the sign-in against the challenge cookie.
          onTwoStepRequired();
          return;
        }
        onAuthenticated(data as User);
        return;
      }
      // Only 401 means the credentials were wrong — its body is identical for
      // unknown user and wrong password (contract: no account enumeration), so
      // one generic message covers both. Never blame the password for a server
      // fault: anything else the contract allows (400) or does not (5xx) is
      // reported as an unexpected failure.
      if (response.status === 429) {
        fail("rateLimited");
      } else if (response.status === 401) {
        fail("invalidCredentials");
      } else {
        fail("unexpected");
      }
    } catch (requestError) {
      console.warn("Login request failed:", requestError);
      fail("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AuthShell>
      <AuthForm
        title={t("login.title")}
        helper={t("login.helper")}
        onSubmit={() => {
          void submit();
        }}
      >
        {error === "none" ? (
          notice === undefined ? null : (
            <NoticeBanner tone={notice.tone} message={notice.message} />
          )
        ) : (
          <NoticeBanner
            ref={alertRef}
            tone={rateLimited ? "warning" : "danger"}
            message={t(`login.error.${error}`)}
          />
        )}
        <div className="hm-form__fields">
          <TextField
            id="login-identifier"
            label={t("login.identifierLabel")}
            autoComplete="username"
            dir="auto"
            value={identifier}
            onChange={(value) => {
              setIdentifier(value);
              clearRateLimitNotice();
            }}
          />
          <PasswordField
            id="login-password"
            label={t("login.passwordLabel")}
            autoComplete="current-password"
            value={password}
            onChange={(value) => {
              setPassword(value);
              clearRateLimitNotice();
            }}
            labelAction={
              // Instance policy decides whether this link exists at all: a
              // zero-config install has no mail transport, and the contract
              // adds password_reset_available precisely so the screen can omit
              // the link instead of offering one that goes nowhere. Absent
              // while the instance document is still loading, so it never
              // appears and then vanishes.
              loaded && info.password_reset_available ? (
                <button type="button" className="hm-link" onClick={onForgotPassword}>
                  {t("login.forgotPassword")}
                </button>
              ) : null
            }
          />
        </div>
        <PrimaryButton
          label={t("login.submit")}
          busyLabel={t("login.submitting")}
          busy={submitting}
          disabled={rateLimited}
        />
      </AuthForm>
    </AuthShell>
  );
}
