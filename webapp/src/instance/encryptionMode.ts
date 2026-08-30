import type { components } from "../api/schema";

export type EncryptionMode = components["schemas"]["EncryptionMode"];

/**
 * What this client believes the organisation's encryption mode is — the rule
 * that decides what a channel or DM is born as (ADR 011 decision 1).
 *
 * CONTRACT GAP, reported rather than patched: nothing a member can read
 * publishes `encryption_mode`. It lives only on `OrgSettings`, behind
 * `/api/v1/admin/org`, which answers 403 to everyone who is not an
 * administrator — and the creation surfaces belong to every member. So the
 * client assumes the mode here.
 *
 * Assuming `strict` is not a guess. ADR 011 decision 3 fixes every install and
 * every migrated instance as Strict and refuses `compliance` with 409
 * `encryption_mode_locked` until encryption at rest, a retention policy and
 * compliance export exist, so it is currently the only mode the API can be in.
 * It is also the secure one, which is what a client should assume when it
 * cannot ask. The day Compliance unlocks, `InstanceInfo` needs the field and
 * this constant becomes a read of it — the surfaces already take the mode as
 * an input and behave correctly under both, so that is the only change.
 *
 * Until then the assumption is corrected where being wrong would matter: a
 * creation surface sends the value it displayed rather than omitting it, so a
 * stale client is refused (`e2ee_required_by_org` / `e2ee_forbidden_by_org`)
 * and says so, instead of silently handing the user the opposite of what their
 * screen promised about a property that can never be changed afterwards.
 */
export const ORG_ENCRYPTION_MODE: EncryptionMode = "strict";

/** What a conversation born under `mode` is. The mode is the whole rule. */
export function bornEncrypted(mode: EncryptionMode): boolean {
  return mode === "strict";
}
