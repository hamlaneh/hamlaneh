import { useTranslation } from "react-i18next";

import { TwoFactorSetup } from "../components/settings/TwoFactorSetup";

interface TotpEnrolmentScreenProps {
  onEnrolled: () => void;
  onSignOut: () => void;
}

/**
 * UNDESIGNED SURFACE — no artboard draws forced enrolment (docs/design/
 * STATUS.md), so the frame around the flow is plain semantic HTML with no
 * styling of its own. The flow inside it is the delivered three-step setup,
 * unchanged, and keeps the design it was drawn with.
 *
 * Reached only while the session carries `totp_enrollment_required`: the
 * organization turned the policy on and this account has no authenticator. The
 * person did not choose this, so the copy says whose requirement it is and
 * what it costs, and the only way past it other than finishing is signing out
 * — which is what `cancelLabel` renames every exit to.
 *
 * Activation clears the flag server-side, so `onEnrolled` refetches the
 * session rather than signing anyone in again.
 */
export function TotpEnrolmentScreen({ onEnrolled, onSignOut }: TotpEnrolmentScreenProps) {
  const { t } = useTranslation();

  return (
    <main>
      <h1>{t("totpRequired.title")}</h1>
      <p>{t("totpRequired.body")}</p>
      <TwoFactorSetup
        cancelLabel={t("totpRequired.signOut")}
        onCancel={onSignOut}
        onActivated={onEnrolled}
      />
    </main>
  );
}
