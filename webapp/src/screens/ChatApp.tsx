import { useTranslation } from "react-i18next";
import { Navigate, Route, Routes } from "react-router";

import { AdminApp } from "./AdminApp";
import { ChatShell } from "../components/chat/ChatShell";
import type { ChatShellProps } from "../components/chat/ChatShell";

/**
 * The authenticated app's route table, deliberately tiny:
 *   /                        the first conversation
 *   /c/:channelId            one conversation
 *   /c/:channelId/m/:id      a message permalink ("Copy link"), which the
 *                            history request resolves with the `around` cursor
 *   /admin/*                 the admin dashboard, for admins only
 *
 * The pre-authentication screens stay outside the router — they are chosen by
 * session state, not by URL.
 */
export function ChatApp(props: ChatShellProps) {
  const { t } = useTranslation();

  return (
    <Routes>
      <Route path="/" element={<ChatShell {...props} />} />
      <Route path="/c/:channelId" element={<ChatShell {...props} />} />
      <Route path="/c/:channelId/m/:messageId" element={<ChatShell {...props} />} />
      <Route
        path="/admin/*"
        element={
          <AdminApp
            currentUser={props.currentUser}
            organizationName={props.organizationName ?? t("app.name")}
          />
        }
      />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
