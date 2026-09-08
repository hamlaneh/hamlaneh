import { useCallback, useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { AdminError, getUpdateStatus, requestUpdate } from "../../admin/adminApi";
import type { UpdateStatus } from "../../admin/adminApi";
import { describeUpdate } from "../../admin/updateStatus";
import { useAdminResource } from "../../admin/useAdminResource";
import { useRateLimitNotice } from "../../auth/rateLimit";
import type { RateLimitKeys } from "../../auth/rateLimit";
import { formatActivationDate } from "../../settings/sessionTime";
import {
  DownloadIcon,
  InfoIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  TriangleAlertIcon,
} from "../icons";
import { SettingsButton } from "../settings/SettingsButton";

/**
 * "Updates" — what version this instance runs, whether a newer one is waiting,
 * and the two requests an operator can make of the host (ADR 016).
 *
 * It asks; it never applies. The POST carries a `kind` and nothing else, so the
 * widest outcome reachable from this panel is the release the host's timer
 * would have applied within six hours anyway — which is what keeps a stolen
 * admin session from being a downgrade to a signed old release.
 *
 * UNDESIGNED SURFACE — no artboard draws an update control, so nothing here is
 * a new treatment: it is the delivered `hm-admin-panel`, the `hm-admin-field` /
 * `hm-admin-readonly` pair the same screen already uses to report an instance
 * fact it does not edit, the `hm-admin-note` banner, `hm-admin-hint` lines,
 * `hm-admin-state__actions` and `SettingsButton`. One rule is added to
 * `admin.css` and it is containment rather than appearance — see
 * `.hm-admin-mono--block`. Unlike `EncryptionModeSection` next to it, this
 * panel does wear the panel treatment: it reports settings-shaped facts and
 * carries no ceremony, so borrowing the panel states nothing that is not true.
 *
 * Its own fetch, deliberately: a host with no update state must not be able to
 * take the organization settings down with it.
 */

/** How often to re-read the status while a run is in flight. */
const POLL_INTERVAL_MS = 3000;

/**
 * This panel's rate-limit sentence family. The undated wording is its own key
 * and the counted variants are borrowed from the reset-request screen, whose
 * sentence this is word for word: asking the host to check is a request, not
 * an attempt, and the two counted forms are that same sentence with the vague
 * tail replaced by the number the server gave.
 */
const RATE_LIMIT_KEYS: RateLimitKeys = {
  undated: "admin.org.updates.error.rateLimited",
  seconds: "resetRequest.error.rateLimitedSeconds",
  minutes: "resetRequest.error.rateLimitedMinutes",
};

/** Which refusal is on screen; `rateLimited` reads its sentence off the clock. */
type RequestFailure = "none" | "inProgress" | "unavailable" | "rateLimited" | "unexpected";

export function UpdatesSection() {
  const { t, i18n } = useTranslation();
  const status = useAdminResource(useCallback(() => getUpdateStatus(), []));
  const fieldId = useId();
  const { state, update, reload } = status;

  const [busy, setBusy] = useState<"check" | "apply" | null>(null);
  const [failure, setFailure] = useState<RequestFailure>("none");
  const {
    message: rateLimitMessage,
    start: startRateLimitWait,
    clear: clearRateLimitWait,
  } = useRateLimitNotice(RATE_LIMIT_KEYS, () => {
    // The stated wait has passed, so the notice goes with it — but only if it
    // is still the notice on screen; a later refusal of its own must stand.
    setFailure((current) => (current === "rateLimited" ? "none" : current));
  });

  const current: UpdateStatus | null = state.status === "ready" ? state.data : null;
  const view = current === null ? null : describeUpdate(current);
  // A boolean rather than the state itself, so the poll below is not torn down
  // and restarted when the host moves from `requested` to `running`.
  const running = view !== null && view.inFlight !== null;

  // Empty for an absent or unparseable timestamp, which is what keeps a
  // malformed one from rendering as "Last checked ." Both are contract-optional.
  const checkedAt = formatActivationDate(current?.last_check_at ?? "", i18n.language);
  const ranAt = formatActivationDate(current?.last_run_at ?? "", i18n.language);

  /**
   * While a run is in flight the panel re-reads the status instead of waiting
   * for the operator to reload. `update` rather than `reload`: reload paints
   * the loading line again every three seconds, which reads as the panel
   * breaking rather than as an update progressing.
   *
   * A failed poll is logged and the interval kept, because the expected way for
   * one to fail is the server restarting under an update it was asked for. The
   * next poll after it comes back carries the outcome.
   */
  useEffect(() => {
    if (!running) {
      return undefined;
    }
    let live = true;
    const timer = setInterval(() => {
      void getUpdateStatus().then(
        (next) => {
          if (live) {
            update(next);
          }
        },
        (pollError: unknown) => {
          console.warn("Re-reading the update status failed:", pollError);
        },
      );
    }, POLL_INTERVAL_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [running, update]);

  const ask = (kind: "check" | "apply") => {
    setBusy(kind);
    setFailure("none");
    clearRateLimitWait();
    void (async () => {
      try {
        update(await requestUpdate(kind));
      } catch (requestError) {
        console.warn("Asking the host for an update failed:", requestError);
        if (!(requestError instanceof AdminError)) {
          setFailure("unexpected");
          return;
        }
        if (requestError.status === 429) {
          startRateLimitWait(requestError.response);
          setFailure("rateLimited");
          return;
        }
        setFailure(
          requestError.status === 409
            ? "inProgress"
            : requestError.status === 503
              ? "unavailable"
              : "unexpected",
        );
        // Both of those are the host's own picture disagreeing with this
        // screen's — a run started elsewhere, or a watcher that has gone. The
        // status is re-read so the panel redraws as what the host now reports
        // rather than as an error line over a stale picture.
        if (requestError.status === 409 || requestError.status === 503) {
          reload();
        }
      } finally {
        setBusy(null);
      }
    })();
  };

  return (
    <section className="hm-admin-panel">
      <h2 className="hm-admin-panel__title">{t("admin.org.updates.title")}</h2>

      {state.status === "error" ? (
        <>
          <div className="hm-admin-note hm-admin-note--warning">
            <TriangleAlertIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
            <span>{t("admin.org.updates.loadFailed")}</span>
          </div>
          <div className="hm-admin-state__actions">
            <SettingsButton
              tone="primary"
              size="sm"
              label={t("admin.error.retry")}
              icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
              onClick={reload}
            />
          </div>
        </>
      ) : current === null || view === null ? (
        <p className="hm-admin-hint" role="status">
          {t("common.loading")}
        </p>
      ) : (
        <>
          <div className="hm-admin-field">
            <span className="hm-admin-field__label" id={`${fieldId}-installed`}>
              {t("admin.org.updates.installedLabel")}
            </span>
            {/* Direction-pinned and mono, as every generated value on this
                screen is: a version string is read character by character and
                must not reorder in a Persian interface. */}
            <output
              className="hm-admin-readonly"
              dir="ltr"
              aria-labelledby={`${fieldId}-installed`}
            >
              {current.installed_version}
            </output>
          </div>

          {/* The frame for every sentence below it: which releases this host
              takes at all is what makes "outside the channel" mean anything. */}
          <span className="hm-admin-hint">
            {t(`admin.org.updates.channel.${current.channel}`)}
          </span>

          {view.capability === "selfUpdating" ? null : (
            <div className="hm-admin-note">
              <InfoIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
              <span>{t(`admin.org.updates.capability.${view.capability}`)}</span>
            </div>
          )}

          {view.availability === null ? null : (
            <p className="hm-admin-hint">
              {t(`admin.org.updates.availability.${view.availability}`, {
                version: current.available_version ?? "",
              })}
            </p>
          )}
          {/* Each date qualifies exactly the sentence above it, and appears
              only when that sentence does: "Last checked …" under no claim
              about availability is a fact attached to nothing. */}
          {view.availability === null || checkedAt === "" ? null : (
            <span className="hm-admin-hint">
              {t("admin.org.updates.checkedAt", { when: checkedAt })}
            </span>
          )}

          {view.inFlight !== null ? (
            <div className="hm-admin-note" role="status">
              <LoaderCircleIcon
                size={16}
                strokeWidth={1.85}
                className="hm-admin-note__icon hm-spinner"
              />
              <span>{t(`admin.org.updates.progress.${view.inFlight}`)}</span>
            </div>
          ) : view.outcome === null ? null : (
            <div
              className={`hm-admin-note${view.outcome === "succeeded" ? "" : " hm-admin-note--warning"}`}
            >
              {view.outcome === "succeeded" ? (
                <InfoIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
              ) : (
                <TriangleAlertIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
              )}
              <span>{t(`admin.org.updates.outcome.${view.outcome}`)}</span>
            </div>
          )}
          {view.outcome === null || ranAt === "" ? null : (
            <span className="hm-admin-hint">
              {t("admin.org.updates.ranAt", { when: ranAt })}
            </span>
          )}

          {current.message === null ||
          current.message === undefined ||
          current.message === "" ? null : (
            // `group` rather than `aria-labelledby` on the <pre> itself: a
            // <pre> maps to `generic`, which prohibits an accessible name, so
            // the attribute would be inert. The wrapper takes the name and the
            // block reads inside it.
            <div className="hm-admin-field" role="group" aria-labelledby={`${fieldId}-message`}>
              <span className="hm-admin-field__label" id={`${fieldId}-message`}>
                {t("admin.org.updates.messageLabel")}
              </span>
              {/* The updater's own last line, verbatim, as text and never as
                  markup: it is a host-generated string on the most
                  authenticated surface in the product, and React escaping it is
                  the whole defense. Direction-pinned for the same reason the
                  version above is. */}
              <pre className="hm-admin-mono hm-admin-mono--block" dir="ltr">
                {current.message}
              </pre>
            </div>
          )}

          {failure === "none" ? null : (
            <div className="hm-admin-note hm-admin-note--warning" role="alert">
              <TriangleAlertIcon size={16} strokeWidth={1.85} className="hm-admin-note__icon" />
              <span>
                {failure === "rateLimited"
                  ? rateLimitMessage
                  : t(`admin.org.updates.error.${failure}`)}
              </span>
            </div>
          )}

          {/* No button at all where the instance cannot honour one. An offer it
              cannot keep would leave an operator believing they are patched
              (ADR 016 decision 3), which is worse than no offer. */}
          {view.capability !== "selfUpdating" ? null : (
            <div className="hm-admin-state__actions">
              <SettingsButton
                label={t("admin.org.updates.check")}
                size="sm"
                icon={<RefreshCwIcon size={16} strokeWidth={1.85} />}
                disabled={running || busy !== null}
                busy={busy === "check"}
                busyLabel={t("admin.org.updates.checking")}
                onClick={() => {
                  ask("check");
                }}
              />
              {view.availability !== "available" ? null : (
                <SettingsButton
                  tone="primary"
                  label={t("admin.org.updates.apply")}
                  size="sm"
                  icon={<DownloadIcon size={16} strokeWidth={1.85} />}
                  disabled={running || busy !== null}
                  busy={busy === "apply"}
                  busyLabel={t("admin.org.updates.applying")}
                  onClick={() => {
                    ask("apply");
                  }}
                />
              )}
            </div>
          )}
        </>
      )}
    </section>
  );
}
