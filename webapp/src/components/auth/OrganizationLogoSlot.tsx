/** The instance's own identity, set by an admin. Absent on a fresh install. */
export interface Organization {
  name: string;
  logoUrl: string;
}

interface OrganizationLogoSlotProps {
  organization?: Organization | undefined;
}

/**
 * Renders the organization mark above the wordmark. When the instance has no
 * logo the slot renders nothing at all — no box, no reserved height, no
 * residual gap (delivered design, "OrganizationLogoSlot · absent").
 */
export function OrganizationLogoSlot({ organization }: OrganizationLogoSlotProps) {
  if (organization === undefined) {
    return null;
  }

  return (
    <div className="hm-identity__organization">
      {/* Decorative: the name beside it is the accessible text. */}
      <img className="hm-identity__logo" src={organization.logoUrl} alt="" />
      <span className="hm-identity__organization-name">{organization.name}</span>
    </div>
  );
}
