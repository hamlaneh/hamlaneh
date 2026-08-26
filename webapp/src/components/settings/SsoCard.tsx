import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { providerRedirect } from "../../auth/sso";
import { isolateAuto } from "../../i18n/bidi";
import { useInstance } from "../../instance/instanceInfo";

/**
 * Full key paths rather than one namespace, so the two sentences that already
 * exist on the sign-in screen are borrowed instead of copied. The *key* is held
 * rather than the sentence so the message follows a language switch.
 */
type SsoErrorKey =
  | "settings.sso.error.noPassword"
  | "settings.sso.error.alreadyLinked"
  | "settings.sso.error.notLinked"
  | "settings.sso.error.unavailable"
  | "login.error.rateLimited"
  | "login.error.unexpected";

/**
 * Connect or disconnect single sign-on — `User.sso_linked`, which the contract
 * describes as "what the settings screen offers Connect or Disconnect from".
 *
 * UNDESIGNED SURFACE — no artboard draws account linking, so this is plain
 * semantic HTML with no styling beyond structure, and none of the delivered
 * card treatments (docs/design/STATUS.md, "Single sign-on linking").
 *
 * The flag is read from `/users/me` here rather than taken from the signed-in
 * user the shell already holds: connecting leaves the page entirely and comes
 * back through the callback, and disconnecting changes the flag under a copy
 * the shell fetched at sign-in. Reading it when the card mounts is the same
 * shape `useTotpStatus` uses, and it costs one request when Security opens.
 */
export function SsoCard() {
  const { t } = useTranslation();
  const { info, loaded } = useInstance();
  /** null while the flag has not been read; nothing is drawn on a guess. */
  const [linked, setLinked] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [errorKey, setErrorKey] = useState<SsoErrorKey | null>(null);

  useEffect(() => {
    let live = true;
    void api
      .GET("/api/v1/users/me")
      .then(({ data }) => {
        if (live && data !== undefined) {
          setLinked(data.sso_linked);
        }
      })
      .catch((requestError: unknown) => {
        // Unreadable: the card stays absent rather than offering an action
        // against a state nobody knows.
        console.warn("Single sign-on status lookup failed:", requestError);
      });
    return () => {
      live = false;
    };
  }, []);

  const connect = async () => {
    setBusy(true);
    setErrorKey(null);
    try {
      const { data, error: apiError, response } = await api.POST("/api/v1/users/me/oidc");
      if (response.status === 200 && data !== undefined) {
        // The provider hand-off is a navigation, like the sign-in button — the
        // difference is only that this URL has to be asked for first, because
        // the transaction cookie it pairs with carries the intent to link.
        providerRedirect.follow(data.redirect_url);
        return;
      }
      setErrorKey(
        response.status === 409 && apiError?.error.code === "sso_already_linked"
          ? "settings.sso.error.alreadyLinked"
          : response.status === 503
            ? "settings.sso.error.unavailable"
            : response.status === 429
              ? "login.error.rateLimited"
              : "login.error.unexpected",
      );
    } catch (requestError) {
      console.warn("Connecting single sign-on failed:", requestError);
      setErrorKey("login.error.unexpected");
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    setBusy(true);
    setErrorKey(null);
    try {
      const { error: apiError, response } = await api.DELETE("/api/v1/users/me/oidc");
      if (response.status === 204) {
        setLinked(false);
        return;
      }
      setErrorKey(
        // The refusal that has a real reason behind it: the account has no
        // password, so the link is its only way in and removing it would lock
        // the person out. Only an administrator can change that.
        response.status === 409 && apiError?.error.code === "sso_unlink_no_password"
          ? "settings.sso.error.noPassword"
          : response.status === 404
            ? "settings.sso.error.notLinked"
            : response.status === 429
              ? "login.error.rateLimited"
              : "login.error.unexpected",
      );
    } catch (requestError) {
      console.warn("Disconnecting single sign-on failed:", requestError);
      setErrorKey("login.error.unexpected");
    } finally {
      setBusy(false);
    }
  };

  const enabled = loaded && info.sso?.enabled === true;
  if (linked === null || (!enabled && !linked)) {
    // Nothing configured and nothing linked: there is no door here to describe.
    // Still linked on an instance that has since turned single sign-on off is
    // the one case worth drawing anyway — that link is real and removable.
    return null;
  }

  // Isolated because the name is data and may run either direction inside the
  // surrounding sentence. The `??` is for the type rather than for the server:
  // the contract has provider_name present whenever enabled is true, but
  // OpenAPI cannot say "required when" without a oneOf worse than the prose,
  // so the generated type stays optional and the branch has to exist.
  const provider = isolateAuto(info.sso?.provider_name ?? t("sso.provider"));

  return (
    <section>
      <h4>{t("settings.sso.cardTitle")}</h4>
      <p>{t(linked ? "settings.sso.linked" : "settings.sso.notLinked", { provider })}</p>
      {errorKey === null ? null : <p role="alert">{t(errorKey)}</p>}
      {linked ? (
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            void disconnect();
          }}
        >
          {busy ? t("settings.sso.disconnecting") : t("settings.sso.disconnect", { provider })}
        </button>
      ) : (
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            void connect();
          }}
        >
          {busy ? t("settings.sso.connecting") : t("settings.sso.connect", { provider })}
        </button>
      )}
    </section>
  );
}
