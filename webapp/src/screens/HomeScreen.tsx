import { useTranslation } from "react-i18next";

import type { components } from "../api/schema";
import { LanguageSwitcher } from "../components/LanguageSwitcher";

type User = components["schemas"]["User"];

interface HomeScreenProps {
  user: User;
  onLogout: () => void;
  onChangePassword: () => void;
}

/**
 * Placeholder home screen for an authenticated user. The chat shell is a
 * separate design (docs/design/STATUS.md: PENDING), so this screen carries no
 * visual design of its own — only the shared tokens, so it does not read as
 * broken next to the finished auth screens.
 */
export function HomeScreen({ user, onLogout, onChangePassword }: HomeScreenProps) {
  const { t } = useTranslation();

  return (
    <section className="hm-placeholder" aria-labelledby="home-title">
      <h2 className="hm-form__title" id="home-title">
        {t("home.title")}
      </h2>
      <p className="hm-form__helper">{t("home.signedInAs", { name: user.display_name })}</p>
      <div className="hm-placeholder__actions">
        <button type="button" className="hm-text-button" onClick={onChangePassword}>
          {t("home.changePasswordLink")}
        </button>
        <button type="button" className="hm-text-button" onClick={onLogout}>
          {t("home.logout")}
        </button>
      </div>
      {/* Until the chat shell ships, this is the only place a signed-in user
          can change language. */}
      <div className="hm-placeholder__actions">
        <LanguageSwitcher />
      </div>
    </section>
  );
}
