import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../../api/client";
import type { UserSummary } from "../../../chat/types";

/**
 * UNDESIGNED SURFACE — plain semantic HTML, no styling beyond structure.
 *
 * The mockup draws neither an invite picker nor a DM picker, so both are
 * functional plumbing over the same directory endpoint until a design lands.
 */

interface PeoplePickerProps {
  title: string;
  actionLabel: string;
  onPick: (user: UserSummary) => Promise<boolean>;
  onClose: () => void;
}

/** One directory request per pause in typing, not per keystroke. */
const QUERY_DEBOUNCE_MS = 200;

export function PeoplePicker({ title, actionLabel, onPick, onClose }: PeoplePickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [people, setPeople] = useState<UserSummary[]>([]);
  const [failed, setFailed] = useState(false);
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
      className="hm-plumbing"
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
      {failed ? <p role="alert">{t("chat.people.failed")}</p> : null}
      <ul>
        {people.map((person) => (
          <li key={person.id}>
            <button
              type="button"
              onClick={() => {
                void onPick(person).then((ok) => {
                  if (ok) {
                    onClose();
                  } else {
                    setFailed(true);
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
