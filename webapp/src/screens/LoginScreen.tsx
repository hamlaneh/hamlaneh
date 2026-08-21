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

type User = components["schemas"]["User"];

type LoginError =
  | "none"
  | "twoStepRequired"
  | "invalidCredentials"
  | "rateLimited"
  | "networkError"
  | "unexpected";

interface LoginScreenProps {
  onAuthenticated: (user: User) => void;
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
export function LoginScreen({ onAuthenticated }: LoginScreenProps) {
  const { t } = useTranslation();
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
          // there is no session yet. The code screen lands with slice 1.1b —
          // until then, say so rather than pretending the sign-in worked.
          fail("twoStepRequired");
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
        {error === "none" ? null : (
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
              // Password reset has no endpoint until slice 1.1b; the design's
              // disabled link state stands in until it does. aria-disabled
              // keeps assistive tech from reading it as ordinary prose.
              <span className="hm-link" role="link" aria-disabled="true" data-disabled="true">
                {t("login.forgotPassword")}
              </span>
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
