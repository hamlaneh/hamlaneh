import { useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { AdminError, setOrgEncryptionMode } from "../../admin/adminApi";
import type { EncryptionMode, OrgSettings } from "../../admin/adminApi";
import { formatCount } from "../../chat/format";
import { useFocusTrap } from "../settings/useFocusTrap";

/**
 * Which kind of conversation this instance creates — ADR 011, and the one org
 * setting with a ceremony rather than a save-as-you-type field.
 *
 * UNDESIGNED SURFACE — no artboard draws an encryption mode or its switch
 * dialog, so this is plain semantic HTML with no styling beyond structure and
 * none of the delivered panel, switch-row, toggle or confirm-dialog treatments
 * (docs/design/STATUS.md, "Organization encryption mode" and "Encryption-mode
 * switch dialog"). `useFocusTrap` is borrowed because it is behaviour, not a
 * visual decision: a modal that leaks Tab to the page behind it is broken in
 * any design.
 *
 * The contract publishes both totals rather than one "outside the current
 * mode" count, and that shape was chosen because of this screen: each
 * direction's disclosure names the conversations that will be outside the mode
 * being CHOSEN — the encrypted ones when choosing Compliance, the
 * server-readable ones when choosing Strict — which is the other set in both
 * directions. A single current-mode count would have printed the plaintext
 * total where the encrypted one belongs, on a screen about encryption.
 *
 * Neither total moves when the mode changes: conversations are never
 * converted, which is ADR 011 decision 2 restated as data.
 */

const MODES = ["strict", "compliance"] as const;

interface EncryptionModeSectionProps {
  settings: OrgSettings;
  /** Hands the screen the settings the instance now holds, after a switch. */
  onSwitched: (next: OrgSettings) => void;
}

export function EncryptionModeSection({ settings, onSwitched }: EncryptionModeSectionProps) {
  const { t, i18n } = useTranslation();
  const fieldId = useId();
  const lockedId = useId();

  /** The mode the admin picked, awaiting the dialog's confirmation. */
  const [pending, setPending] = useState<EncryptionMode | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const current = settings.encryption_mode;
  const outside =
    current === "strict" ? settings.plaintext_conversations : settings.encrypted_conversations;

  const confirm = (mode: EncryptionMode) => {
    setBusy(true);
    setFailure(null);
    void (async () => {
      try {
        onSwitched(await setOrgEncryptionMode(mode));
        setPending(null);
      } catch (requestError) {
        console.warn("Changing the encryption mode failed:", requestError);
        setFailure(
          // The one refusal with a reason worth repeating. It should be
          // unreachable from here — Compliance is drawn disabled — but an
          // instance can lock it while this screen is open.
          requestError instanceof AdminError && requestError.code === "encryption_mode_locked"
            ? t("admin.org.encryption.complianceLocked")
            : t("admin.org.encryption.switchFailed"),
        );
      } finally {
        setBusy(false);
      }
    })();
  };

  return (
    <section>
      {/* What the current mode does NOT describe: the plaintext ones under
          strict, the encrypted ones under compliance. Derived rather than
          served as one number, so the switch dialog can name the other set —
          see the note above. */}
      <h2>{t("admin.org.encryption.title")}</h2>
      <p>{t("admin.org.encryption.intro")}</p>
      <p>{t(`admin.org.encryption.current.${current}`)}</p>
      {/* Permanent, and shown at zero as well: a count that appears only when
          it is non-zero teaches an admin that silence means the mode covers
          everything, which is the implication ADR 011 decision 2 refuses. */}
      <p>
        {outside === 0
          ? t("admin.org.encryption.outside.none")
          : t(`admin.org.encryption.outside.${current}`, {
              conversations: formatCount(outside, i18n.language),
            })}
      </p>

      <fieldset disabled={busy}>
        <legend>{t("admin.org.encryption.chooseLabel")}</legend>
        {MODES.map((mode) => (
          <p key={mode}>
            <label>
              <input
                type="radio"
                name={`${fieldId}-encryption-mode`}
                value={mode}
                checked={current === mode}
                // Shown, never hidden, and refused with its reason beside it:
                // a hidden option teaches nobody what this product will offer,
                // and an offered one would be the dishonest toggle (ADR 011
                // decision 3). Whatever the instance actually holds stays
                // selectable, so a compliance instance is not drawn as
                // sitting in an unavailable mode.
                disabled={mode === "compliance" && current !== "compliance"}
                aria-describedby={mode === "compliance" ? lockedId : undefined}
                onChange={() => {
                  setFailure(null);
                  setPending(mode);
                }}
              />
              {t(`admin.org.encryption.mode.${mode}`)}
            </label>
            <span> {t(`admin.org.encryption.modeHint.${mode}`)}</span>
          </p>
        ))}
        <p id={lockedId}>{t("admin.org.encryption.complianceLocked")}</p>
      </fieldset>

      {failure === null || pending !== null ? null : <p role="alert">{failure}</p>}

      {pending === null ? null : (
        <EncryptionModeSwitchDialog
          to={pending}
          conversationsOutsideTarget={
            pending === "strict" ? settings.plaintext_conversations : settings.encrypted_conversations
          }
          busy={busy}
          error={failure}
          onConfirm={() => {
            confirm(pending);
          }}
          onCancel={() => {
            setPending(null);
            setFailure(null);
          }}
        />
      )}
    </section>
  );
}

interface EncryptionModeSwitchDialogProps {
  /** The mode being chosen; its own direction decides every sentence. */
  to: EncryptionMode;
  conversationsOutsideTarget: number;
  busy: boolean;
  error: string | null;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * The disclosure ADR 011 decision 2 fixes, in both directions.
 *
 * Both say the same thing about what already exists: nothing changes. Neither
 * direction is a conversion, and the reason each is impossible is the reason
 * the product works — a strict-to-compliance switch cannot decrypt what this
 * server has no key for, and a compliance-to-strict switch cannot un-read what
 * the server already read. Exported so the direction that cannot be reached
 * from the screen while Compliance is locked still has its copy under test.
 */
export function EncryptionModeSwitchDialog({
  to,
  conversationsOutsideTarget,
  busy,
  error,
  onConfirm,
  onCancel,
}: EncryptionModeSwitchDialogProps) {
  const { t, i18n } = useTranslation();
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const handleTrapKey = useFocusTrap(dialogRef);

  return (
    <div
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      tabIndex={-1}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onCancel();
          return;
        }
        handleTrapKey(event);
      }}
    >
      <h3 id={titleId}>{t(`admin.org.encryption.switch.${to}.title`)}</h3>
      {/* Sentence one in both directions: nothing already stored changes. */}
      <p>{t(`admin.org.encryption.switch.${to}.unchanged`)}</p>
      <p>{t(`admin.org.encryption.switch.${to}.begins`)}</p>
      {to === "compliance" ? (
        <p>{t("admin.org.encryption.switch.compliance.exportImpossible")}</p>
      ) : null}
      {/* The live blast radius, before confirming rather than from support
          requests afterwards — the house pattern accounts_without_totp set.

          Keyed on the mode being CHOSEN, not the one in force: this sentence
          is about what the new mode will not describe, and naming the current
          mode's leftovers here would answer a question nobody asked at the
          moment somebody is deciding. */}
      <p>
        {conversationsOutsideTarget === 0
          ? t("admin.org.encryption.outside.none")
          : t(`admin.org.encryption.outside.${to}`, {
              conversations: formatCount(conversationsOutsideTarget, i18n.language),
            })}
      </p>
      {error === null ? null : <p role="alert">{error}</p>}
      <p>
        <button type="button" disabled={busy} onClick={onConfirm}>
          {busy
            ? t("admin.status.saving")
            : t(`admin.org.encryption.switch.${to}.confirm`)}
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          {t("chat.common.cancel")}
        </button>
      </p>
    </div>
  );
}
