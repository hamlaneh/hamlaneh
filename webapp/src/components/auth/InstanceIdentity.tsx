import { ArcMotif } from "../icons";
import type { Organization } from "./OrganizationLogoSlot";
import { OrganizationLogoSlot } from "./OrganizationLogoSlot";
import { ProductWordmark } from "./ProductWordmark";

interface InstanceIdentityProps {
  organization?: Organization | undefined;
}

/**
 * The identity panel: 40% of the shell at 1440, collapsing to a brand header
 * below 900px (stylesheet). Carries the organization slot, the wordmark and
 * the decorative arc motif.
 */
export function InstanceIdentity({ organization }: InstanceIdentityProps) {
  return (
    <div className="hm-auth__identity">
      <ArcMotif className="hm-identity__motif" />
      <div className="hm-identity__content">
        <OrganizationLogoSlot organization={organization} />
        <ProductWordmark />
      </div>
    </div>
  );
}
