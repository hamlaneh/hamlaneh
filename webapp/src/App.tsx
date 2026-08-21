import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BrowserRouter } from "react-router";

import { api } from "./api/client";
import type { components } from "./api/schema";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthShell } from "./components/auth/AuthShell";
import { ChangePasswordScreen } from "./screens/ChangePasswordScreen";
import { ChatApp } from "./screens/ChatApp";
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
 * UNDESIGNED SURFACE — no artboard draws a crash state, so this is plain
 * semantic HTML with no styling beyond structure.
 *
 * It says what happened and offers the one recovery that is honestly
 * available, and it never shows the error text: that can carry message
 * content or an id, and it belongs in the console, not on the page.
 */
function CrashFallback() {
  const { t } = useTranslation();
  return (
    <div role="alert">
      <h1>{t("app.error.title")}</h1>
      <p>{t("app.error.body")}</p>
      <button
        type="button"
        onClick={() => {
          window.location.reload();
        }}
      >
        {t("app.error.reload")}
      </button>
    </div>
  );
}

/**
 * Session bootstrap. Which pre-authentication screen shows is decided by
 * session state, not by URL, so those stay a conditional render and each
 * brings its own AuthShell. Once signed in, the chat shell mounts behind a
 * router: channels are addressable and a message has a permalink.
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

  if (session.status === "loading") {
    return (
      <AuthShell>
        <p className="hm-form__helper" role="status">
          {t("common.loading")}
        </p>
      </AuthShell>
    );
  }

  if (session.status === "unauthenticated") {
    return <LoginScreen onAuthenticated={handleAuthenticated} />;
  }

  if (session.user.must_change_password) {
    // Forced mode: while the flag is set this branch always wins — the only
    // exits are completing the change or signing out (wrong account).
    return (
      <ChangePasswordScreen
        mode="forced"
        onSuccess={handlePasswordChanged}
        onSignOut={handleLogout}
      />
    );
  }

  if (showChangePassword) {
    return (
      <ChangePasswordScreen
        mode="voluntary"
        onSuccess={handlePasswordChanged}
        onCancel={() => {
          setShowChangePassword(false);
        }}
      />
    );
  }

  // The chat shell is the authenticated app; only it needs a URL, so the
  // router starts here. The boundary wraps it because this is the surface a
  // hostile link reaches: without one, a single throw blanks the page.
  return (
    <ErrorBoundary fallback={<CrashFallback />}>
      <BrowserRouter>
        <ChatApp
          currentUser={session.user}
          onLogout={handleLogout}
          onChangePassword={() => {
            setShowChangePassword(true);
          }}
        />
      </BrowserRouter>
    </ErrorBoundary>
  );
}

export default App;
