import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";

import { EllipsisVerticalIcon } from "../icons";

export interface RowMenuItem {
  key: string;
  label: string;
  icon: ReactNode;
  /**
   * `danger` for a destructive item, `constructive` for its inverse
   * (Reactivate restores sign-in, so it is brand, not danger).
   */
  tone?: "default" | "danger" | "constructive";
  /** Draws a rule above this item — the separator before the last group. */
  separated?: boolean;
  disabled?: boolean;
  /** Why the item cannot be used, for the disabled case (title + aria). */
  disabledReason?: string;
  onSelect: () => void;
}

interface RowMenuProps {
  /** Accessible name of the trigger, e.g. "Actions for a.jones". */
  label: string;
  items: readonly RowMenuItem[];
  disabled?: boolean;
}

/**
 * The one ellipsis menu every row action lives in (`admin-components` §02) —
 * not a row of icons, so the table reads the same at 12 users and at 400.
 *
 * Deliberately NOT `role="menu"`: that role promises arrow-key navigation and
 * a roving tabindex, and a menu that claims it without implementing it is
 * worse for a screen-reader user than a plain group of buttons, which Tab
 * already walks. Escape closes and hands focus back to the trigger.
 */
export function RowMenu({ label, items, disabled = false }: RowMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listId = useId();

  useEffect(() => {
    if (!open) {
      return undefined;
    }
    const onPointerDown = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || containerRef.current?.contains(event.target) !== true) {
        setOpen(false);
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [open]);

  const close = (returnFocus: boolean) => {
    setOpen(false);
    if (returnFocus) {
      triggerRef.current?.focus();
    }
  };

  return (
    <div
      className="hm-admin-rowmenu"
      ref={containerRef}
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          event.stopPropagation();
          close(true);
        }
      }}
      // Tabbing out of the last item should not leave an orphan popover open.
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          setOpen(false);
        }
      }}
    >
      <button
        type="button"
        ref={triggerRef}
        className="hm-admin-rowmenu__trigger"
        aria-label={label}
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        disabled={disabled}
        onClick={() => {
          setOpen((current) => !current);
        }}
      >
        <EllipsisVerticalIcon size={17} strokeWidth={2} />
      </button>
      {open ? (
        <ul className="hm-admin-rowmenu__list" id={listId} aria-label={label}>
          {items.map((item) => (
            <li key={item.key}>
              {item.separated === true ? (
                <span className="hm-admin-rowmenu__rule" aria-hidden="true" />
              ) : null}
              <button
                type="button"
                className={`hm-admin-rowmenu__item${
                  item.tone === undefined || item.tone === "default"
                    ? ""
                    : ` hm-admin-rowmenu__item--${item.tone}`
                }`}
                disabled={item.disabled}
                title={item.disabledReason}
                onClick={() => {
                  close(false);
                  item.onSelect();
                }}
              >
                {item.icon}
                {item.label}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
