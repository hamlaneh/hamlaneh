import { useCallback, useEffect, useState } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";

export type TotpStatus = components["schemas"]["TotpStatus"];

export interface TotpStatusState {
  status: "loading" | "ready" | "failed";
  totp: TotpStatus;
}

const FAILED: TotpStatusState = { status: "failed", totp: { enabled: false } };

async function fetchTotpStatus(): Promise<TotpStatusState> {
  try {
    const { data } = await api.GET("/api/v1/users/me/totp");
    return data === undefined ? FAILED : { status: "ready", totp: data };
  } catch (requestError) {
    console.warn("Two-step status request failed:", requestError);
    return FAILED;
  }
}

/**
 * The Security card's two-step state — GET /api/v1/users/me/totp.
 *
 * While two-step verification is off the contract omits the three nullable
 * fields rather than sending nulls, so nothing here may assume they exist:
 * `enabled` is the only field always present, and the on-state card checks the
 * others before drawing a row for them.
 */
export function useTotpStatus() {
  const [state, setState] = useState<TotpStatusState>({
    status: "loading",
    totp: { enabled: false },
  });

  const load = useCallback(() => fetchTotpStatus().then(setState), []);

  useEffect(() => {
    void load();
  }, [load]);

  return { ...state, reload: load };
}
