import { useState } from "react";
import { Navigate, Route, Routes } from "react-router";

import { AdminAuditLog } from "../components/admin/AdminAuditLog";
import { AdminInvites } from "../components/admin/AdminInvites";
import { AdminOrgSettings } from "../components/admin/AdminOrgSettings";
import { AdminUsers } from "../components/admin/AdminUsers";
import type { User } from "../chat/types";

interface AdminAppProps {
  currentUser: User;
  organizationName: string;
}

/**
 * The dashboard's route table:
 *   /admin           users
 *   /admin/invites   open invite links
 *   /admin/org       instance settings
 *   /admin/audit     the log
 *
 * Guarded here as well as on the server. The server refuses regardless — that
 * is the authority — but a dashboard that paints and then errors is a lie, so
 * a non-admin is sent back to chat before anything renders.
 */
export function AdminApp({ currentUser, organizationName }: AdminAppProps) {
  /* Renaming the organization is an admin action, and the sidebar carries the
   * name; take the new one straight from the PATCH rather than reloading. */
  const [name, setName] = useState(organizationName);

  if (!currentUser.is_admin) {
    return <Navigate to="/" replace />;
  }

  const shared = { currentUser, organizationName: name };

  return (
    <Routes>
      <Route index element={<AdminUsers {...shared} />} />
      <Route path="invites" element={<AdminInvites {...shared} />} />
      <Route
        path="org"
        element={<AdminOrgSettings {...shared} onOrganizationRenamed={setName} />}
      />
      <Route path="audit" element={<AdminAuditLog {...shared} />} />
      <Route path="*" element={<Navigate to="/admin" replace />} />
    </Routes>
  );
}
