import { useTranslation } from "react-i18next";

import type { User } from "../../../chat/types";
import { LanguageSwitcher } from "../../LanguageSwitcher";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup draws a Settings control in the user footer but no panel behind
 * it; user settings are BRIEFS.md §4 (PENDING). Until that design lands this
 * is where the account actions the app already has — change password, sign
 * out, language — are reachable.
 */

interface AccountMenuProps {
  user: User;
  onChangePassword: () => void;
  onLogout: () => void;
  onClose: () => void;
}

export function AccountMenu({ user, onChangePassword, onLogout, onClose }: AccountMenuProps) {
  const { t } = useTranslation();

  return (
    <section
      className="hm-plumbing hm-plumbing--footer"
      role="dialog"
      aria-modal="false"
      aria-label={t("chat.footer.account")}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <h2>{t("account.title")}</h2>
      <p>{t("account.signedInAs", { name: user.display_name })}</p>
      <p>
        <button type="button" onClick={onChangePassword}>
          {t("account.changePasswordLink")}
        </button>
      </p>
      <p>
        <button type="button" onClick={onLogout}>
          {t("account.logout")}
        </button>
      </p>
      <LanguageSwitcher />
      <p>
        <button type="button" onClick={onClose}>
          {t("chat.common.close")}
        </button>
      </p>
    </section>
  );
}
