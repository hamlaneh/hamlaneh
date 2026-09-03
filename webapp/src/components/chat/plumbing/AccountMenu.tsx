import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { PRESENCE_LABEL_KEY } from "../../../chat/presence";
import type { Presence, User } from "../../../chat/types";
import { KeyIcon, LoaderCircleIcon, LogOutIcon, XIcon } from "../../icons";
import { LanguageSwitcher } from "../../LanguageSwitcher";
import { Avatar } from "../Avatar";
import { ACCOUNT_MENU_ID, useRestoreFocus } from "./overlay";

/**
 * Account — `chat-addendum-account-menu-light` / `-dark` / `-rtl-fa` /
 * `-mobile` / `-states`, with the contract on
 * `chat-addendum-menu-components` §01–§03.
 *
 * An **anchored non-modal dialog, not an ARIA menu**: it holds a radio group,
 * which is something a menu cannot. `role="dialog"` with `aria-modal="false"`,
 * normal DOM tab order — Close, Change password, the language radiogroup, Log
 * out — no roving tabindex, no arrow keys across the actions, no scrim and no
 * desktop focus trap. Focus enters Close; Escape and the visible Close restore
 * it to the trigger.
 *
 * The language radiogroup keeps native semantics (the checked radio is the
 * single tab stop, arrows move between EN and فا) and its arrows never
 * traverse the other account actions — which is exactly what the delivered
 * `LanguageSwitcher` already does, reused unchanged from the auth set.
 *
 * Log out is neutral, unconfirmed and duplicate-safe, with a stable-width busy
 * label and deliberately no error state: the local session clears even if the
 * server request fails.
 */

interface AccountMenuProps {
  user: User;
  presence: Presence;
  /**
   * Enters the same voluntary change flow the Settings Security panel already
   * owns — no second password form is designed here.
   */
  onChangePassword: () => void;
  onLogout: () => void;
  onClose: () => void;
}

export function AccountMenu({
  user,
  presence,
  onChangePassword,
  onLogout,
  onClose,
}: AccountMenuProps) {
  const { t } = useTranslation();
  const [loggingOut, setLoggingOut] = useState(false);
  const titleId = useId();

  const popoverRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  useRestoreFocus(popoverRef);

  // Focus enters Close (menu-components §02).
  useEffect(() => {
    closeRef.current?.focus();
  }, []);

  return (
    <div
      ref={popoverRef}
      id={ACCOUNT_MENU_ID}
      className="hm-popover hm-popover--account"
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <div className="hm-popover__header">
        <h2 className="hm-popover__title" id={titleId}>
          {t("account.title")}
        </h2>
        <button
          ref={closeRef}
          type="button"
          className="hm-icon-button hm-close-button"
          aria-label={t("chat.common.close")}
          onClick={onClose}
        >
          <XIcon size={18} strokeWidth={1.85} />
        </button>
      </div>

      <div className="hm-account__identity">
        <Avatar
          userId={user.id}
          displayName={user.display_name}
          size={34}
          typeSize={14}
          presence={presence}
          presenceLabel={t(PRESENCE_LABEL_KEY[presence])}
        />
        {/* The whole sentence is one auto-direction run, so a Latin name still
            reads correctly inside the Persian frame. The artboard weights the
            name alone; splitting a translated sentence to do that is worse
            than not doing it, so the emphasis is left to the design pipeline. */}
        <p className="hm-account__signed-in" dir="auto">
          {t("account.signedInAs", { name: user.display_name })}
        </p>
      </div>

      <div className="hm-popover__group">
        <button type="button" className="hm-menu-action" onClick={onChangePassword}>
          <span className="hm-menu-action__glyph">
            <KeyIcon size={17} />
          </span>
          <span className="hm-menu-action__label">{t("account.changePassword")}</span>
        </button>
      </div>

      <div className="hm-account__language">
        <span className="hm-account__language-label">{t("language.label")}</span>
        {/* Applies immediately, keeps the popover open and anchored, and leaves
            focus on the selected radio. Direction changes atomically. */}
        <LanguageSwitcher />
      </div>

      <span className="hm-popover__rule" aria-hidden="true" />

      <div className="hm-popover__group">
        <button
          type="button"
          className="hm-menu-action"
          disabled={loggingOut}
          onClick={() => {
            // Duplicate-safe and deliberately without an error state.
            setLoggingOut(true);
            onLogout();
          }}
        >
          <span className="hm-menu-action__glyph">
            {loggingOut ? (
              <LoaderCircleIcon size={17} className="hm-spin" />
            ) : (
              /* log-out is one of the two glyphs that genuinely mirror: its
                 arrow points toward the logical end. */
              <LogOutIcon size={17} className="hm-mirror-glyph" />
            )}
          </span>
          <span className="hm-menu-action__label">
            {loggingOut ? t("account.loggingOut") : t("account.logout")}
          </span>
        </button>
      </div>
    </div>
  );
}
