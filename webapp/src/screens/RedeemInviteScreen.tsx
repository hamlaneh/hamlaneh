import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import { AuthForm } from "../components/auth/AuthForm";
import { AuthShell } from "../components/auth/AuthShell";
import { NoticeBanner } from "../components/auth/NoticeBanner";
import { PasswordField } from "../components/auth/PasswordField";
import { PasswordRequirements } from "../components/auth/PasswordRequirements";
import { PasswordStrengthMeter } from "../components/auth/PasswordStrengthMeter";
import { PrimaryButton } from "../components/auth/PrimaryButton";
import { TextField } from "../components/auth/TextField";
import { useInstance } from "../instance/instanceInfo";

/** The contract's own rule (RedeemInviteRequest.username). */
const USERNAME_PATTERN = /^[a-z0-9][a-z0-9_.-]*$/u;
const USERNAME_MIN = 3;
const USERNAME_MAX = 32;

type Preview =
  | { status: "loading" }
  | { status: "ready"; organizationName: string }
  /** Unknown, expired, revoked and already-used all answer the same 404. */
  | { status: "unusable" }
  | { status: "unreachable" };

type FormError =
  | "none"
  | "username"
  | "usernameTaken"
  | "password"
  | "mismatch"
  | "unusable"
  | "rateLimited"
  | "unexpected";

interface RedeemInviteScreenProps {
  token: string;
  /** Sends the finished account to the sign-in screen. */
  onAccountCreated: () => void;
}

/**
 * UNDESIGNED SURFACE — no artboard covers invite redemption, so this is
 * assembled entirely from the delivered auth parts (`AuthShell`, `AuthForm`,
 * `TextField`, `PasswordField`, `PrimaryButton`, `NoticeBanner`) with no
 * visual treatment of its own. Filed `awaiting-design` in
 * docs/design/STATUS.md. This is the precedent the recovery-code screen set.
 *
 * The screen is public: it runs before anybody has an account, and the
 * preview it draws deliberately names only the organization — never who
 * issued the invite or for whom.
 */
export function RedeemInviteScreen({ token, onAccountCreated }: RedeemInviteScreenProps) {
  const { t } = useTranslation();
  const { info } = useInstance();
  const [preview, setPreview] = useState<Preview>({ status: "loading" });
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<FormError>("none");
  const [submitting, setSubmitting] = useState(false);
  const alertRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // ESLint's narrowing follows the assignments it can see and concludes
    // this is always true after the await — it cannot see that the cleanup
    // below flips it while the request is in flight, which is the entire
    // reason it exists. The guards are load-bearing: without them a preview
    // that resolves after the screen has moved on sets state on nothing.
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
    let live = true;
    void (async () => {
      try {
        const { data, response } = await api.GET("/api/v1/invites/{token}", {
          params: { path: { token } },
        });
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        if (!live) {
          return;
        }
        setPreview(
          data === undefined
            ? { status: response.status === 404 ? "unusable" : "unreachable" }
            : { status: "ready", organizationName: data.org_name },
        );
      } catch (requestError) {
        console.warn("Invite preview failed:", requestError);
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        if (live) {
          setPreview({ status: "unreachable" });
        }
      }
    })();
    return () => {
      live = false;
    };
  }, [token]);

  const submit = () => {
    if (submitting) {
      return;
    }
    const trimmed = username.trim();
    if (
      trimmed.length < USERNAME_MIN ||
      trimmed.length > USERNAME_MAX ||
      !USERNAME_PATTERN.test(trimmed)
    ) {
      setError("username");
      return;
    }
    if (password.length < info.password_min_length) {
      setError("password");
      return;
    }
    if (password !== confirm) {
      setError("mismatch");
      return;
    }
    setSubmitting(true);
    setError("none");
    void (async () => {
      try {
        const { response, error: apiError } = await api.POST("/api/v1/invites/{token}", {
          params: { path: { token } },
          body: {
            username: trimmed,
            password,
            ...(displayName.trim() === "" ? {} : { display_name: displayName.trim() }),
          },
        });
        if (response.status === 201) {
          onAccountCreated();
          return;
        }
        if (response.status === 404) {
          setError("unusable");
        } else if (apiError?.error.code === "username_taken") {
          setError("usernameTaken");
        } else if (response.status === 429) {
          setError("rateLimited");
        } else {
          setError("unexpected");
        }
      } catch (requestError) {
        console.warn("Redeeming the invite failed:", requestError);
        setError("unexpected");
      } finally {
        setSubmitting(false);
        alertRef.current?.focus();
      }
    })();
  };

  if (preview.status === "loading") {
    return (
      <AuthShell>
        <p className="hm-form__helper" role="status">
          {t("common.loading")}
        </p>
      </AuthShell>
    );
  }

  if (preview.status !== "ready") {
    return (
      <AuthShell>
        <AuthForm
          title={t("invite.unusable.title")}
          helper={t(
            preview.status === "unusable" ? "invite.unusable.body" : "invite.unreachable.body",
          )}
          onSubmit={onAccountCreated}
        >
          <button type="button" className="hm-text-button" onClick={onAccountCreated}>
            {t("invite.goToSignIn")}
          </button>
        </AuthForm>
      </AuthShell>
    );
  }

  return (
    <AuthShell opticalRise={16}>
      <AuthForm
        title={t("invite.title", { organization: preview.organizationName })}
        helper={t("invite.helper")}
        onSubmit={submit}
      >
        {error === "none" || error === "username" || error === "password" || error === "mismatch" ? null : (
          <NoticeBanner ref={alertRef} tone="danger" message={t(`invite.error.${error}`)} />
        )}
        <div className="hm-form__fields">
          <TextField
            id="invite-username"
            label={t("invite.usernameLabel")}
            hint={t("invite.usernameHint")}
            error={
              error === "username"
                ? t("invite.error.username")
                : error === "usernameTaken"
                  ? t("invite.error.usernameTaken")
                  : undefined
            }
            autoComplete="username"
            dir="ltr"
            value={username}
            onChange={setUsername}
          />
          <TextField
            id="invite-display-name"
            label={t("invite.displayNameLabel")}
            hint={t("invite.displayNameHint")}
            autoComplete="name"
            value={displayName}
            onChange={setDisplayName}
          />
          <PasswordField
            id="invite-password"
            label={t("invite.passwordLabel")}
            autoComplete="new-password"
            value={password}
            onChange={setPassword}
            error={error === "password" ? t("invite.error.password") : undefined}
          />
          <PasswordStrengthMeter password={password} minimumLength={info.password_min_length} />
          <PasswordRequirements
            requirements={[
              {
                id: "minLength",
                label: t("password.minLength", { minimum: info.password_min_length }),
                met: password.length >= info.password_min_length,
              },
            ]}
          />
          <PasswordField
            id="invite-confirm"
            label={t("invite.confirmLabel")}
            autoComplete="new-password"
            value={confirm}
            onChange={setConfirm}
            error={error === "mismatch" ? t("invite.error.mismatch") : undefined}
          />
        </div>
        <PrimaryButton
          label={t("invite.submit")}
          busyLabel={t("invite.submitting")}
          busy={submitting}
        />
      </AuthForm>
    </AuthShell>
  );
}
