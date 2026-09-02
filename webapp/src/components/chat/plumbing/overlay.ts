import { useEffect } from "react";
import type { RefObject } from "react";

/**
 * The two anchored popovers' element ids, shared with their triggers'
 * `aria-controls`. Exactly one of each exists at a time, so constants are
 * enough and no id has to be threaded through props.
 */
export const CHANNEL_MENU_ID = "hm-channel-actions";
export const ACCOUNT_MENU_ID = "hm-account-menu";

/**
 * Gives focus back to whatever opened an anchored, non-modal popover.
 *
 * The dialogs use `useFocusTrap`, which both traps Tab and restores on
 * unmount. A popover must not trap (chat-addendum-menu-components §01: no
 * roving tabindex, no desktop focus trap), and it must not pull focus
 * backwards either — "an outside activation dismisses and leaves focus on the
 * chosen outside target" (§02). So the restore is conditional: it happens
 * only when nothing else has claimed focus, which is exactly the Escape, the
 * visible Close and the act-and-dismiss paths.
 */
export function useRestoreFocus(surfaceRef: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const opener = document.activeElement;
    // Captured here: the surface element does not change identity while it is
    // mounted, and reading the ref from the cleanup would read it too late.
    const surface = surfaceRef.current;
    return () => {
      const active = document.activeElement;
      const claimedElsewhere =
        active !== null && active !== document.body && !(surface?.contains(active) ?? false);
      if (!claimedElsewhere && opener instanceof HTMLElement) {
        opener.focus();
      }
    };
  }, [surfaceRef]);
}
