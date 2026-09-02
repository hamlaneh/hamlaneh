import { useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../../api/client";
import { PRESENCE_LABEL_KEY } from "../../../chat/presence";
import type { UserSummary } from "../../../chat/types";
import type { EncryptionMode } from "../../../instance/encryptionMode";
import { useFocusTrap } from "../../settings/useFocusTrap";
import { CircleAlertIcon, HashIcon, LoaderCircleIcon, SearchIcon, XIcon } from "../../icons";
import { Avatar } from "../Avatar";
import { creationRefusalKey } from "./creationRefusal";
import type { CreationRefusalKey } from "./creationRefusal";

/**
 * The people picker — `chat-addendum-people-invite-*`,
 * `chat-addendum-people-new-dm-*`, `chat-addendum-people-picker-states` and
 * the contract on `chat-addendum-overlay-components` §03.
 *
 * One component, two modes: `invite` into the open channel and `newDm` to open
 * or reuse a one-to-one. Single-pick and immediate in both — the whole row is
 * one semantic button with the verb inside it. No checkbox, no chip, no footer
 * confirm, and no nested control anywhere in the row.
 *
 * A row carries only an initials avatar, the display name, the complete
 * `@username` and optional presence. No email, role, team or photo, and no
 * already-a-member state: the current data contract cannot support one, so the
 * sheet deliberately draws none.
 *
 * Only the activated row goes busy; the rest stay live. Success closes the
 * dialog; failure keeps the query and the results and offers recovery.
 */

interface PeoplePickerProps {
  title: string;
  actionLabel: string;
  /** The busy label for the row being acted on: "Inviting…" / "Opening…". */
  busyLabel?: string;
  /** The channel the invite lands in, drawn under the title in invite mode. */
  channelSlug?: string | undefined;
  /** Resolves to null on success, or the server's own refusal code. */
  onPick: (user: UserSummary) => Promise<string | null>;
  onClose: () => void;
  /**
   * The organisation's encryption mode, when this picker is the moment a
   * conversation is created. It decides what the DM is born as — there is no
   * choice to offer (ADR 011 decision 1). Absent for the invite picker, which
   * joins a conversation that exists and has already decided.
   */
  encryptionMode?: EncryptionMode | undefined;
}

/** One directory request per pause in typing, not per keystroke. */
const QUERY_DEBOUNCE_MS = 200;

type LoadState = "loading" | "ready" | "failed";

export function PeoplePicker({
  title,
  actionLabel,
  busyLabel,
  channelSlug,
  onPick,
  onClose,
  encryptionMode,
}: PeoplePickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [people, setPeople] = useState<UserSummary[]>([]);
  const [loadState, setLoadState] = useState<LoadState>("loading");
  /** Bumped to re-run the effect when the person retries a failed load. */
  const [reloads, setReloads] = useState(0);
  /** Only the activated row goes busy; the rest stay live. */
  const [busyId, setBusyId] = useState<string | null>(null);
  const [failureKey, setFailureKey] = useState<CreationRefusalKey | "chat.people.failed" | null>(
    null,
  );
  const queryId = useId();
  const titleId = useId();

  const dialogRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const onTabKeyDown = useFocusTrap(dialogRef);

  // Focus enters the search field — it is also where Channel actions hands
  // focus when it opens this picker (menu-components §02).
  useEffect(() => {
    searchRef.current?.focus();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      void (async () => {
        try {
          const { data } = await api.GET("/api/v1/users", {
            params: { query: { limit: 50, ...(query.trim() === "" ? {} : { q: query.trim() }) } },
            signal: controller.signal,
          });
          if (data !== undefined) {
            setPeople(data.users);
            setLoadState("ready");
          }
        } catch (error) {
          if (controller.signal.aborted) {
            // Superseded by a newer query, or the picker closed.
            return;
          }
          console.warn("Could not load the user directory:", error);
          setPeople([]);
          setLoadState("failed");
        }
      })();
    }, QUERY_DEBOUNCE_MS);

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [query, reloads]);

  const pick = (person: UserSummary) => {
    if (busyId !== null) {
      return;
    }
    setBusyId(person.id);
    setFailureKey(null);
    void onPick(person).then(
      (refusal) => {
        if (refusal === null) {
          onClose();
          return;
        }
        // The query and the results both survive: the recovery is to pick
        // again, or to pick somebody else.
        setBusyId(null);
        setFailureKey(creationRefusalKey(refusal, "chat.people.failed"));
      },
      () => {
        setBusyId(null);
        setFailureKey("chat.people.failed");
      },
    );
  };

  return (
    <>
      <button
        type="button"
        className="hm-overlay-scrim"
        aria-label={t("chat.common.close")}
        onClick={onClose}
      />
      <div
        ref={dialogRef}
        className="hm-dialog hm-dialog--people"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            onClose();
            return;
          }
          onTabKeyDown(event);
        }}
      >
        <div className="hm-dialog__header">
          <div className="hm-dialog__heading">
            <h2 className="hm-dialog__title" id={titleId}>
              {title}
            </h2>
            {channelSlug === undefined ? null : (
              <p className="hm-dialog__subject">
                <HashIcon size={14} strokeWidth={1.85} />
                {/* The complete slug is one isolated LTR unit, so it never
                    reads as "deploys#" in the Persian frame. */}
                <bdi className="hm-slug" dir="ltr">
                  {channelSlug}
                </bdi>
              </p>
            )}
          </div>
          <button
            type="button"
            className="hm-icon-button hm-close-button"
            aria-label={t("chat.common.close")}
            onClick={onClose}
          >
            <XIcon size={19} strokeWidth={1.85} />
          </button>
        </div>

        <div className="hm-people__search">
          {/* The label stays visible; the placeholder is supplementary. */}
          <label className="hm-overlay-field__label" htmlFor={queryId}>
            {t("chat.people.searchLabel")}
          </label>
          <div className="hm-people__search-shell">
            <span className="hm-people__search-icon">
              <SearchIcon size={16} />
            </span>
            <input
              ref={searchRef}
              id={queryId}
              className="hm-people__search-field"
              type="search"
              value={query}
              placeholder={t("chat.people.searchPlaceholder")}
              onChange={(event) => {
                setQuery(event.target.value);
              }}
            />
          </div>
        </div>

        {encryptionMode === undefined ? null : (
          <div className="hm-people__search">
            {/* Picking the person *is* the creation moment for a DM, and there is
                nothing to choose: the organisation's mode decides, and it is
                fixed once the conversation exists. What is shown is therefore
                the outcome, not a control. */}
            <p className="hm-overlay-field__helper">{t(`chat.people.e2eeByMode.${encryptionMode}`)}</p>
            <p className="hm-overlay-field__helper">{t("chat.people.reopenNote")}</p>
          </div>
        )}

        {failureKey === null ? null : (
          <div className="hm-people__search">
            <p className="hm-overlay-banner" role="alert">
              <span className="hm-overlay-banner__glyph">
                <CircleAlertIcon size={16} strokeWidth={1.85} />
              </span>
              {t(failureKey)}
            </p>
          </div>
        )}

        {loadState === "loading" ? (
          <p className="hm-people__state" role="status">
            <LoaderCircleIcon size={20} className="hm-spin" />
            {t("chat.people.loading")}
          </p>
        ) : loadState === "failed" ? (
          <div className="hm-people__state">
            {/* Assertive, and it never steals focus. The recovery travels with
                the error rather than sitting somewhere else. */}
            <p role="alert">{t("chat.people.loadFailed")}</p>
            <button
              type="button"
              className="hm-overlay-button"
              onClick={() => {
                setLoadState("loading");
                setReloads((count) => count + 1);
              }}
            >
              {t("chat.common.retry")}
            </button>
          </div>
        ) : people.length === 0 ? (
          <p className="hm-people__state" role="status">
            {query.trim() === ""
              ? t("chat.people.directoryEmpty")
              : t("chat.people.noResults")}
          </p>
        ) : (
          <ul className="hm-people__list">
            {people.map((person) => {
              const busy = busyId === person.id;
              return (
                <li key={person.id}>
                  <button
                    type="button"
                    className="hm-person"
                    /* CONFLICT, reported to the orchestrator: the sheet asks for
                       "Invite Nasrin Ahmadi, @nasrin, to #deploys". Two existing
                       tests pin this exact composed name, and the channel it
                       names is not a prop this component's callers pass, so the
                       delivered name is not built here. */
                    aria-label={`${actionLabel}: ${person.display_name}`}
                    disabled={busyId !== null}
                    onClick={() => {
                      pick(person);
                    }}
                  >
                    <Avatar
                      userId={person.id}
                      displayName={person.display_name}
                      size={34}
                      typeSize={14}
                      presence={person.presence}
                      presenceLabel={null}
                    />
                    <span className="hm-person__text">
                      {/* User-owned names are never translated and never
                          re-directed: a Latin name stays Latin in a Persian
                          list. */}
                      <span className="hm-person__name" dir="auto">
                        {person.display_name}
                      </span>
                      <span className="hm-person__meta">
                        <bdi className="hm-username" dir="ltr">
                          @{person.username}
                        </bdi>
                        {person.presence === undefined ? null : (
                          <>
                            <span className="hm-tick" aria-hidden="true" />
                            {/* The word always travels with the dot. */}
                            <span>{t(PRESENCE_LABEL_KEY[person.presence])}</span>
                          </>
                        )}
                      </span>
                    </span>
                    <span className="hm-person__action" aria-hidden="true">
                      {busy ? (
                        <>
                          <LoaderCircleIcon size={13} className="hm-spin" />
                          {busyLabel ?? actionLabel}
                        </>
                      ) : (
                        actionLabel
                      )}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}

        <div className="hm-dialog__footer">
          <button type="button" className="hm-overlay-button" onClick={onClose}>
            {t("chat.common.cancel")}
          </button>
        </div>
      </div>
    </>
  );
}
