import type { components } from "../api/schema";

export type EncryptionMode = components["schemas"]["EncryptionMode"];

/**
 * The organisation's encryption mode decides what a channel or DM is born as
 * (ADR 011 decision 1), and the client reads it from the public instance
 * document rather than assuming it.
 *
 * It is published there rather than only on `OrgSettings` because that
 * document answers 403 to everyone who is not an administrator, and the
 * creation surfaces belong to every member. When the document cannot be
 * fetched at all, `FALLBACK_INSTANCE_INFO` supplies `strict` — the secure
 * assumption, and currently the only mode the API can be in.
 *
 * Being wrong is corrected where it would matter rather than prevented: a
 * creation surface sends the value it displayed instead of omitting it, so a
 * client working from a stale mode is refused (`e2ee_required_by_org` /
 * `e2ee_forbidden_by_org`) and says so, rather than silently handing somebody
 * the opposite of what their screen promised about a property that can never
 * be changed afterwards.
 */

/** What a conversation born under `mode` is. The mode is the whole rule. */
export function bornEncrypted(mode: EncryptionMode): boolean {
  return mode === "strict";
}
