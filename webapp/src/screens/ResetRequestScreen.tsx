import { useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { RESET_TOKEN_TTL_MINUTES } from "../auth/passwordPolicy";
import { useRateLimitNotice } from "../auth/rateLimit";
import type { RateLimitKeys } from "../auth/rateLimit";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { BackLink } from "../components/auth/BackLink";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PrimaryButton } from "../components/auth/PrimaryButton";
import { TextField } from "../components/auth/TextField";

type RequestError = "none" | "invalidEmail" | "rateLimited" | "networkError" | "unexpected";

/**
 * This screen's own sentence family: what it counts down is a run of link
 * requests, not failed attempts, so it says "Too many requests" where the
 * sign-in screen says "Too many attempts" — the counted variants have to
 * follow the undated wording they replace.
 */
const RATE_LIMIT_KEYS: RateLimitKeys = {
  undated: "resetRequest.error.rateLimited",
  seconds: "resetRequest.error.rateLimitedSeconds",
  minutes: "resetRequest.error.rateLimitedMinutes",
};

interface ResetRequestScreenProps {
  onBackToSignIn: () => void;
}

/**
 * "Reset your password", artboards `reset-request` and
 * `reset-request-confirmation`.
 *
 * The confirmation is the same for every address the contract accepts —
 * POST /auth/reset-request always answers 202 — and it names no identity, so
 * the screen cannot be used to find out which addresses have accounts. It is
 * announced with role="status" (NoticeBanner's non-danger tones), which is
 * what keeps it from stealing focus: nothing failed.
 */
export function ResetRequestScreen({ onBackToSignIn }: ResetRequestScreenProps) {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<RequestError>("none");
  const [submitting, setSubmitting] = useState(false);
  const {
    message: rateLimitMessage,
    start: startRateLimitWait,
    clear: clearRateLimitWait,
  } = useRateLimitNotice(RATE_LIMIT_KEYS, () => {
    // The stated wait has passed, so the notice goes — but only if it is
    // still the notice on screen; a later failure of its own must stand.
    setError((current) => (current === "rateLimited" ? "none" : current));
  });

  const submit = async () => {
    if (submitting) {
      return;
    }
    if (email.trim() === "") {
      setError("invalidEmail");
      return;
    }
    setSubmitting(true);
    setError("none");
    // A fresh attempt supersedes whatever wait the last 429 stated.
    clearRateLimitWait();
    try {
      const { response } = await api.POST("/api/v1/auth/reset-request", {
        body: { email: email.trim() },
      });
      if (response.status === 202) {
        setSent(true);
        return;
      }
      if (response.status === 429) {
        // The spec's RateLimited response carries the wait; reading it is the
        // difference between telling the user when the door reopens and
        // inventing "a few minutes".
        startRateLimitWait(response);
        setError("rateLimited");
        return;
      }
      setError(response.status === 400 ? "invalidEmail" : "unexpected");
    } catch (requestError) {
      console.warn("Password-reset request failed:", requestError);
      setError("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  if (sent) {
    return (
      <AuthShell>
        <AuthForm
          title={t("resetRequest.title")}
          onSubmit={() => {
            // Nothing left to submit; the form is only here for the heading
            // block, which keeps the column in exactly the drawn position.
          }}
        >
          <NoticeBanner tone="success" message={t("resetRequest.confirmation")} />
          <p className="hm-form__helper">
            {t("resetRequest.confirmationHelper", { minutes: RESET_TOKEN_TTL_MINUTES })}
          </p>
          <BackLink label={t("resetRequest.backToSignIn")} onClick={onBackToSignIn} />
        </AuthForm>
      </AuthShell>
    );
  }

  const formError =
    error === "rateLimited"
      ? rateLimitMessage
      : error === "networkError" || error === "unexpected"
        ? t(`resetRequest.error.${error}`)
        : undefined;

  return (
    <AuthShell>
      <AuthForm
        title={t("resetRequest.title")}
        helper={t("resetRequest.helper")}
        onSubmit={() => {
          void submit();
        }}
      >
        {formError === undefined ? null : (
          <NoticeBanner tone={error === "rateLimited" ? "warning" : "danger"} message={formError} />
        )}
        <div className="hm-form__fields">
          <TextField
            id="reset-email"
            label={t("resetRequest.emailLabel")}
            type="email"
            dir="ltr"
            autoComplete="email"
            value={email}
            onChange={(value) => {
              setEmail(value);
              if (error === "invalidEmail") {
                setError("none");
              }
            }}
            error={error === "invalidEmail" ? t("resetRequest.error.invalidEmail") : undefined}
          />
        </div>
        <PrimaryButton
          label={t("resetRequest.submit")}
          busyLabel={t("resetRequest.submitting")}
          busy={submitting}
        />
        <BackLink label={t("resetRequest.backToSignIn")} onClick={onBackToSignIn} />
      </AuthForm>
    </AuthShell>
  );
}
