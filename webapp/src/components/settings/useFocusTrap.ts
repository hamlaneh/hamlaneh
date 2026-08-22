import { useEffect } from "react";
import type { KeyboardEvent, RefObject } from "react";

/**
 * Everything a keyboard can reach. Deliberately no visibility filter: jsdom
 * has no layout, and every control inside an open dialog is on screen anyway.
 */
const FOCUSABLE = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

/**
 * Traps Tab inside a dialog and gives focus back when it closes.
 *
 * `restoreTo` names the control focus should return to — the settings panel
 * hands it the sidebar gear, which is what the handoff requires. Without one,
 * focus returns to whatever had it when the dialog opened, which is the right
 * answer for a confirm dialog raised from a button that is still there.
 *
 * Returns the keydown handler to put on the dialog element; it stops
 * propagation so a dialog nested inside another traps its own Tab first.
 */
export function useFocusTrap(
  containerRef: RefObject<HTMLElement | null>,
  restoreTo?: RefObject<HTMLElement | null>,
) {
  useEffect(() => {
    const opener = restoreTo?.current ?? document.activeElement;
    // The container carries the dialog's accessible name, so landing on it
    // (rather than on its first control) is what announces where you are.
    containerRef.current?.focus();
    return () => {
      if (opener instanceof HTMLElement) {
        opener.focus();
      }
    };
  }, [containerRef, restoreTo]);

  return (event: KeyboardEvent<HTMLElement>) => {
    if (event.key !== "Tab") {
      return;
    }
    const container = containerRef.current;
    if (container === null) {
      return;
    }
    event.stopPropagation();
    const focusable = [...container.querySelectorAll<HTMLElement>(FOCUSABLE)];
    const first = focusable[0];
    const last = focusable.at(-1);
    if (first === undefined || last === undefined) {
      // Nothing to move to: keep focus where it is rather than letting Tab
      // walk out of the dialog.
      event.preventDefault();
      return;
    }
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === container)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  };
}
