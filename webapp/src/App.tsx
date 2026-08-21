import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { api } from "./api/client";
import type { components } from "./api/schema";
import { LanguageSwitcher } from "./components/LanguageSwitcher";
import { ChangePasswordScreen } from "./screens/ChangePasswordScreen";
import { HomeScreen } from "./screens/HomeScreen";
import { LoginScreen } from "./screens/LoginScreen";

type User = components["schemas"]["User"];

type Session =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "authenticated"; user: User };

/** Asks the server who is signed in (GET /users/me) and maps it to a Session. */
async function fetchSession(): Promise<Session> {
  try {
    const { data } = await api.GET("/api/v1/users/me");
    return data !== undefined
      ? { status: "authenticated", user: data }
      : { status: "unauthenticated" };
  } catch (requestError) {
    // Unreachable server is handled as "not signed in": the login screen
    // is the only place to retry from.
    console.warn("Session lookup failed:", requestError);
    return { status: "unauthenticated" };
  }
}

/**
 * Session bootstrap and screen routing by conditional render (no router
 * dependency — two screens do not justify one).
 */
function App() {
  const { t } = useTranslation();
  const [session, setSession] = useState<Session>({ status: "loading" });
  const [showChangePassword, setShowChangePassword] = useState(false);

  const refreshSession = useCallback(() => {
    void fetchSession().then(setSession);
  }, []);

  useEffect(() => {
    // Session bootstrap: resolve the initial "loading" state.
    refreshSession();
  }, [refreshSession]);

  const handleAuthenticated = (user: User) => {
    setShowChangePassword(false);
    setSession({ status: "authenticated", user });
  };

  const handleLogout = () => {
    void (async () => {
      try {
        await api.POST("/api/v1/auth/logout");
      } catch (requestError) {
        // The server-side session may outlive this failure, but locally the
        // user asked to leave — drop to the login screen regardless.
        console.warn("Logout request failed:", requestError);
      }
      setShowChangePassword(false);
      setSession({ status: "unauthenticated" });
    })();
  };

  const handlePasswordChanged = () => {
    setShowChangePassword(false);
    // Refetch /users/me so the cleared must_change_password flag comes from
    // the server, not from a local guess.
    refreshSession();
  };

  let content: ReactNode;
  if (session.status === "loading") {
    content = <p role="status">{t("common.loading")}</p>;
  } else if (session.status === "unauthenticated") {
    content = <LoginScreen onAuthenticated={handleAuthenticated} />;
  } else if (session.user.must_change_password) {
    // Forced mode: while the flag is set this branch always wins — the only
    // exits are completing the change or signing out (wrong account).
    content = (
      <ChangePasswordScreen
        mode="forced"
        onSuccess={handlePasswordChanged}
        onSignOut={handleLogout}
      />
    );
  } else if (showChangePassword) {
    content = (
      <ChangePasswordScreen
        mode="voluntary"
        onSuccess={handlePasswordChanged}
        onCancel={() => {
          setShowChangePassword(false);
        }}
      />
    );
  } else {
    content = (
      <HomeScreen
        user={session.user}
        onLogout={handleLogout}
        onChangePassword={() => {
          setShowChangePassword(true);
        }}
      />
    );
  }

  return (
    <>
      <header>
        <h1>{t("app.name")}</h1>
        <p>{t("app.tagline")}</p>
        <LanguageSwitcher />
      </header>
      <main>{content}</main>
    </>
  );
}

export default App;
