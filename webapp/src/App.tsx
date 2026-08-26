import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BrowserRouter } from "react-router";

import { api } from "./api/client";
import type { components } from "./api/schema";
import { readInviteToken } from "./auth/inviteToken";
import { consumeResetToken } from "./auth/resetToken";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthShell } from "./components/auth/AuthShell";
import { useAccountLanguage } from "./i18n/useLanguage";
import { InstanceProvider } from "./instance/InstanceProvider";
import { ChangePasswordScreen } from "./screens/ChangePasswordScreen";
import { ChatApp } from "./screens/ChatApp";
import { LoginScreen } from "./screens/LoginScreen";
import type { LoginNotice } from "./screens/LoginScreen";
import { RedeemInviteScreen } from "./screens/RedeemInviteScreen";
import { ResetPasswordScreen } from "./screens/ResetPasswordScreen";
import { ResetRequestScreen } from "./screens/ResetRequestScreen";
import { TotpChallengeScreen } from "./screens/TotpChallengeScreen";

type User = components["schemas"]["User"];

type Session =
  | { status: "loading" }
  | { status: "unauthenticated" }
  | { status: "authenticated"; user: User };

/**
 * Which pre-authentication screen is showing. Sign-in is two halves now — a
 * 202 from login means the password was right but no session exists yet — and
 * the reset screens are reached from the sign-in screen or from an emailed
 * link, never from a session state.
 */
type AuthFlow =
  | { screen: "login"; notice?: LoginNotice }
  | { screen: "totp" }
  | { screen: "resetRequest" }
  | { screen: "resetPassword"; token: string }
  | { screen: "redeemInvite"; token: string };

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
 * session state and by the sign-in flow, not by URL, so those stay a
 * conditional render and each brings its own AuthShell. Once signed in, the
 * chat shell mounts behind a router: channels are addressable and a message
 * has a permalink.
 */
function App() {
  const { t } = useTranslation();
  const [session, setSession] = useState<Session>({ status: "loading" });
  /**
   * Read once, at the first render, and scrubbed from the address bar in the
   * same breath. A token in the link's fragment outranks everything: it is a
   * recovery path, and it must work even for someone who is already signed in
   * somewhere.
   */
  const [flow, setFlow] = useState<AuthFlow>(() => {
    const resetToken = consumeResetToken();
    if (resetToken !== null) {
      return { screen: "resetPassword", token: resetToken };
    }
    // A join link is the only way into an instance whose registration is
    // closed, so it outranks the sign-in screen the same way a reset link
    // does — somebody following an invitation has no account to sign in with.
    const inviteToken = readInviteToken();
    return inviteToken === null
      ? { screen: "login" }
      : { screen: "redeemInvite", token: inviteToken };
  });

  const refreshSession = useCallback(() => {
    void fetchSession().then(setSession);
  }, []);

  useEffect(() => {
    // Session bootstrap: resolve the initial "loading" state.
    refreshSession();
  }, [refreshSession]);

  // The language follows the person, not the browser: the account's locale
  // takes over the moment there is an account, and every later switch is
  // saved back to it. See useAccountLanguage for why the account outranks
  // what this browser remembered.
  useAccountLanguage(session.status === "authenticated" ? session.user.locale : null);

  useEffect(() => {
    // index.html ships an English <title> the app never revisited, so the tab
    // read "Hamlaneh" while the whole interface was in Persian. `lang`/`dir`
    // are the i18n module's to set; this is the same idea for the one piece of
    // chrome that lives outside React's tree.
    document.title = t("app.name");
  }, [t]);

  const handleAuthenticated = (user: User) => {
    setFlow({ screen: "login" });
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
      setFlow({ screen: "login" });
      setSession({ status: "unauthenticated" });
    })();
  };

  const handlePasswordChanged = () => {
    // Refetch /users/me so the cleared must_change_password flag comes from
    // the server, not from a local guess.
    refreshSession();
  };

  if (flow.screen === "redeemInvite") {
    return (
      <RedeemInviteScreen
        token={flow.token}
        onAccountCreated={() => {
          // The invite creates the account and nothing else: no session is
          // minted, deliberately, so the new user signs in like anybody
          // else and the sign-in path stays the one way in.
          window.history.replaceState(null, "", "/");
          setSession({ status: "unauthenticated" });
          setFlow({
            screen: "login",
            notice: { tone: "success", message: t("redeemInvite.done") },
          });
        }}
      />
    );
  }

  if (flow.screen === "resetPassword") {
    return (
      <ResetPasswordScreen
        token={flow.token}
        onComplete={() => {
          // The contract revokes every session family on a reset, so there is
          // nothing to return to — the user signs in fresh.
          setSession({ status: "unauthenticated" });
          setFlow({
            screen: "login",
            notice: { tone: "success", message: t("resetPassword.done") },
          });
        }}
        onBackToSignIn={() => {
          setFlow({ screen: "login" });
        }}
        onRequestNewLink={() => {
          setFlow({ screen: "resetRequest" });
        }}
      />
    );
  }

  if (flow.screen === "resetRequest") {
    return (
      <ResetRequestScreen
        onBackToSignIn={() => {
          setFlow({ screen: "login" });
        }}
      />
    );
  }

  if (flow.screen === "totp") {
    return (
      <TotpChallengeScreen
        onAuthenticated={handleAuthenticated}
        onChallengeLost={() => {
          // No live challenge left: say why the password step came back
          // rather than dropping the user there with no explanation.
          setFlow({
            screen: "login",
            notice: { tone: "warning", message: t("totp.error.challengeLost") },
          });
        }}
        onBack={() => {
          setFlow({ screen: "login" });
        }}
      />
    );
  }

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
    return (
      <LoginScreen
        notice={flow.notice}
        onAuthenticated={handleAuthenticated}
        onTwoStepRequired={() => {
          setFlow({ screen: "totp" });
        }}
        onForgotPassword={() => {
          setFlow({ screen: "resetRequest" });
        }}
      />
    );
  }

  if (session.user.must_change_password) {
    // Forced mode: while the flag is set this branch always wins — the only
    // exits are completing the change or signing out (wrong account). A
    // voluntary change is Settings → Security instead.
    return (
      <ChangePasswordScreen onSuccess={handlePasswordChanged} onSignOut={handleLogout} />
    );
  }

  // The chat shell is the authenticated app; only it needs a URL, so the
  // router starts here. The boundary wraps it because this is the surface a
  // hostile link reaches: without one, a single throw blanks the page.
  return (
    <ErrorBoundary fallback={<CrashFallback />}>
      <BrowserRouter>
        <ChatApp currentUser={session.user} onLogout={handleLogout} />
      </BrowserRouter>
    </ErrorBoundary>
  );
}

/** The instance document is policy every screen below reads; fetch it once. */
export default function AppWithInstance() {
  return (
    <InstanceProvider>
      <App />
    </InstanceProvider>
  );
}
