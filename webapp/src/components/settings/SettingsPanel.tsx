import { useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import { useTranslation } from "react-i18next";

import { AppearanceSection } from "./AppearanceSection";
import { ConfirmDialog } from "./ConfirmDialog";
import { LanguageSection } from "./LanguageSection";
import { RecoveryCodesStep } from "./RecoveryCodesStep";
import { SecuritySection } from "./SecuritySection";
import { SessionsSection } from "./SessionsSection";
import { TwoFactorSetup } from "./TwoFactorSetup";
import { useFocusTrap } from "./useFocusTrap";
import { useChangePassword } from "../../auth/useChangePassword";
import { useSessions } from "../../settings/useSessions";
import { useTotpStatus } from "../../settings/useTotpStatus";
import { LanguagesIcon, ShieldIcon, SunIcon, XIcon } from "../icons";

/**
 * The nav rows, in drawn order. `Profile` is deliberately absent: the artboard
 * draws an editable display name and an avatar upload, and neither has an
 * endpoint yet — a row leading to controls that cannot save anything would be
 * worse than the missing row. It returns with the profile endpoints.
 */
const SECTIONS = ["language", "security", "appearance"] as const;
type Section = (typeof SECTIONS)[number];

const SECTION_ICON = {
  language: LanguagesIcon,
  security: ShieldIcon,
  appearance: SunIcon,
} as const;

/** Which Security screen is showing; every one of them keeps Security selected. */
type SecurityView =
  | { kind: "overview" }
  | { kind: "sessions" }
  | { kind: "totpSetup" }
  | { kind: "newCodes"; codes: readonly string[] };

interface SettingsPanelProps {
  onClose: () => void;
  /** Focus goes back here on close — the sidebar gear, per the handoff. */
  restoreFocusRef: RefObject<HTMLElement | null>;
}

/**
 * User settings, as a panel over the dimmed chat rather than a page of its
 * own: settings are a detour, so the conversation stays visible behind them
 * and Escape puts you back in it.
 *
 * Panel 1040 × 720, radius 16, header 68, nav 220 with 44px rows, content
 * padding 28/32, over the same scrim the chat drawer uses.
 */
export function SettingsPanel({ onClose, restoreFocusRef }: SettingsPanelProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const tabId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef, restoreFocusRef);

  const [section, setSection] = useState<Section>("security");
  const [securityView, setSecurityView] = useState<SecurityView>({ kind: "overview" });
  const [confirmLeave, setConfirmLeave] = useState(false);

  /**
   * The password form's refs live here rather than inside the hook: a
   * component may not read ref values while rendering, and the Security
   * section needs to hand these to its fields.
   */
  const currentRef = useRef<HTMLInputElement>(null);
  const nextRef = useRef<HTMLInputElement>(null);
  const confirmRef = useRef<HTMLInputElement>(null);
  const alertRef = useRef<HTMLDivElement>(null);
  const passwordRefs = useMemo(
    () => ({ current: currentRef, next: nextRef, confirm: confirmRef, alert: alertRef }),
    [],
  );

  const password = useChangePassword(passwordRefs);
  const totp = useTotpStatus();
  const sessions = useSessions();

  const submitPassword = async () => {
    if (await password.submit()) {
      password.clear();
      // The contract revokes every OTHER session on a password change, so the
      // list on screen is stale the moment this succeeds.
      await sessions.reload();
      return true;
    }
    return false;
  };

  /** Escape and the close control both come through here. */
  const requestClose = () => {
    if (password.dirty) {
      setConfirmLeave(true);
      return;
    }
    onClose();
  };

  const selectSection = (next: Section) => {
    setSection(next);
    // Every Security sub-screen is reached from the overview, so the nav row
    // is also the way back out of one.
    setSecurityView({ kind: "overview" });
  };

  const moveSelection = (delta: number) => {
    const index = SECTIONS.indexOf(section);
    const next = SECTIONS[(index + delta + SECTIONS.length) % SECTIONS.length];
    if (next !== undefined) {
      selectSection(next);
      document.getElementById(`${tabId}-${next}`)?.focus();
    }
  };

  const handleNavKey = (event: KeyboardEvent<HTMLDivElement>) => {
    // A vertical tab list moves with Up/Down, and Home/End jump the ends.
    if (event.key === "ArrowDown") {
      event.preventDefault();
      moveSelection(1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveSelection(-1);
    } else if (event.key === "Home") {
      event.preventDefault();
      moveSelection(-SECTIONS.indexOf(section));
    } else if (event.key === "End") {
      event.preventDefault();
      moveSelection(SECTIONS.length - 1 - SECTIONS.indexOf(section));
    }
  };

  return (
    <div className="hm-settings-scrim">
      <div
        ref={dialogRef}
        className="hm-settings"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            requestClose();
            return;
          }
          handleTrapKey(event);
        }}
      >
        <header className="hm-settings__header">
          <h2 className="hm-settings__product-title" id={titleId}>
            {t("settings.title")}
          </h2>
          <button
            type="button"
            className="hm-settings__close"
            aria-label={t("settings.close")}
            onClick={requestClose}
          >
            <XIcon size={19} strokeWidth={1.85} />
          </button>
        </header>

        <div className="hm-settings__body">
          <div
            className="hm-settings__nav"
            role="tablist"
            aria-orientation="vertical"
            aria-label={t("settings.title")}
            onKeyDown={handleNavKey}
          >
            {SECTIONS.map((name) => {
              const Icon = SECTION_ICON[name];
              const selected = section === name;
              return (
                <button
                  key={name}
                  id={`${tabId}-${name}`}
                  type="button"
                  role="tab"
                  className="hm-settings__nav-row"
                  aria-selected={selected}
                  aria-controls={`${tabId}-panel`}
                  tabIndex={selected ? 0 : -1}
                  onClick={() => {
                    selectSection(name);
                  }}
                >
                  <Icon size={18} strokeWidth={1.85} />
                  {t(`settings.nav.${name}`)}
                </button>
              );
            })}
          </div>

          <div
            className="hm-settings__content"
            id={`${tabId}-panel`}
            role="tabpanel"
            aria-labelledby={`${tabId}-${section}`}
            tabIndex={0}
          >
            {section === "language" ? (
              <LanguageSection />
            ) : section === "appearance" ? (
              <AppearanceSection />
            ) : securityView.kind === "sessions" ? (
              <SessionsSection sessions={sessions} />
            ) : securityView.kind === "totpSetup" ? (
              <TwoFactorSetup
                onCancel={() => {
                  setSecurityView({ kind: "overview" });
                }}
                onActivated={() => {
                  void totp.reload();
                  setSecurityView({ kind: "overview" });
                }}
              />
            ) : securityView.kind === "newCodes" ? (
              <RecoveryCodesStep
                codes={securityView.codes}
                confirmLabel={t("settings.totp.codes.done")}
                busyLabel={t("settings.working")}
                busy={false}
                onConfirm={() => {
                  void totp.reload();
                  setSecurityView({ kind: "overview" });
                }}
              />
            ) : (
              <SecuritySection
                password={password}
                currentRef={currentRef}
                nextRef={nextRef}
                confirmRef={confirmRef}
                alertRef={alertRef}
                passwordSaved={password.saved}
                onSubmitPassword={() => {
                  void submitPassword();
                }}
                totp={totp}
                sessions={sessions}
                onManageSessions={() => {
                  setSecurityView({ kind: "sessions" });
                }}
                onSetUpTotp={() => {
                  setSecurityView({ kind: "totpSetup" });
                }}
                onTotpDisabled={() => {
                  void totp.reload();
                }}
                onRecoveryCodesRegenerated={(codes) => {
                  setSecurityView({ kind: "newCodes", codes });
                }}
              />
            )}
          </div>
        </div>
      </div>

      {confirmLeave ? (
        <ConfirmDialog
          title={t("settings.leave.title")}
          body={t("settings.leave.body")}
          confirmLabel={t("settings.leave.save")}
          busyLabel={t("changePassword.submitting")}
          cancelLabel={t("settings.leave.discard")}
          tone="primary"
          busy={password.submitting}
          onConfirm={() => {
            void submitPassword().then((saved) => {
              setConfirmLeave(false);
              // A failed save keeps the panel open with the reason on screen;
              // nothing typed is thrown away behind the user's back.
              if (saved) {
                onClose();
              }
            });
          }}
          onCancel={() => {
            setConfirmLeave(false);
            password.clear();
            onClose();
          }}
          onDismiss={() => {
            // Escape here means "not yet", never "throw it away".
            setConfirmLeave(false);
          }}
        />
      ) : null}
    </div>
  );
}
