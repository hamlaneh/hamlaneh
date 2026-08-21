import type { Presence } from "./types";

/**
 * Presence label keys, as a static map: a template-literal key would not
 * survive the typed-resource check, and presence must always be paired with a
 * word — the dot alone never carries the state.
 */
export const PRESENCE_LABEL_KEY = {
  online: "chat.presence.online",
  away: "chat.presence.away",
  offline: "chat.presence.offline",
} as const satisfies Record<Presence, string>;
