import { useCallback, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../api/client";
import type { MediaConnect } from "../calls/media";
import type { MintTicket } from "../calls/useCallSession";
import { useCallSession } from "../calls/useCallSession";
import { LanguageSwitcher } from "../components/LanguageSwitcher";
import { CallView } from "../components/calls/CallView";
import { ChevronRightIcon, CircleAlertIcon, InfoIcon } from "../components/icons";
import { isolateAuto } from "../i18n/bidi";

/** The contract's own bound (openapi.yaml → JoinConferenceRequest.display_name). */
const NAME_MAX = 64;

type Preview =
  | { status: "loading" }
  | { status: "ready"; org: string; title: string; active: boolean }
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
 * The page's own thin top rule: identity at one end, language at the other.
 *
 * WHAT THE IDENTITY END CARRIES. "Sanjab Coop is hosting this meeting" — one
 * line of text, deliberately not a logo lockup, because a wordmark over a
 * centred card *is* the front-door treatment. The name is the organisation's,
 * never the product's: naming Hamlaneh here would claim Hamlaneh is the host,
 * which is both false and the refused wordmark by another route. It comes from
 * `ConferencePreview.org_name`, the same thing `InvitePreview` already names on
 * an equally unauthenticated screen — a visitor following a link out of a chat
 * message otherwise has no way to tell whose meeting they are walking into.
 *
 * A dead link carries no name, because a 404 carries no body: the page knows
 * the link failed and nothing about who issued it, and guessing is exactly what
 * the rest of this screen refuses to do.
 *
 * Once joined the slot carries the meeting title, so a guest with the form
 * gone still knows where they are.
 */
function MeetRule({ org, title }: { org?: string | undefined; title?: string | undefined }) {
  const { t } = useTranslation();

  return (
    <header className="hm-meet__rule">
      {title === undefined ? null : (
        <span className="hm-meet__rule-title" dir="auto">
          {title}
        </span>
      )}
      {org === undefined ? null : (
        <>
          <span className="hm-meet__rule-org" dir="auto">
            {org}
          </span>
          <span className="hm-meet__rule-divider" aria-hidden="true" />
          <span className="hm-meet__rule-host">{t("meet.hostedBy")}</span>
        </>
      )}
      <LanguageSwitcher />
    </header>
  );
}

/**
 * `meet-guest` — the page a conference link opens for somebody with no account
 * on this instance, and the only unauthenticated application surface in the
 * product besides sign-in.
 *
 * Artboards `meet-guest` and `meet-guest-dead`; the reasoning is in
 * docs/design/CALLS_HANDOFF.md and the values are in `meet.css`.
 *
 * WHAT IT REFUSES, AND WHY THE REFUSAL IS THE DESIGN. No `AuthShell`, no
 * `AuthForm`, no `PrimaryButton`, no `.hm-button`, no centred card on a tinted
 * ground, no product wordmark lockup, and no link to sign-in or registration
 * anywhere on the page. Every other pre-session screen is built from those,
 * and wearing them is precisely how a page starts implying that typing your
 * name into it produces an account. It does not. A guest gets a ticket to one
 * room, no session, no user row (openapi.yaml → joinConference). What it wears
 * instead is its own thin rule, a flush single column on the plain canvas, one
 * field, one action, and a note saying in words that no account is created.
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
            : { status: "ready", org: data.org_name, title: data.title, active: data.active },
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
      <div className="hm-meet">
        <MeetRule />
        <main className="hm-meet__body">
          <div className="hm-meet__column">
            <p className="hm-meet__hint" role="status">
              {t("common.loading")}
            </p>
          </div>
        </main>
      </div>
    );
  }

  if (preview.status !== "ready") {
    // One honest sentence and a way out. The way out is the instance's own
    // front door, not "sign in": a stranger has no account here and offering
    // one would answer a question they did not ask.
    const unusable = preview.status === "unusable";
    return (
      <div className="hm-meet">
        <MeetRule />
        <main className="hm-meet__body">
          <div className="hm-meet__column hm-meet__column--dead">
            <span className="hm-meet__glyph" aria-hidden="true">
              <CircleAlertIcon size={22} strokeWidth={1.85} />
            </span>
            <div className="hm-meet__heading">
              <h1 className="hm-meet__title hm-meet__title--dead">
                {t(unusable ? "meet.unusable.title" : "meet.unreachable.title")}
              </h1>
              <p className="hm-meet__body-text">
                {t(unusable ? "meet.unusable.body" : "meet.unreachable.body")}
              </p>
            </div>
            <div className="hm-meet__divider" />
            <a className="hm-meet__home" href="/">
              {t("meet.goHome")}
              <ChevronRightIcon size={16} strokeWidth={1.85} className="hm-mirror-glyph" />
            </a>
          </div>
        </main>
      </div>
    );
  }

  // Once there is a session, it is simply a call: the same grid, tiles and
  // control bar a member sees (BRIEFS.md §5 `call-grid`), not a second call
  // view that would drift from it. Leaving drops back to the form below, which
  // is also how somebody rejoins a room they stepped out of.
  if (call.status !== "idle" || call.errorKey !== null) {
    return (
      <div className="hm-meet">
        <MeetRule title={preview.title} />
        <main className="hm-meet__call">
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
      </div>
    );
  }

  return (
    <div className="hm-meet">
      <MeetRule org={preview.org} />
      <main className="hm-meet__body">
        <div className="hm-meet__column">
          <div className="hm-meet__heading">
            <span className="hm-meet__eyebrow">{t("meet.eyebrow")}</span>
            <h1 className="hm-meet__title">{isolateAuto(preview.title)}</h1>
            {/* Arriving before anybody else is a state, not a fault
                (BRIEFS.md §5). Shape and words, never colour alone — and
                nothing animates: the preview was read once, on load. */}
            <p className="hm-meet__presence" data-active={preview.active}>
              <span className="hm-meet__presence-dot" aria-hidden="true" />
              {t(preview.active ? "meet.active" : "meet.empty")}
            </p>
          </div>

          <div className="hm-meet__divider" />

          <form
            className="hm-meet__form"
            onSubmit={(event) => {
              event.preventDefault();
              const trimmed = name.trim();
              // `required` blocks an empty field; a field of spaces gets past
              // it, and the server's minLength would not catch that either.
              if (trimmed !== "") {
                call.join(trimmed);
              }
            }}
          >
            <label className="hm-meet__label" htmlFor={nameId}>
              {t("meet.nameLabel")}
            </label>
            {/* Stacked below 1280 and one row above it: a 64-character name
                and a 44px target cannot share a 375 row. */}
            <div className="hm-meet__row">
              <div className="hm-meet__field">
                <input
                  className="hm-input"
                  id={nameId}
                  type="text"
                  dir="auto"
                  required
                  maxLength={NAME_MAX}
                  // Never `username` or `email`: a password manager offering
                  // to save credentials here would say the opposite of the
                  // copy below, and no account is created.
                  autoComplete="name"
                  aria-describedby={hintId}
                  value={name}
                  onChange={(event) => {
                    setName(event.target.value);
                  }}
                />
              </div>
              <button className="hm-meet__join" type="submit">
                {t("meet.join")}
                <ChevronRightIcon size={17} strokeWidth={1.85} className="hm-mirror-glyph" />
              </button>
            </div>
            {/* Says out loud that a name here is a claim. A guest can present as
                anyone and only the people in the room can tell — ADR 005 names
                that weakness rather than hiding it, and so does the screen. */}
            <p className="hm-meet__hint" id={hintId}>
              {t("meet.nameHint")}
            </p>
          </form>

          <p className="hm-meet__note">
            <InfoIcon size={17} strokeWidth={1.85} className="hm-meet__note-icon" />
            <span>{t("meet.guestNote")}</span>
          </p>

          {/* UNDESIGNED — `conference-plain-label` is PENDING in
              docs/design/STATUS.md, so this is plain semantic HTML.

              It is not optional and it is not a caveat to be tucked away: a
              conference admits guests, a guest holds no credential and
              therefore no MLS leaf, so there is nothing to derive a key with
              (ADR 006, decision 3). The screen says the server can reach the
              audio and video, and the unqualified word "encrypted" never
              appears here — this meeting really does run over TLS and SRTP,
              and saying "encrypted" without saying which kind is exactly the
              overclaim PLAN §2.4 forbids. The second sentence exists so a
              working, deliberate feature does not read as a broken one. */}
          <div className="hm-plumbing">
            <p>{t("meet.mediaLabel")}</p>
            <p>{t("meet.mediaWhy")}</p>
          </div>
        </div>
      </main>
    </div>
  );
}
