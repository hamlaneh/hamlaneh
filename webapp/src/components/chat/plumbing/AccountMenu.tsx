import { useTranslation } from "react-i18next";

import type { User } from "../../../chat/types";
import { LanguageSwitcher } from "../../LanguageSwitcher";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The account menu itself is designed (Hamlaneh Chat.dc.html, the
 * `chat-addendum-account-menu-*` artboards) and still awaits its reskin. It is
 * anchored above the identity trigger; the gear beside it opens Settings
 * directly, which is where password changes, language and appearance now live.
 *
 * The drawn menu also lists "Change password". That is not implemented here:
 * the settings panel owns the password form (`settings-security`), and two
 * routes to the same form is the duplication this slice was told to avoid.
 * Flagged for the designer in the slice report.
 */

interface AccountMenuProps {
  user: User;
  onLogout: () => void;
  onClose: () => void;
}

export function AccountMenu({ user, onLogout, onClose }: AccountMenuProps) {
  const { t } = useTranslation();

  return (
    <section
      className="hm-plumbing hm-plumbing--footer"
      role="dialog"
      aria-modal="false"
      aria-label={t("account.title")}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <h2>{t("account.title")}</h2>
      <p>{t("account.signedInAs", { name: user.display_name })}</p>
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
