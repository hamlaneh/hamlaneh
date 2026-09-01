import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, NavLink } from "react-router";

import { formatCount } from "../../chat/format";
import type { User } from "../../chat/types";
import { Avatar } from "../chat/Avatar";
import {
  ArrowLeftIcon,
  LinkIcon,
  NestMark,
  ScrollTextIcon,
  SettingsIcon,
  ShieldIcon,
  UsersIcon,
} from "../icons";

/**
 * The four sections the artboards draw, in their order, followed by the one
 * they do not: provisioning tokens arrived after the design and is appended
 * rather than inserted, so the drawn four keep their drawn positions. Its
 * glyph is borrowed (docs/design/STATUS.md, "SCIM provisioning tokens").
 */
const SECTIONS = [
  { to: "/admin", end: true, key: "users", Icon: UsersIcon },
  { to: "/admin/invites", end: false, key: "invites", Icon: LinkIcon },
  { to: "/admin/org", end: false, key: "org", Icon: SettingsIcon },
  { to: "/admin/audit", end: false, key: "audit", Icon: ScrollTextIcon },
  { to: "/admin/scim", end: false, key: "scim", Icon: ShieldIcon },
] as const;

type SectionKey = (typeof SECTIONS)[number]["key"];

export interface AdminShellProps {
  currentUser: User;
  organizationName: string;
  /**
   * Row counts for the nav, keyed by section. A screen supplies only the
   * count it has actually loaded — the users screen knows how many users
   * there are, and nothing else fetches a list it does not draw just to put
   * a number beside it.
   *
   * Keyed rather than a field per section so the lookup below cannot go out
   * of step with SECTIONS: a ternary here handed `org` and `audit` the
   * invites count, and the invites screen drew `0` beside all three.
   */
  counts?: Partial<Record<SectionKey, number | undefined>>;
  /** Page title, subtitle and the one action the artboard puts beside them. */
  title: string;
  subtitle: string;
  action?: ReactNode;
  /** The filter bar, when the screen has one. */
  filters?: ReactNode;
  children: ReactNode;
}

/**
 * The admin frame: white sidebar, `Back to chat` above the org identity, and
 * exactly one `ADMINISTRATION` kicker — the three things that separate this
 * mode from chat, and nothing else does (ADMIN_HANDOFF "The mode signal").
 *
 * Geometry is the handoff's: sidebar 260, content padding-inline 40, nav row
 * 44. The same markup mirrors under dir="rtl" — no RTL branch anywhere.
 */
export function AdminShell({
  currentUser,
  organizationName,
  counts,
  title,
  subtitle,
  action,
  filters,
  children,
}: AdminShellProps) {
  const { t, i18n } = useTranslation();

  return (
    <div className="hm-admin">
      <nav className="hm-admin__sidebar" aria-label={t("admin.nav.label")}>
        <div className="hm-admin__head">
          {/* First in the tab order: admin is somewhere you visit. */}
          <Link className="hm-admin__exit" to="/">
            <ArrowLeftIcon size={17} strokeWidth={1.85} />
            {t("admin.backToChat")}
          </Link>
          <div className="hm-admin__org">
            <NestMark size={26} />
            <span className="hm-admin__org-text">
              <span className="hm-admin__org-name" dir="auto">
                {organizationName}
              </span>
              <span className="hm-admin__kicker">{t("admin.kicker")}</span>
            </span>
          </div>
        </div>

        <div className="hm-admin__nav">
          <ul className="hm-admin__nav-list">
            {SECTIONS.map(({ to, end, key, Icon }) => {
              const count = counts?.[key];
              return (
                <li key={key}>
                  <NavLink className="hm-admin__nav-row" to={to} end={end}>
                    <Icon size={18} strokeWidth={1.85} />
                    <span className="hm-admin__nav-label">{t(`admin.nav.${key}`)}</span>
                    {count === undefined ? null : (
                      <span className="hm-admin__nav-count">
                        {formatCount(count, i18n.language)}
                      </span>
                    )}
                  </NavLink>
                </li>
              );
            })}
          </ul>
        </div>

        <div className="hm-admin__me">
          <Avatar
            userId={currentUser.id}
            displayName={currentUser.display_name}
            size={32}
            typeSize={13}
          />
          <span className="hm-admin__me-text">
            <span className="hm-admin__me-name">{currentUser.display_name}</span>
            <span className="hm-admin__me-role">{t("admin.role.admin")}</span>
          </span>
        </div>
      </nav>

      <main className="hm-admin__content">
        <div className="hm-admin__header">
          <div className="hm-admin__heading">
            <h1 className="hm-admin__title">{title}</h1>
            <p className="hm-admin__subtitle">{subtitle}</p>
          </div>
          {action}
        </div>
        {filters === undefined ? null : <div className="hm-admin__filters">{filters}</div>}
        <div className="hm-admin__body">{children}</div>
      </main>
    </div>
  );
}
