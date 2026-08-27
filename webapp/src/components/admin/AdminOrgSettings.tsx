import { useCallback, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { getOrgSettings, updateOrgSettings } from "../../admin/adminApi";
import type { OrgSettings, UpdateOrgSettingsRequest } from "../../admin/adminApi";
import { useAdminResource } from "../../admin/useAdminResource";
import { formatCount } from "../../chat/format";
import type { User } from "../../chat/types";
import { useInstance } from "../../instance/instanceInfo";
import { AdminLoadFailed } from "./AdminStates";
import { AdminShell } from "./AdminShell";
import { NoticeBanner } from "../auth/NoticeBanner";
import { RefreshCwIcon, TriangleAlertIcon } from "../icons";
import { SavedMark } from "../settings/SavedMark";
import { SettingsButton } from "../settings/SettingsButton";

/** The lifetimes offered, inside the contract's 1..8760 hours. */
const LIFETIMES = [24, 168, 720, 2160] as const;

/** Written out for the reason EXPIRY_LABELS in AdminInvites is. */
const LIFETIME_LABELS = {
  24: "admin.org.lifetime.24",
  168: "admin.org.lifetime.168",
  720: "admin.org.lifetime.720",
  2160: "admin.org.lifetime.2160",
} as const;

interface AdminOrgSettingsProps {
  currentUser: User;
  organizationName: string;
  /** Lets the shell's org name follow a rename without a reload. */
  onOrganizationRenamed?: (name: string) => void;
}

/**
 * "Org settings" — `admin-org-settings`. Every field saves immediately, on
 * its own, and the subtitle says so: there is no Save button to forget.
 */
export function AdminOrgSettings({
  currentUser,
  organizationName,
  onOrganizationRenamed,
}: AdminOrgSettingsProps) {
  const { t, i18n } = useTranslation();
  const { info, loaded } = useInstance();
  const settings = useAdminResource(useCallback(() => getOrgSettings(), []));
  const fieldId = useId();

  const [name, setName] = useState("");
  // What org_name was when the draft above was seeded. React's documented
  // way to reset a draft when its source changes: compare during render and
  // setState right there, which React re-runs immediately without painting
  // the stale value — rather than an effect, which sets state on every
  // settings object handed back and schedules a second render to show what
  // the first already had.
  const [seededFrom, setSeededFrom] = useState<string | null>(null);
  const [savedField, setSavedField] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const current = settings.state.status === "ready" ? settings.state.data : null;

  // The typed field is a draft over the loaded value; every other control
  // commits on change and needs no draft at all.
  //
  // Seeded during render rather than in an effect: an effect would set state
  // synchronously on every load and schedule a second render to show a value
  // the first one already had. Keyed on the loaded org_name, so a reload that
  // brings a different name reseeds the draft and one that brings the same
  // name leaves whatever the admin is halfway through typing alone.
  if (current !== null && seededFrom !== current.org_name) {
    setSeededFrom(current.org_name);
    setName(current.org_name);
  }

  const save = (field: string, patch: UpdateOrgSettingsRequest) => {
    setSaving(field);
    setFailure(null);
    void (async () => {
      try {
        const next: OrgSettings = await updateOrgSettings(patch);
        settings.update(next);
        setSavedField(field);
        if (patch.org_name !== undefined) {
          onOrganizationRenamed?.(next.org_name);
        }
      } catch (requestError) {
        console.warn("Saving the org setting failed:", requestError);
        setFailure(t("admin.org.saveFailed"));
        // Put the draft back to what the server last told us, so the screen
        // never shows a value the instance does not hold.
        if (current !== null) {
          setName(current.org_name);
        }
      } finally {
        setSaving(null);
      }
    })();
  };

  const mark = (field: string) =>
    savedField === field && saving === null ? <SavedMark /> : null;

  return (
    <AdminShell
      currentUser={currentUser}
      organizationName={organizationName}
      title={t("admin.org.title")}
      subtitle={t("admin.org.subtitle")}
    >
      {failure === null ? null : <NoticeBanner tone="danger" message={failure} />}

      {settings.state.status === "error" ? (
        <AdminLoadFailed
          title={t("admin.org.loadFailed")}
          body={t("admin.error.loadBody")}
          retry={
            <SettingsButton
              tone="primary"
              label={t("admin.error.retry")}
              icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
              onClick={settings.reload}
            />
          }
        />
      ) : current === null ? (
        <p className="hm-admin-hint" role="status">
          {t("common.loading")}
        </p>
      ) : (
        <div className="hm-admin-columns">
          <div className="hm-admin-column">
            <section className="hm-admin-panel">
              <h2 className="hm-admin-panel__title">{t("admin.org.organization")}</h2>

              <div className="hm-admin-field">
                <label className="hm-admin-field__label" htmlFor={`${fieldId}-name`}>
                  {t("admin.org.nameLabel")}
                </label>
                <input
                  className="hm-input"
                  id={`${fieldId}-name`}
                  type="text"
                  dir="auto"
                  maxLength={64}
                  value={name}
                  disabled={saving === "name"}
                  onChange={(event) => {
                    setName(event.target.value);
                  }}
                  onBlur={() => {
                    const trimmed = name.trim();
                    if (trimmed !== "" && trimmed !== current.org_name) {
                      save("name", { org_name: trimmed });
                    }
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.currentTarget.blur();
                    }
                  }}
                />
                <span className="hm-admin-hint">{t("admin.org.nameHint")}</span>
                {mark("name")}
              </div>

              <div className="hm-admin-field">
                <label className="hm-admin-field__label" htmlFor={`${fieldId}-locale`}>
                  {t("admin.org.localeLabel")}
                </label>
                <select
                  className="hm-admin-select"
                  id={`${fieldId}-locale`}
                  value={current.default_locale}
                  disabled={saving === "locale"}
                  onChange={(event) => {
                    save("locale", { default_locale: event.target.value === "fa" ? "fa" : "en" });
                  }}
                >
                  <option value="en">{t("settings.language.name.en")}</option>
                  <option value="fa">{t("settings.language.name.fa")}</option>
                </select>
                <span className="hm-admin-hint">{t("admin.org.localeHint")}</span>
                {mark("locale")}
              </div>
            </section>
          </div>

          <div className="hm-admin-column">
            <section className="hm-admin-panel">
              <fieldset className="hm-admin-fieldset" disabled={saving === "registration"}>
                <legend>{t("admin.org.registration")}</legend>
                {(["invite", "open"] as const).map((mode) => (
                  <label className="hm-admin-choice" key={mode}>
                    <input
                      type="radio"
                      name={`${fieldId}-registration`}
                      value={mode}
                      checked={current.registration_mode === mode}
                      onChange={() => {
                        save("registration", { registration_mode: mode });
                      }}
                    />
                    <span className="hm-admin-choice__text">
                      <span className="hm-admin-choice__label">
                        {t(`admin.org.registrationMode.${mode}`)}
                      </span>
                      <span className="hm-admin-choice__hint">
                        {t(`admin.org.registrationHint.${mode}`)}
                      </span>
                    </span>
                  </label>
                ))}
              </fieldset>
              {/* The consequence of Open, stated where the choice is made. */}
              <div className="hm-admin-note hm-admin-note--warning">
                <TriangleAlertIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
                <span>{t("admin.org.openWarning")}</span>
              </div>
              {mark("registration")}

              {/* Here rather than in Security beside session lifetime: this
                  answers the question the radio group above answers — how an
                  account comes into existence on this instance. It sits
                  outside that fieldset so a registration save does not disable
                  it, and it is a switch rather than a third radio because the
                  two settings govern two independent doors.

                  ALWAYS SHOWN, and never disabled, on an instance with no
                  provider configured. The sign-in screen hides its single
                  sign-on link when `sso.enabled` is false, and SsoCard hides
                  itself, because a door with nowhere to go is dishonest to the
                  person about to walk through it. This is the opposite kind of
                  control: stored organisation policy, not a door. Its value
                  outlives any provider configuration — an instance whose
                  provider was removed still holds whatever was last set, and
                  it governs the moment a provider comes back — so hiding it
                  would make a live rule invisible and let an administrator
                  configure a provider tomorrow under a setting they were never
                  shown. Disabling it would invent a coupling the contract does
                  not have: PATCH accepts the field with no provider
                  configured, and pre-setting policy before wiring the provider
                  is a reasonable order to work in. So it is shown, writable,
                  and says plainly when there is nothing yet for it to govern. */}
              <div className="hm-admin-switchrow">
                <span className="hm-admin-choice__text" id={`${fieldId}-jit-label`}>
                  <span className="hm-admin-choice__label">{t("admin.org.ssoJitLabel")}</span>
                  {/* Every toggle carries a consequence line, never a bare switch. */}
                  <span className="hm-admin-choice__hint">{t("admin.org.ssoJitHint")}</span>
                  <span className="hm-admin-choice__hint">{t("admin.org.ssoJitNote")}</span>
                  {/* `loaded` so the line does not appear and then vanish once
                      the instance document arrives. */}
                  {!loaded || info.sso?.enabled === true ? null : (
                    <span className="hm-admin-choice__hint">
                      {t("admin.org.ssoJitUnconfigured")}
                    </span>
                  )}
                </span>
                <button
                  type="button"
                  className="hm-admin-toggle"
                  role="switch"
                  // Optional in the contract, and off is its documented
                  // default, so an absent field reads as off.
                  aria-checked={current.sso_jit_provisioning}
                  aria-labelledby={`${fieldId}-jit-label`}
                  disabled={saving === "jit"}
                  onClick={() => {
                    save("jit", {
                      sso_jit_provisioning: !current.sso_jit_provisioning,
                    });
                  }}
                >
                  <span className="hm-admin-toggle__knob" aria-hidden="true" />
                </button>
              </div>
              {mark("jit")}
            </section>

            <section className="hm-admin-panel">
              <h2 className="hm-admin-panel__title">{t("admin.org.security")}</h2>

              <div className="hm-admin-switchrow">
                <span className="hm-admin-choice__text" id={`${fieldId}-totp-label`}>
                  <span className="hm-admin-choice__label">{t("admin.org.requireTotpLabel")}</span>
                  {/* Every toggle carries a consequence line, never a bare switch. */}
                  <span className="hm-admin-choice__hint">{t("admin.org.requireTotpHint")}</span>
                  {current.accounts_without_totp === undefined ||
                  current.accounts_without_totp === 0 ? null : (
                    <span className="hm-admin-choice__hint">
                      {t("admin.org.accountsWithoutTotp", {
                        accounts: formatCount(current.accounts_without_totp, i18n.language),
                      })}
                    </span>
                  )}
                </span>
                <button
                  type="button"
                  className="hm-admin-toggle"
                  role="switch"
                  aria-checked={current.require_totp}
                  aria-labelledby={`${fieldId}-totp-label`}
                  disabled={saving === "totp"}
                  onClick={() => {
                    save("totp", { require_totp: !current.require_totp });
                  }}
                >
                  <span className="hm-admin-toggle__knob" aria-hidden="true" />
                </button>
              </div>
              {mark("totp")}

              <div className="hm-admin-modal__row">
                <div className="hm-admin-field">
                  <label className="hm-admin-field__label" htmlFor={`${fieldId}-lifetime`}>
                    {t("admin.org.sessionLifetimeLabel")}
                  </label>
                  <select
                    className="hm-admin-select"
                    id={`${fieldId}-lifetime`}
                    value={current.session_lifetime_hours}
                    disabled={saving === "lifetime"}
                    onChange={(event) => {
                      save("lifetime", { session_lifetime_hours: Number(event.target.value) });
                    }}
                  >
                    {(LIFETIMES as readonly number[]).includes(current.session_lifetime_hours)
                      ? null
                      : (
                          // Whatever the instance holds stays selectable, even
                          // when it is not one of the offered steps.
                          <option value={current.session_lifetime_hours}>
                            {t("admin.org.lifetime.custom", {
                              hours: formatCount(current.session_lifetime_hours, i18n.language),
                            })}
                          </option>
                        )}
                    {LIFETIMES.map((hours) => (
                      <option key={hours} value={hours}>
                        {t(LIFETIME_LABELS[hours])}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="hm-admin-field">
                  <span className="hm-admin-field__label" id={`${fieldId}-minlen`}>
                    {t("admin.org.passwordMinLabel")}
                  </span>
                  {/* Instance policy, served with the form (GET /instance).
                      The contract has no field for it on OrgSettings, so it
                      is reported here and changed nowhere in this screen. */}
                  <output className="hm-admin-readonly" dir="ltr" aria-labelledby={`${fieldId}-minlen`}>
                    {info.password_min_length}
                  </output>
                </div>
              </div>
              {mark("lifetime")}
              <span className="hm-admin-hint">{t("admin.org.passwordMinNote")}</span>
            </section>
          </div>
        </div>
      )}
    </AdminShell>
  );
}
