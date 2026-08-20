import type { SubmitEvent } from "react";
import { useTranslation } from "react-i18next";

/**
 * Placeholder login screen (Phase 0 — no real authentication yet).
 * Unstyled on purpose: docs/design/STATUS.md marks this screen PENDING,
 * so this is functional plumbing only (awaiting-design).
 */
export function LoginScreen() {
  const { t } = useTranslation();

  const handleSubmit = (event: SubmitEvent<HTMLFormElement>) => {
    // No auth backend exists in Phase 0; the form is intentionally inert.
    event.preventDefault();
  };

  return (
    <section aria-labelledby="login-title">
      <h2 id="login-title">{t("login.title")}</h2>
      <p>{t("login.notice")}</p>
      <form onSubmit={handleSubmit}>
        <div>
          <label htmlFor="login-username">{t("login.usernameLabel")}</label>
          <input
            id="login-username"
            name="username"
            type="text"
            autoComplete="username"
          />
        </div>
        <div>
          <label htmlFor="login-password">{t("login.passwordLabel")}</label>
          <input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
          />
        </div>
        <button type="submit">{t("login.submit")}</button>
      </form>
    </section>
  );
}
