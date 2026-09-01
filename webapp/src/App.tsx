import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BrowserRouter } from "react-router";

import { api } from "./api/client";
import type { components } from "./api/schema";
import { readInviteToken } from "./auth/inviteToken";
import { consumeResetToken } from "./auth/resetToken";
import { consumeSsoLanding } from "./auth/sso";
import { readMeetToken } from "./calls/meetLink";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { AuthShell } from "./components/auth/AuthShell";
import { useAccountLanguage } from "./i18n/useLanguage";
import { InstanceProvider } from "./instance/InstanceProvider";
import { ChangePasswordScreen } from "./screens/ChangePasswordScreen";
import { ChatApp } from "./screens/ChatApp";
import { LoginScreen } from "./screens/LoginScreen";
import type { LoginNotice } from "./screens/LoginScreen";
import { MeetGuestScreen } from "./screens/MeetGuestScreen";
import { RedeemInviteScreen } from "./screens/RedeemInviteScreen";
import { ResetPasswordScreen } from "./screens/ResetPasswordScreen";
import { ResetRequestScreen } from "./screens/ResetRequestScreen";
import { TotpChallengeScreen } from "./screens/TotpChallengeScreen";
import { TotpEnrolmentScreen } from "./screens/TotpEnrolmentScreen";

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
  | { screen: "redeemInvite"; token: string }
  /**
   * A CONFERENCE LINK, AND WHY IT IS HERE RATHER THAN A ROUTE.
   *
   * `/meet/{token}` is the webapp's first unauthenticated *application* URL,
   * so "make it a real route" is the obvious reading — and it is the wrong
   * one twice over. The router (`BrowserRouter`, in the authenticated branch
   * below) exists because the chat shell has things to navigate *between*:
   * channels are addressable and a message has a permalink. The guest page
   * has exactly one screen and no sub-navigation at all, so a route buys it
   * nothing. Paying for it, on the other hand, means hoisting the router
   * above the session gate and turning this whole pre-session conditional —
   * the reset, invite, two-step and forced-change screens, none of which are
   * URLs — into route elements, to give one screen an address it already has.
   *
   * The precedent it actually matches is the invite link: a bearer capability
   * in a path segment, read once at the first render, rendered as a state.
   * The difference from the invite is that this one outranks the *session*
   * too — the same way a reset link does — because a conference link is for
   * whoever opens it, and a member who happens to be signed in joins the
   * meeting exactly as a stranger does. There is no member-flavoured join in
   * the contract.
   */
  | { screen: "meet"; token: string };

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
 *
 * Two of these screens are reached by a *link* rather than by a state — the
 * invitation and the conference link — and both are read out of the address
 * bar here, at the first render, rather than routed to. See AuthFlow above for
 * why the conference link in particular is not a route.
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
    // First, and before the fragment is even read: a conference link is the
    // one address here that belongs to somebody who may have no account at
    // all, so nothing about this instance's sign-in state gets to answer for
    // it. See the AuthFlow note above.
    const meetToken = readMeetToken();
    if (meetToken !== null) {
      return { screen: "meet", token: meetToken };
    }
    const resetToken = consumeResetToken();
    if (resetToken !== null) {
      return { screen: "resetPassword", token: resetToken };
    }
    // A callback that came back owing a second factor. It needs no data of
    // its own — the challenge travels in the cookie the callback set, which is
    // exactly why the contract makes this a query parameter on the root rather
    // than a route: there is nothing for a route to carry, and a path that
    // resolved on its own would imply a state a stale link's visitor is not in.
    if (consumeSsoLanding()?.outcome === "challenge") {
      return { screen: "totp" };
    }
    // A join link is the only way into an instance whose registration is
    // closed, so it outranks the sign-in screen the same way a reset link
    // does — somebody following an invitation has no account to sign in with.
    const inviteToken = readInviteToken();
    return inviteToken === null
      ? { screen: "login" }
      : { screen: "redeemInvite", token: inviteToken };
  });
  /**
   * How a single sign-on attempt failed, if one just did. Read (and scrubbed
   * from the address bar) at the first render, like the reset token above.
   *
   * The *code* is held rather than the sentence, so the notice follows the
   * language switcher the sign-in screen renders instead of freezing in
   * whichever language the callback happened to land in.
   */
  const [ssoError, setSsoError] = useState(() => {
    const landing = consumeSsoLanding();
    return landing?.outcome === "error" ? landing.code : null;
  });

  const refreshSession = useCallback(() => {
    void fetchSession().then(setSession);
  }, []);

  const guest = flow.screen === "meet";

  useEffect(() => {
    // Session bootstrap: resolve the initial "loading" state — except for a
    // conference link, which never reads the answer. A stranger's browser has
    // no reason to be asked who is signed in on somebody else's server.
    if (!guest) {
      refreshSession();
    }
  }, [guest, refreshSession]);

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
    // Whatever went wrong on the way in is over; it must not be waiting on the
    // sign-in screen again after a later sign-out.
    setSsoError(null);
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

  if (flow.screen === "meet") {
    return <MeetGuestScreen token={flow.token} />;
  }

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
        notice={
          // A failed single sign-on is a standing message from wherever the
          // user arrived, which is exactly what `notice` is for. A notice this
          // flow set itself (a completed reset, an expired challenge) is the
          // more recent thing that happened, so it wins.
          flow.notice ??
          (ssoError === null
            ? undefined
            : { tone: "danger", message: t(`login.sso.error.${ssoError}`) })
        }
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
    //
    // PRECEDENCE, when a session carries this AND totp_enrollment_required
    // (an admin-created account, first sign-in, on an instance that requires
    // two-step verification): the password goes first. A temporary password is
    // a credential the account holder does not yet exclusively hold — whoever
    // issued it knows it — and binding a second factor to an account somebody
    // else can still sign into is the weaker order of the two. The password
    // change also revokes every other session, so enrolment then happens on a
    // session nobody else could have.
    return (
      <ChangePasswordScreen onSuccess={handlePasswordChanged} onSignOut={handleLogout} />
    );
  }

  if (session.user.totp_enrollment_required) {
    // The same forced pattern as above, one flag later: the organization
    // requires two-step verification and this account has none. Activation
    // clears the flag on the server, so refetching the session is what lets
    // the app continue into chat without a second sign-in.
    return <TotpEnrolmentScreen onEnrolled={refreshSession} onSignOut={handleLogout} />;
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
