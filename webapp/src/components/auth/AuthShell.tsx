import type { CSSProperties, ReactNode } from "react";

import { LanguageSwitcher } from "../LanguageSwitcher";
import { InstanceIdentity } from "./InstanceIdentity";
import type { Organization } from "./OrganizationLogoSlot";

interface AuthShellProps {
  children: ReactNode;
  organization?: Organization | undefined;
  /**
   * Distance the form column sits above the mathematical centre, in px.
   * The artboards use 44 for sign-in and 16 for the taller password form.
   */
  opticalRise?: number;
}

/**
 * Every pre-authentication screen: identity panel, language switcher and the
 * 424px form column, in the geometry of the delivered design. The same markup
 * mirrors under dir="rtl" — no RTL-specific branch anywhere.
 */
export function AuthShell({ children, organization, opticalRise = 44 }: AuthShellProps) {
  const style = { "--hm-optical-rise": `${String(opticalRise)}px` } as CSSProperties;

  return (
    <div className="hm-auth">
      <InstanceIdentity organization={organization} />
      <div className="hm-auth__switcher">
        <LanguageSwitcher />
      </div>
      <div className="hm-auth__region">
        <div className="hm-auth__column" style={style}>
          {children}
        </div>
      </div>
    </div>
  );
}
