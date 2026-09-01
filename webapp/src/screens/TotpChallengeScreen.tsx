import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { useRateLimitNotice } from "../auth/rateLimit";
import type { RateLimitKeys } from "../auth/rateLimit";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { BackLink } from "../components/auth/BackLink";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { OtpInput } from "../components/auth/OtpInput";
import { PrimaryButton } from "../components/auth/PrimaryButton";
import { TextField } from "../components/auth/TextField";

type User = components["schemas"]["User"];

const CODE_LENGTH = 6;

/**
 * Which of the two things the contract accepts in `code` is being typed. The
 * endpoint is the same for both — only the control that collects the value
 * differs, because a recovery code is not six digits.
 */
type Method = "authenticator" | "recovery";

/** Everything the contract tells the server to ignore inside a recovery code. */
const IGNORED_IN_RECOVERY_CODE = /[\s-]/gu;

/** The contract's bounds on `code` (openapi.yaml, TotpLoginRequest). */
const RECOVERY_MIN_LENGTH = 6;
const RECOVERY_MAX_LENGTH = 16;

type ChallengeError =
  | "none"
  | "incomplete"
  | "invalidCode"
  | "rateLimited"
  | "networkError"
  | "unexpected";

/**
 * This screen's undated wording, with the counted variants borrowed from the
 * sign-in screen: `totp.error.rateLimited` and `login.error.rateLimited` are
 * the same sentence, and the counted forms are that sentence with the vague
 * tail replaced by the number the server gave.
 */
const RATE_LIMIT_KEYS: RateLimitKeys = {
  undated: "totp.error.rateLimited",
  seconds: "login.error.rateLimitedSeconds",
  minutes: "login.error.rateLimitedMinutes",
};

/**
 * Reduces a typed recovery code to the form the contract describes:
 * "case-insensitive, hyphens and spaces ignored". Someone reading `P4RD-1TWL`
 * off paper at a bad moment types `p4rd1twl`, or pastes it with a stray space
 * — all of it means the same code, and none of it should cost them the
 * account.
 */
function normalizeRecoveryCode(raw: string): string {
  return raw.replace(IGNORED_IN_RECOVERY_CODE, "").toUpperCase();
}

/**
 * Whether the typed value is worth a request at all. Both bounds come from the
 * contract, so this refuses only what the server would answer 400 to — and a
 * 400 here would read as "something went wrong" rather than "check the code".
 */
function acceptableLength(method: Method, typed: string): boolean {
  if (method === "authenticator") {
    return typed.length === CODE_LENGTH;
  }
  return typed.length >= RECOVERY_MIN_LENGTH && typed.length <= RECOVERY_MAX_LENGTH;
}

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
 * A wrong code does not restart the flow — the input clears and focus returns
 * to its start, exactly as the settings sheet specifies for the same input.
 *
 * UNDESIGNED ADDITION — the recovery-code half. No artboard draws it (see
 * docs/design/STATUS.md), so it introduces no visual treatment of its own: the
 * switch is the existing `hm-text-button`, exactly as the forced
 * password-change screen carries its own off-artboard escape hatch, and the
 * field is the delivered `TextField`. Without it, the ten codes the product
 * hands out at enrolment could never be typed anywhere.
 */
export function TotpChallengeScreen({
  onAuthenticated,
  onChallengeLost,
  onBack,
}: TotpChallengeScreenProps) {
  const { t } = useTranslation();
  const [method, setMethod] = useState<Method>("authenticator");
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [error, setError] = useState<ChallengeError>("none");
  const [submitting, setSubmitting] = useState(false);
  // Bumped whenever focus must return to the input: on every rejection (so a
  // repeated identical one still moves focus) and on every switch of method.
  const [focusRequest, setFocusRequest] = useState(0);
  const firstCellRef = useRef<HTMLInputElement>(null);
  const recoveryRef = useRef<HTMLInputElement>(null);
  const {
    message: rateLimitMessage,
    start: startRateLimitWait,
    clear: clearRateLimitWait,
  } = useRateLimitNotice(RATE_LIMIT_KEYS, () => {
    // The stated wait has passed, so the notice goes — but only if it is
    // still the notice on screen; a later failure of its own must stand.
    setError((current) => (current === "rateLimited" ? "none" : current));
  });

  useEffect(() => {
    if (focusRequest === 0) {
      return;
    }
    const input = method === "recovery" ? recoveryRef.current : firstCellRef.current;
    input?.focus();
  }, [focusRequest, method]);

  const requestFocus = () => {
    setFocusRequest((count) => count + 1);
  };

  const reject = (kind: Exclude<ChallengeError, "none" | "incomplete">) => {
    setError(kind);
    setCode("");
    setRecoveryCode("");
    requestFocus();
  };

  /** Switches which code is being typed; neither half keeps the other's value. */
  const switchTo = (next: Method) => {
    setMethod(next);
    setCode("");
    setRecoveryCode("");
    setError("none");
    clearRateLimitWait();
    requestFocus();
  };

  const submit = async () => {
    if (submitting) {
      return;
    }
    const typed = method === "recovery" ? normalizeRecoveryCode(recoveryCode) : code;
    if (!acceptableLength(method, typed)) {
      setError("incomplete");
      requestFocus();
      return;
    }
    setSubmitting(true);
    setError("none");
    try {
      // One endpoint, one field, both methods: the server decides which kind
      // of code this is, exactly as the contract says it does.
      const { data, error: apiError, response } = await api.POST("/api/v1/auth/login/totp", {
        body: { code: typed },
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
      if (response.status === 429) {
        // The spec's RateLimited response carries the wait; reading it is the
        // difference between telling the user when the door reopens and
        // inventing "a few minutes".
        startRateLimitWait(response);
        reject("rateLimited");
        return;
      }
      reject("unexpected");
    } catch (requestError) {
      console.warn("Two-step verification request failed:", requestError);
      reject("networkError");
    } finally {
      setSubmitting(false);
    }
  };

  const recovering = method === "recovery";

  // Clears whichever input is on screen as soon as it is edited again.
  const clearError = () => {
    if (error !== "none") {
      setError("none");
      clearRateLimitWait();
    }
  };

  // A rejected or unfinished code belongs on the input; anything about the
  // request itself belongs in the form-level banner. The wording follows what
  // is on screen NOW, not what was submitted, so a switch mid-request can
  // never leave a message pointing at the wrong kind of code.
  const inputError =
    error === "invalidCode" || error === "incomplete"
      ? t(
          recovering
            ? `totp.error.${error === "incomplete" ? "recoveryIncomplete" : "invalidRecoveryCode"}`
            : `totp.error.${error}`,
        )
      : undefined;
  const formError =
    error === "rateLimited"
      ? rateLimitMessage
      : error === "networkError" || error === "unexpected"
        ? t(`totp.error.${error}`)
        : undefined;

  return (
    <AuthShell>
      <AuthForm
        title={t("totp.title")}
        helper={t(recovering ? "totp.recovery.helper" : "totp.helper")}
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
        {recovering ? (
          // A recovery code is XXXX-XXXX, so it needs one plain field rather
          // than the six single-character cells. `dir="ltr"` for the same
          // reason the cell row carries it: the value is Latin, and Bidi would
          // otherwise reorder it on the Persian page.
          <TextField
            id="recovery-code"
            ref={recoveryRef}
            label={t("totp.recovery.codeLabel")}
            // Deliberately not "one-time-code": that invites a password
            // manager to offer the authenticator's six digits, and a wrong
            // code here costs one of only five attempts.
            autoComplete="off"
            dir="ltr"
            value={recoveryCode}
            onChange={(value) => {
              setRecoveryCode(value);
              clearError();
            }}
            error={inputError}
          />
        ) : (
          <OtpInput
            id="totp-code"
            label={t("totp.codeLabel")}
            value={code}
            length={CODE_LENGTH}
            firstCellRef={firstCellRef}
            onChange={(value) => {
              setCode(value);
              clearError();
            }}
            error={inputError}
          />
        )}
        <PrimaryButton
          label={t("totp.submit")}
          busyLabel={t("totp.submitting")}
          busy={submitting}
        />
        {/* Not on the artboard: without it the recovery codes handed out at
            enrolment have nowhere to be typed (docs/design/STATUS.md). Same
            treatment as the sign-out escape hatch on the forced
            password-change screen — no visual vocabulary of its own. */}
        <button
          type="button"
          className="hm-text-button"
          onClick={() => {
            switchTo(recovering ? "authenticator" : "recovery");
          }}
        >
          {t(recovering ? "totp.recovery.useAuthenticator" : "totp.recovery.useRecoveryCode")}
        </button>
        <BackLink label={t("totp.back")} onClick={onBack} />
      </AuthForm>
    </AuthShell>
  );
}
