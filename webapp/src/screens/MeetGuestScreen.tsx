import { useCallback, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { MediaConnect } from "../calls/media";
import type { MintTicket } from "../calls/useCallSession";
import { useCallSession } from "../calls/useCallSession";
import { LanguageSwitcher } from "../components/LanguageSwitcher";
import { CallView } from "../components/calls/CallView";
import { isolateAuto } from "../i18n/bidi";

/** The contract's own bound (openapi.yaml → JoinConferenceRequest.display_name). */
const NAME_MAX = 64;

type Preview =
  | { status: "loading" }
  | { status: "ready"; title: string; active: boolean }
  /**
   * Unknown, expired and revoked are ONE answer — a 404 — and this page does
   * not guess which. Nothing in the response separates them, so a sentence
   * naming a cause would be invention, and a wrong one tells the visitor to do
   * the wrong thing ("wait for it to be reissued" for a link that was
   * deliberately revoked). It is also the property the contract is protecting:
   * a distinct refusal for "revoked" would confirm the conference exists.
   * `RedeemInviteScreen` reached the same shape for the same reason.
   */
  | { status: "unusable" }
  /** Not the same thing: we could not ask. Not a claim about the link. */
  | { status: "unreachable" };

interface MeetGuestScreenProps {
  token: string;
  /** Test seam only — production connects with `livekit-client`. */
  media?: MediaConnect | undefined;
}

/**
 * `meet-guest` — the page a conference link opens for somebody with no account
 * on this instance, and the only unauthenticated application surface in the
 * product besides sign-in.
 *
 * UNDESIGNED SURFACE — `docs/design/STATUS.md` has the call set PENDING and
 * this artboard is not drawn at all, so the page is plain semantic HTML with
 * no styling beyond structure. It deliberately does NOT reuse `AuthShell`,
 * `AuthForm` or `PrimaryButton`, which every other pre-session screen is built
 * from: those are the *sign-in* treatment, and wearing it is precisely how a
 * page starts implying that typing your name into it produces an account. It
 * does not. A guest gets a ticket to one room, no session, no user row
 * (openapi.yaml → joinConference).
 *
 * The one delivered part it does use is `LanguageSwitcher`, because a stranger
 * has no account to carry a locale and no Settings panel to reach — see the
 * language note below.
 */
export function MeetGuestScreen({ token, media }: MeetGuestScreenProps) {
  const { t } = useTranslation();
  const nameId = useId();
  const hintId = useId();
  const [preview, setPreview] = useState<Preview>({ status: "loading" });
  const [name, setName] = useState("");

  /**
   * WHAT LANGUAGE A STRANGER SEES: English, with the switcher on the page.
   *
   * `i18n/useLanguage.ts` documents the precedence this extends — the account
   * outranks what this browser remembered, because an account is a fact about
   * the *person* and localStorage is a fact about a *browser*. A first-time
   * visitor has neither: no account to ask, and nothing remembered. So they
   * land on `FALLBACK_LANGUAGE`, which is English.
   *
   * `navigator.language` was the obvious third rung and is deliberately not
   * taken. It is a device fact, weaker than the browser fact the existing rule
   * already calls too weak to outrank a person — a borrowed laptop, a phone
   * bought abroad, a locale nobody chose. Worse, honouring it *here only*
   * would make this the one screen in the product whose language is picked by
   * a rule no other screen follows: the sign-in screen a stranger reaches from
   * the dead-link state below would flip back to English under them. If
   * browser negotiation is right it is right for every pre-session screen, and
   * that is a change to `i18n/index.ts` and a decision of its own, not a
   * special case smuggled in with a feature.
   *
   * What makes English defensible rather than merely default is that the way
   * out is on the page and one click wide, and the switcher persists the
   * choice for the reload.
   */

  useEffect(() => {
    // The two disables below are load-bearing: ESLint's narrowing follows the
    // assignments it can see and concludes the flag never changes, because it
    // cannot see the cleanup flipping it while the request is in flight — which
    // is the entire reason the guards exist. Same as `RedeemInviteScreen`.
    let live = true;
    void (async () => {
      try {
        const { data, response } = await api.GET("/api/v1/meet/{token}", {
          params: { path: { token } },
        });
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        if (!live) {
          return;
        }
        setPreview(
          data === undefined
            ? { status: response.status === 404 ? "unusable" : "unreachable" }
            : { status: "ready", title: data.title, active: data.active },
        );
      } catch (requestError) {
        console.warn("Conference preview failed:", requestError);
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        if (live) {
          setPreview({ status: "unreachable" });
        }
      }
    })();
    return () => {
      live = false;
    };
  }, [token]);

  /**
   * The guest's half of the ticket seam: the conference endpoint rather than
   * the channel one, carrying the typed name.
   *
   * It also watches for the 404 the preview cannot have seen — the link can be
   * revoked between reading this page and clicking Join, and revocation ends
   * the room as well as the link (ADR 005). That answer is the dead link, not
   * a call that failed, so it moves the page rather than the call's error.
   */
  const mintTicket = useCallback<MintTicket>(
    async (displayName) => {
      const { data, response } = await api.POST("/api/v1/meet/{token}/join", {
        params: { path: { token } },
        body: { display_name: displayName },
      });
      if (response.status === 404) {
        setPreview({ status: "unusable" });
      }
      return { token: data?.token, status: response.status };
    },
    [token],
  );

  const call = useCallSession(media, mintTicket);

  if (preview.status === "loading") {
    return (
      <main>
        <p role="status">{t("common.loading")}</p>
      </main>
    );
  }

  if (preview.status !== "ready") {
    // One honest sentence and a way out. The way out is the instance's own
    // front door, not "sign in": a stranger has no account here and offering
    // one would answer a question they did not ask.
    return (
      <main>
        <LanguageSwitcher />
        <h1>{t(preview.status === "unusable" ? "meet.unusable.title" : "meet.unreachable.title")}</h1>
        <p>{t(preview.status === "unusable" ? "meet.unusable.body" : "meet.unreachable.body")}</p>
        <a href="/">{t("meet.goHome")}</a>
      </main>
    );
  }

  // Once there is a session, it is simply a call: the same grid, tiles and
  // control bar a member sees (BRIEFS.md §5 `call-grid`), not a second call
  // view that would drift from it. Leaving drops back to the form below, which
  // is also how somebody rejoins a room they stepped out of.
  if (call.status !== "idle" || call.errorKey !== null) {
    return (
      <main>
        <CallView
          channelTitle={isolateAuto(preview.title)}
          status={call.status}
          participants={call.participants}
          micEnabled={call.micEnabled}
          cameraEnabled={call.cameraEnabled}
          screenSharing={call.screenSharing}
          errorKey={call.errorKey}
          onToggleMicrophone={call.toggleMicrophone}
          onToggleCamera={call.toggleCamera}
          onToggleScreenShare={call.toggleScreenShare}
          onLeave={call.leave}
        />
      </main>
    );
  }

  return (
    <main>
      <LanguageSwitcher />
      <h1>{isolateAuto(preview.title)}</h1>
      {/* Arriving before anybody else is a state, not a fault (BRIEFS.md §5). */}
      <p>{t(preview.active ? "meet.active" : "meet.empty")}</p>

      <form
        onSubmit={(event) => {
          event.preventDefault();
          const trimmed = name.trim();
          // `required` blocks an empty field; a field of spaces gets past it,
          // and the server's minLength would not catch that either.
          if (trimmed !== "") {
            call.join(trimmed);
          }
        }}
      >
        <label htmlFor={nameId}>{t("meet.nameLabel")}</label>
        <input
          id={nameId}
          type="text"
          required
          maxLength={NAME_MAX}
          autoComplete="name"
          aria-describedby={hintId}
          value={name}
          onChange={(event) => {
            setName(event.target.value);
          }}
        />
        {/* Says out loud that a name here is a claim. A guest can present as
            anyone and only the people in the room can tell — ADR 005 names
            that weakness rather than hiding it, and so does the screen. */}
        <p id={hintId}>{t("meet.nameHint")}</p>
        <button type="submit">{t("meet.join")}</button>
      </form>

      <p>{t("meet.guestNote")}</p>
    </main>
  );
}
