import { useCallback, useEffect, useState } from "react";

/**
 * Loading / ready / failed, for the three admin tables and the settings form.
 *
 * `admin-components` §04 draws all three: the header renders immediately with
 * skeleton rows underneath, and a failure is a card that says nothing has
 * changed and offers Retry in place. That is the whole state machine, so this
 * is the whole hook.
 */
export type AdminResource<T> =
  | { status: "loading" }
  | { status: "ready"; data: T }
  | { status: "error" };

export interface AdminResourceHandle<T> {
  state: AdminResource<T>;
  /** Re-runs the loader — the Retry button, and the refresh after a mutation. */
  reload: () => void;
  /** Replaces loaded data without a round trip (a PATCH answered with the row). */
  update: (next: T) => void;
}

/**
 * `load` must be stable (wrap it in useCallback): it is the effect's only
 * dependency, and an inline lambda would re-fetch on every render.
 */
export function useAdminResource<T>(load: () => Promise<T>): AdminResourceHandle<T> {
  const [state, setState] = useState<AdminResource<T>>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let live = true;
    // Setting "loading" here is the effect telling React what it is about to
    // go and do, not deriving state from props — the rule's own exception.
    // Without it a reload paints the previous page's rows until the new ones
    // arrive, which reads as "nothing happened".
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState({ status: "loading" });
    void load().then(
      (data) => {
        if (live) {
          setState({ status: "ready", data });
        }
      },
      (loadError: unknown) => {
        // The card says only that the server did not answer; the reason goes
        // to the console, where it cannot leak into the page.
        console.warn("Admin resource failed to load:", loadError);
        if (live) {
          setState({ status: "error" });
        }
      },
    );
    return () => {
      live = false;
    };
  }, [load, attempt]);

  const reload = useCallback(() => {
    setAttempt((current) => current + 1);
  }, []);

  const update = useCallback((next: T) => {
    setState({ status: "ready", data: next });
  }, []);

  return { state, reload, update };
}
