import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../../api/client";
import type { UserSummary } from "../../../chat/types";
import type { EncryptionMode } from "../../../instance/encryptionMode";
import { creationRefusalKey } from "./creationRefusal";
import type { CreationRefusalKey } from "./creationRefusal";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup draws neither an invite picker nor a DM picker, so both are
 * functional plumbing over the same directory endpoint until a design lands.
 */

interface PeoplePickerProps {
  title: string;
  actionLabel: string;
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

export function PeoplePicker({
  title,
  actionLabel,
  onPick,
  onClose,
  encryptionMode,
}: PeoplePickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [people, setPeople] = useState<UserSummary[]>([]);
  const [failureKey, setFailureKey] = useState<
    CreationRefusalKey | "chat.people.failed" | null
  >(null);
  const queryId = useId();

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
          }
        } catch (error) {
          if (controller.signal.aborted) {
            // Superseded by a newer query, or the picker closed.
            return;
          }
          console.warn("Could not load the user directory:", error);
          setPeople([]);
        }
      })();
    }, QUERY_DEBOUNCE_MS);

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [query]);

  return (
    <section
      className="hm-plumbing hm-plumbing--overlay"
      role="dialog"
      aria-modal="true"
      aria-label={title}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
    >
      <h2>{title}</h2>
      <p>
        <label htmlFor={queryId}>{t("chat.people.searchLabel")}</label>
        <input
          id={queryId}
          type="search"
          value={query}
          autoFocus
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />
      </p>
      {encryptionMode === undefined ? null : (
        <>
          {/* Picking the person *is* the creation moment for a DM, and there is
              nothing to choose: the organisation's mode decides, and it is
              fixed once the conversation exists. What is shown is therefore
              the outcome, not a control. */}
          <p>{t(`chat.people.e2eeByMode.${encryptionMode}`)}</p>
          <p>{t("chat.people.reopenNote")}</p>
        </>
      )}
      {failureKey === null ? null : <p role="alert">{t(failureKey)}</p>}
      <ul>
        {people.map((person) => (
          <li key={person.id}>
            <button
              type="button"
              onClick={() => {
                void onPick(person).then((refusal) => {
                  if (refusal === null) {
                    onClose();
                  } else {
                    setFailureKey(creationRefusalKey(refusal, "chat.people.failed"));
                  }
                });
              }}
            >
              {`${actionLabel}: ${person.display_name}`}
            </button>
          </li>
        ))}
      </ul>
      <p>
        <button type="button" onClick={onClose}>
          {t("chat.common.cancel")}
        </button>
      </p>
    </section>
  );
}
