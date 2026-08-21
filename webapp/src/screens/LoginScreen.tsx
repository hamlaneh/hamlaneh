import { useState } from "react";
import type { SubmitEvent } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { components } from "../api/schema";

type User = components["schemas"]["User"];

type LoginError = "none" | "invalidCredentials" | "rateLimited" | "unexpected";

interface LoginScreenProps {
  onAuthenticated: (user: User) => void;
}

/**
 * Login screen against POST /api/v1/auth/login.
 * Unstyled on purpose: docs/design/STATUS.md marks this screen PENDING,
 * so this is functional plumbing only (awaiting-design).
 */
export function LoginScreen({ onAuthenticated }: LoginScreenProps) {
  const { t } = useTranslation();
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<LoginError>("none");
  const [submitting, setSubmitting] = useState(false);

  const submit = async () => {
    setSubmitting(true);
    setError("none");
    try {
      const { data, response } = await api.POST("/api/v1/auth/login", {
        body: { identifier, password },
      });
      if (data !== undefined) {
        onAuthenticated(data);
        return;
      }
      // The 401 body is identical for unknown user and wrong password
      // (contract: no account enumeration), so one generic message covers both.
      setError(response.status === 429 ? "rateLimited" : "invalidCredentials");
    } catch (requestError) {
      console.warn("Login request failed:", requestError);
      setError("unexpected");
    } finally {
      setSubmitting(false);
    }
  };

  const handleSubmit = (event: SubmitEvent<HTMLFormElement>) => {
    event.preventDefault();
    void submit();
  };

  return (
    <section aria-labelledby="login-title">
      <h2 id="login-title">{t("login.title")}</h2>
      <form onSubmit={handleSubmit}>
        <div>
          <label htmlFor="login-identifier">{t("login.identifierLabel")}</label>
          <input
            id="login-identifier"
            name="identifier"
            type="text"
            autoComplete="username"
            value={identifier}
            onChange={(event) => {
              setIdentifier(event.target.value);
            }}
          />
        </div>
        <div>
          <label htmlFor="login-password">{t("login.passwordLabel")}</label>
          <input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => {
              setPassword(event.target.value);
            }}
          />
        </div>
        {error === "none" ? null : (
          <p role="alert">{t(`login.error.${error}`)}</p>
        )}
        <button type="submit" disabled={submitting}>
          {t("login.submit")}
        </button>
      </form>
    </section>
  );
}
