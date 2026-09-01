import { useTranslation } from "react-i18next";

import { isolateAuto } from "../../i18n/bidi";
import { Avatar } from "../chat/Avatar";
import { PhoneOutgoingIcon } from "./icons";

interface CallRingProps {
  /** Whoever is calling, already resolved to a name. */
  callerName: string;
  onAccept: () => void;
  onDismiss: () => void;
}

/**
 * `call-ring`: somebody is calling in a direct message.
 *
 * Caller identity, Answer, Dismiss, and the last line says out loud that there
 * is nothing behind it. Per ADR 005 there is no decline-versus-busy
 * distinction, no missed-call message afterwards and no ring timeout —
 * dismissing dismisses the toast and the caller learns nothing, so there is no
 * state to keep and this component keeps none.
 *
 * The avatar tint hashes the name rather than the user id: this component is
 * given only a resolved name (`ChatShell`), so the same person can carry a
 * different tint here than in the sidebar. Fixing that is a prop change in
 * `ChatShell`, which this slice does not own.
 */
export function CallRing({ callerName, onAccept, onDismiss }: CallRingProps) {
  const { t } = useTranslation();

  return (
    // Assertive because it is the one call surface a person sees without having
    // chosen to be in a call, and it is worth interrupting for exactly once.
    <section className="hm-call-ring" aria-label={t("calls.ring.label")} aria-live="assertive">
      <div className="hm-call-ring__caller">
        <Avatar userId={callerName} displayName={callerName} size={42} typeSize={16} />
        <p className="hm-call-ring__from" dir="auto">
          {t("calls.ring.from", { name: isolateAuto(callerName) })}
        </p>
      </div>
      <div className="hm-call-ring__actions">
        <button type="button" className="hm-call-ring__answer" onClick={onAccept}>
          <PhoneOutgoingIcon size={17} strokeWidth={1.85} />
          {t("calls.ring.accept")}
        </button>
        <button type="button" className="hm-call-button" onClick={onDismiss}>
          {t("calls.ring.dismiss")}
        </button>
      </div>
      <p className="hm-call-ring__note">{t("calls.ring.note")}</p>
    </section>
  );
}
