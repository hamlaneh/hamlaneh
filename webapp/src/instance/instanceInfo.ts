import { createContext, useContext } from "react";

import { api } from "../api/client";
import type { components } from "../api/schema";
import { PASSWORD_MIN_LENGTH } from "../auth/passwordPolicy";

export type InstanceInfo = components["schemas"]["InstanceInfo"];

/**
 * What the client assumes when GET /api/v1/instance has not answered (yet, or
 * at all).
 *
 * `password_reset_available` is false on purpose. The field exists so a
 * "Forgot password?" link never goes nowhere (contract: "a link that silently
 * goes nowhere is dishonest"), and offering the link on an instance we could
 * not ask would be exactly that. A reachable server always answers this
 * document, so the pessimistic default costs nothing real.
 *
 * `password_min_length` falls back to the contract's own minimum so the
 * requirement line and the client-side length check never disagree with the
 * server by more than the instance's own stricter policy.
 */
export const FALLBACK_INSTANCE_INFO: InstanceInfo = {
  password_min_length: PASSWORD_MIN_LENGTH,
  password_reset_available: false,
};

export interface InstanceState {
  info: InstanceInfo;
  /** False until the document has been fetched; `info` is the fallback then. */
  loaded: boolean;
}

export const InstanceContext = createContext<InstanceState>({
  info: FALLBACK_INSTANCE_INFO,
  loaded: false,
});

/** Instance capabilities and policy, served with the form rather than compiled in. */
export function useInstance(): InstanceState {
  return useContext(InstanceContext);
}

/** Reads the public instance document; the fallback stands in on any failure. */
export async function fetchInstanceInfo(): Promise<InstanceInfo> {
  try {
    const { data } = await api.GET("/api/v1/instance");
    return data ?? FALLBACK_INSTANCE_INFO;
  } catch (requestError) {
    // Unreachable or malformed: the screens still render, with the
    // conservative defaults above.
    console.warn("Instance document lookup failed:", requestError);
    return FALLBACK_INSTANCE_INFO;
  }
}
