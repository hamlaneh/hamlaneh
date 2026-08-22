import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { BackLink } from "../components/auth/BackLink";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { OtpInput } from "../components/auth/OtpInput";
import { PrimaryButton } from "../components/auth/PrimaryButton";

type User = components["schemas"]["User"];

const CODE_LENGTH = 6;

type ChallengeError =
  | "none"
  | "incomplete"
  | "invalidCode"
  | "rateLimited"
  | "networkError"
  | "unexpected";

interface TotpChallengeScreenProps {
  onAuthenticated: (user: User) => void;
  /** The challenge is gone (expired, consumed, or five wrong codes): start again. */
  onChallengeLost: () => void;
  onBack: () => void;
}

/**
 * The second half of sign-in, artboard `login-totp`: the password was right
 * (login answered 202) but no session exists yet. The challenge travels in the
 * cookie that 202 set, so this screen sends nothing but the code.
 *
 * A wrong code does not restart the flow — the cells clear and focus returns
 * to the first, exactly as the settings sheet specifies for the same input.
 */
export function TotpChallengeScreen({
  onAuthenticated,
  onChallengeLost,
  onBack,
}: TotpChallengeScreenProps) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const [error, setError] = useState<ChallengeError>("none");
  const [submitting, setSubmitting] = useState(false);
  // Bumped on every rejection so a repeated identical one still moves focus.
  const [failureCount, setFailureCount] = useState(0);
  const firstCellRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (failureCount > 0) {
      firstCellRef.current?.focus();
    }
  }, [failureCount]);

  const reject = (kind: Exclude<ChallengeError, "none" | "incomplete">) => {
    setError(kind);
    setCode("");
    setFailureCount((count) => count + 1);
  };

  const submit = async () => {
    if (submitting) {
      return;
    }
    if (code.length < CODE_LENGTH) {
      setError("incomplete");
      firstCellRef.current?.focus();
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      const { data, error: apiError, response } = await api.POST("/api/v1/auth/login/totp", {
        body: { code },
      });
      if (response.status === 200 && data !== undefined) {
        onAuthenticated(data);
        return;
      }
      if (response.status === 401) {
        // One status, two very different situations: a wrong code leaves the
        // challenge alive to retry, anything else means there is no challenge
        // left and the caller must start at the password step.
        if (apiError?.error.code === "invalid_totp_code") {
          reject("invalidCode");
        } else {
          onChallengeLost();
        }
        return;
      }
      reject(response.status === 429 ? "rateLimited" : "unexpected");
    } catch (requestError) {
      console.warn("Two-step verification request failed:", requestError);
      reject("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  // A rejected or unfinished code belongs on the cells; anything about the
  // request itself belongs in the form-level banner.
  const cellError =
    error === "invalidCode" || error === "incomplete" ? t(`totp.error.${error}`) : undefined;
  const formError =
    error === "rateLimited" || error === "networkError" || error === "unexpected"
      ? t(`totp.error.${error}`)
      : undefined;

  return (
    <AuthShell>
      <AuthForm
        title={t("totp.title")}
        helper={t("totp.helper")}
        onSubmit={() => {
          void submit();
        }}
      >
        {formError === undefined ? null : (
          <NoticeBanner
            tone={error === "rateLimited" ? "warning" : "danger"}
            message={formError}
          />
        )}
        <OtpInput
          id="totp-code"
          label={t("totp.codeLabel")}
          value={code}
          length={CODE_LENGTH}
          firstCellRef={firstCellRef}
          onChange={(value) => {
            setCode(value);
            if (error !== "none") {
              setError("none");
            }
          }}
          error={cellError}
        />
        <PrimaryButton
          label={t("totp.submit")}
          busyLabel={t("totp.submitting")}
          busy={submitting}
        />
        <BackLink label={t("totp.back")} onClick={onBack} />
      </AuthForm>
    </AuthShell>
  );
}
