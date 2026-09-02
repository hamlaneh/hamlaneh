import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode, RefObject } from "react";
import { useTranslation } from "react-i18next";

import { api } from "../../../api/client";
import type { UserSummary } from "../../../chat/types";
import { LoaderCircleIcon } from "../../icons";
import { Avatar } from "../Avatar";

/**
 * Mentions — `chat-addendum-mention-picker-light` / `-dark` / `-states`, with
 * the contract on `chat-addendum-overlay-components` §03.
 *
 * The @-query behaviour is a product requirement of the addendum, not a
 * reskin: the picker opens both from an `@` typed at the current composer
 * token and from the existing "Mention someone" control, and both paths are
 * the same component and the same listbox.
 *
 * The model is an editable combobox over a listbox with
 * `aria-activedescendant`, and **focus never leaves the composer**. Arrow
 * Up/Down moves the active row, Enter inserts, Escape closes and leaves the
 * caret exactly where it was, Left/Right keep moving the caret, and Tab closes
 * without inserting. The popover has no scrim and does not trap focus — it is
 * a popover, not a dialog.
 *
 * Filtering is client-side over the already-loaded channel member list; no new
 * server search endpoint is implied. The stored reference is `<@{user_id}>`
 * (openapi.yaml -> SendMessageRequest) and the UI never renders the UUID or
 * the raw token — names are neither unique nor stable, and in Persian they
 * cannot match the username pattern at all.
 */

interface MentionsOptions {
  channelId: string;
  /** The composer's current value and its setter — this hook edits the draft. */
  draft: string;
  setDraft: (next: string) => void;
  fieldRef: RefObject<HTMLTextAreaElement | null>;
}

interface Mentions {
  open: boolean;
  /** For the composer's `aria-controls`. */
  listId: string;
  /** For the composer's `aria-activedescendant`; undefined while closed. */
  activeId: string | undefined;
  /** The anchored popover. Render it inside the composer's positioned box. */
  popover: ReactNode;
  /** True when the key belonged to the picker and the composer must not act. */
  handleKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => boolean;
  /** Re-reads the token under the caret after a change or a caret move. */
  sync: () => void;
  /** The "Mention someone" control's path into the same listbox. */
  openFromTrigger: () => void;
}

/** The token being edited: where its `@` sits, and what follows it. */
interface Token {
  start: number;
  query: string;
  /** Opened by the control rather than by a typed `@`. */
  fromTrigger: boolean;
}

type Status = "loading" | "ready" | "failed";

/**
 * The `@` at the caret, if there is one.
 *
 * It counts only at a word boundary, so an address or a `<@uuid>` already in
 * the draft never reopens the list.
 */
function tokenAt(text: string, caret: number): { start: number; query: string } | null {
  const match = /(?:^|\s)@([^\s@]*)$/.exec(text.slice(0, caret));
  if (match === null) {
    return null;
  }
  const query = match[1] ?? "";
  return { start: caret - query.length - 1, query };
}

function matches(members: readonly UserSummary[], query: string): UserSummary[] {
  const needle = query.trim().toLocaleLowerCase();
  if (needle === "") {
    return [...members];
  }
  return members.filter(
    (member) =>
      member.username.toLocaleLowerCase().includes(needle) ||
      member.display_name.toLocaleLowerCase().includes(needle),
  );
}

export function useMentions({ channelId, draft, setDraft, fieldRef }: MentionsOptions): Mentions {
  const { t } = useTranslation();
  const [token, setToken] = useState<Token | null>(null);
  const [members, setMembers] = useState<UserSummary[]>([]);
  const [status, setStatus] = useState<Status>("loading");
  const [activeIndex, setActiveIndex] = useState(0);
  /** Where the caret must land once the inserted draft has rendered. */
  const pendingCaret = useRef<number | null>(null);
  const listId = useId();
  const rowIdPrefix = useId();

  const open = token !== null;
  const rows = token === null ? [] : matches(members, token.query);
  const active = rows[Math.min(activeIndex, rows.length - 1)];

  // One in-flight member request at a time; a reopen or an unmount cancels it.
  const inFlight = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      inFlight.current?.abort();
    },
    [],
  );

  const load = useCallback(() => {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    setStatus("loading");
    void (async () => {
      try {
        const { data } = await api.GET("/api/v1/channels/{channelId}/members", {
          params: { path: { channelId }, query: { limit: 100 } },
          signal: controller.signal,
        });
        setMembers(data?.members ?? []);
        setStatus("ready");
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        console.warn("Could not load channel members:", error);
        setMembers([]);
        setStatus("failed");
      }
    })();
  }, [channelId]);

  // Members are fetched when the list first opens for this channel, then
  // filtered on this device: the sheet is explicit that no server search
  // endpoint is implied.
  const loaded = useRef<string | null>(null);
  useEffect(() => {
    if (open && loaded.current !== channelId) {
      loaded.current = channelId;
      load();
    }
  }, [open, channelId, load]);

  // The caret is restored after the inserted draft has rendered; setting it in
  // the handler would be overwritten by React's own value update.
  useEffect(() => {
    const caret = pendingCaret.current;
    if (caret === null) {
      return;
    }
    pendingCaret.current = null;
    const field = fieldRef.current;
    field?.focus();
    field?.setSelectionRange(caret, caret);
  }, [draft, fieldRef]);

  const close = () => {
    setToken(null);
    setActiveIndex(0);
  };

  const sync = () => {
    const field = fieldRef.current;
    if (field === null) {
      return;
    }
    const found = tokenAt(field.value, field.selectionStart);
    if (found === null) {
      // Only a typed token closes itself; one the control opened stays open
      // until Escape, Tab, an insertion or a caret that has moved away.
      setToken((current) => (current?.fromTrigger === true ? current : null));
      return;
    }
    setToken({ ...found, fromTrigger: false });
    setActiveIndex(0);
  };

  const insert = (member: UserSummary) => {
    const field = fieldRef.current;
    if (token === null || field === null) {
      return;
    }
    const before = draft.slice(0, token.start);
    // A typed `@` already sits after a space or at the start. One the control
    // opened lands at the caret, so it brings its own separator.
    const lead = token.fromTrigger && before !== "" && !/\s$/u.test(before) ? " " : "";
    const reference = `${lead}<@${member.id}>`;
    const after = draft.slice(field.selectionStart);
    pendingCaret.current = token.start + reference.length;
    setDraft(before + reference + after);
    close();
  };

  const openFromTrigger = () => {
    const field = fieldRef.current;
    const caret = field?.selectionStart ?? draft.length;
    setToken({ start: caret, query: "", fromTrigger: true });
    setActiveIndex(0);
    // Focus never leaves the composer, so the control hands it straight back.
    field?.focus();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (!open) {
      return false;
    }
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp": {
        if (rows.length === 0) {
          return false;
        }
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        setActiveIndex((index) => (index + step + rows.length) % rows.length);
        return true;
      }
      case "Enter": {
        if (active === undefined) {
          return false;
        }
        event.preventDefault();
        insert(active);
        return true;
      }
      case "Escape": {
        // Closes and leaves the caret exactly where it was.
        event.preventDefault();
        close();
        return true;
      }
      case "Tab": {
        // Closes and moves on. It never inserts on Tab.
        close();
        return false;
      }
      default:
        // Left/Right and every other key keep moving the caret; `sync` on
        // keyup re-reads the token they landed in.
        return false;
    }
  };

  const rowId = (index: number) => `${rowIdPrefix}-${String(index)}`;
  const activeId =
    open && active !== undefined ? rowId(rows.indexOf(active)) : undefined;

  const popover = !open ? null : (
    <div className="hm-mention-popover">
      <p className="hm-mention-popover__header" id={`${listId}-label`}>
        {t("chat.mention.listLabel")}
      </p>
      {status === "loading" ? (
        <p className="hm-mention-popover__state" role="status">
          <LoaderCircleIcon size={14} className="hm-spin" /> {t("chat.mention.loading")}
        </p>
      ) : status === "failed" ? (
        <p className="hm-mention-popover__state" role="alert">
          {t("chat.mention.loadFailed")}
        </p>
      ) : rows.length === 0 ? (
        <p className="hm-mention-popover__state" role="status">
          {t("chat.mention.noMatch")}
        </p>
      ) : (
        <ul className="hm-mention-list" role="listbox" id={listId} aria-labelledby={`${listId}-label`}>
          {rows.map((member, index) => (
            <li
              key={member.id}
              id={rowId(index)}
              className="hm-mention-row"
              role="option"
              aria-selected={member === active}
              /* Pointer only. The listbox never takes focus: the composer
                 stays the focused element and owns the keyboard. */
              onMouseDown={(event) => {
                event.preventDefault();
                insert(member);
              }}
            >
              <Avatar
                userId={member.id}
                displayName={member.display_name}
                size={30}
                typeSize={12}
              />
              <span className="hm-mention-row__text">
                <span className="hm-mention-row__name" dir="auto">
                  {member.display_name}
                </span>
                <bdi className="hm-mention-row__username" dir="ltr">
                  @{member.username}
                </bdi>
              </span>
              {/* Weight, the inline-start bar and this hint are what keep the
                  keyboard-active row from looking like a hovered one. The
                  mention list carries no presence: a mention does not depend
                  on it. */}
              {member === active ? (
                <span className="hm-mention-row__hint" aria-hidden="true">
                  Enter
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );

  return { open, listId, activeId, popover, handleKeyDown, sync, openFromTrigger };
}
