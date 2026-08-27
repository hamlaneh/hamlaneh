import { useTranslation } from "react-i18next";

import { isolateAuto } from "../../i18n/bidi";

interface CallRingProps {
  /** Whoever is calling, already resolved to a name. */
  callerName: string;
  onAccept: () => void;
  onDismiss: () => void;
}

/**
 * `call-ring`: somebody is calling in a direct message.
 *
 * Deliberately thin, per ADR 005: there is no decline-versus-busy distinction,
 * no missed-call message afterwards and no ring timeout. Dismissing dismisses
 * the toast and the caller learns nothing — so there is no state to keep, and
 * this component keeps none.
 *
 * UNDESIGNED SURFACE — `docs/design/STATUS.md` has the call view PENDING, so
 * this is plain semantic HTML with no styling beyond structure.
 */
export function CallRing({ callerName, onAccept, onDismiss }: CallRingProps) {
  const { t } = useTranslation();

  return (
    // Assertive because it is the one call surface a person sees without having
    // chosen to be in a call, and it is worth interrupting for exactly once.
    <section aria-label={t("calls.ring.label")} aria-live="assertive">
      <p>{t("calls.ring.from", { name: isolateAuto(callerName) })}</p>
      <button type="button" onClick={onAccept}>
        {t("calls.ring.accept")}
      </button>
      <button type="button" onClick={onDismiss}>
        {t("calls.ring.dismiss")}
      </button>
    </section>
  );
}
