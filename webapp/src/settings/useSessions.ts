import { useCallback, useEffect, useState } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";

export type SessionFamily = components["schemas"]["SessionFamily"];

export type SessionsStatus = "loading" | "ready" | "failed";

export interface SessionsState {
  status: SessionsStatus;
  sessions: readonly SessionFamily[];
  /** Set when the last revocation failed; cleared by the next attempt. */
  actionFailed: boolean;
}

const FAILED: SessionsState = { status: "failed", sessions: [], actionFailed: false };

async function fetchSessions(): Promise<SessionsState> {
  try {
    const { data } = await api.GET("/api/v1/users/me/sessions");
    return data === undefined
      ? FAILED
      : { status: "ready", sessions: data.sessions, actionFailed: false };
  } catch (requestError) {
    console.warn("Session list request failed:", requestError);
    return FAILED;
  }
}

/**
 * The caller's signed-in devices — GET /api/v1/users/me/sessions, plus the two
 * revocations the `settings-sessions` artboard offers.
 *
 * The list is deliberately unpaged in the contract (bounded by the refresh
 * TTL, drawn as a flat list), so there is nothing to page here either.
 */
export function useSessions() {
  const [state, setState] = useState<SessionsState>({
    status: "loading",
    sessions: [],
    actionFailed: false,
  });

  const load = useCallback(() => fetchSessions().then(setState), []);

  useEffect(() => {
    void load();
  }, [load]);

  /** Signs one device out; the row goes as soon as the server confirms. */
  const revoke = useCallback(async (familyId: string) => {
    try {
      const { response } = await api.DELETE("/api/v1/users/me/sessions/{familyId}", {
        params: { path: { familyId } },
      });
      // 404 means it is already gone — the row should go either way. The
      // contract answers 404 for a family that is not the caller's, so this
      // never confirms anything about someone else's session.
      if (response.status === 204 || response.status === 404) {
        setState((previous) => ({
          ...previous,
          sessions: previous.sessions.filter((entry) => entry.family_id !== familyId),
          actionFailed: false,
        }));
        return;
      }
      setState((previous) => ({ ...previous, actionFailed: true }));
    } catch (requestError) {
      console.warn("Session revocation failed:", requestError);
      setState((previous) => ({ ...previous, actionFailed: true }));
    }
  }, []);

  /** Signs out everywhere else; this device is the only row left. */
  const revokeOthers = useCallback(async () => {
    try {
      const { response } = await api.POST("/api/v1/users/me/sessions/revoke-others");
      if (response.status === 204) {
        setState((previous) => ({
          ...previous,
          sessions: previous.sessions.filter((entry) => entry.current),
          actionFailed: false,
        }));
        return;
      }
      setState((previous) => ({ ...previous, actionFailed: true }));
    } catch (requestError) {
      console.warn("Sign-out-everywhere-else request failed:", requestError);
      setState((previous) => ({ ...previous, actionFailed: true }));
    }
  }, []);

  return { ...state, reload: load, revoke, revokeOthers };
}
