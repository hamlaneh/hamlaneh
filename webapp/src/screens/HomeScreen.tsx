import { useTranslation } from "react-i18next";

import type { components } from "../api/schema";

type User = components["schemas"]["User"];

interface HomeScreenProps {
  user: User;
  onLogout: () => void;
  onChangePassword: () => void;
}

/**
 * Placeholder home screen for an authenticated user (Phase 1.1 — the chat
 * shell arrives in a later slice). Unstyled on purpose: docs/design/STATUS.md
 * marks this screen PENDING, so this is functional plumbing only
 * (awaiting-design).
 */
export function HomeScreen({ user, onLogout, onChangePassword }: HomeScreenProps) {
  const { t } = useTranslation();

  return (
    <section aria-labelledby="home-title">
      <h2 id="home-title">{t("home.title")}</h2>
      <p>{t("home.signedInAs", { name: user.display_name })}</p>
      <button type="button" onClick={onChangePassword}>
        {t("home.changePasswordLink")}
      </button>
      <button type="button" onClick={onLogout}>
        {t("home.logout")}
      </button>
    </section>
  );
}
