import type { ChangedMember, ChannelVerification, VerificationRecords } from "./types";

/**
 * The two predicates the send gate is built out of (ADR 008, decision 3).
 *
 * They are here rather than inline in `service.ts` because they are the whole
 * of the enforced invariant, and the invariant deserves to be readable and
 * testable on its own:
 *
 * > This device encrypts an application message only into a tree whose every
 * > non-own leaf key is inside the accepted (pinned or verified) set of the
 * > member the directory attributes it to.
 */

/**
 * What changed about one person since this device accepted their keys, or null
 * when the sets agree.
 *
 * `current` is the directory's claim about them — the party under test — and
 * `accepted` is what a human on this device pinned or verified. Every
 * acceptance names the exact set it accepted, which is what stops one
 * acceptance from generalizing to the next change.
 */
export function changeOf(
  userId: string,
  accepted: readonly string[],
  current: readonly string[],
): ChangedMember | null {
  const acceptedKeys = new Set(accepted);
  const currentKeys = new Set(current);
  const added = [...currentKeys].filter((key) => !acceptedKeys.has(key));
  const removed = [...acceptedKeys].filter((key) => !currentKeys.has(key));
  if (added.length === 0 && removed.length === 0) {
    return null;
  }
  // Which of the two a person is told about matters: "a new device appeared"
  // and "the key you checked is gone" read very differently to somebody
  // deciding whether to worry.
  return { userId, kind: removed.length === 0 ? "newDevice" : "replacedKey", added, removed };
}

/**
 * The whole verification state of one conversation: who changed, and how many
 * leaves the directory attributes to nobody current.
 *
 * ## Why these two scans are the invariant
 *
 * The invariant fails for a leaf `k` exactly when either
 *
 *  1. `k` is in no member's directory set — it is attributed to nobody
 *     current, so there is no accepted set for it to be inside. Counted as an
 *     uncovered leaf. This is precisely what ADR 007's allow-list sweep
 *     evicts, so the branch heals itself on the next reconcile without asking
 *     anybody anything; until then the pre-sweep window is a non-sending
 *     window, which is the stronger true statement.
 *  2. `k` is attributed to member X and is not in X's accepted set. Then `k`
 *     is in X's *directory* set and not in X's *accepted* set, so X's `added`
 *     is non-empty and X appears in `changed`.
 *
 * So `changed` plus `uncoveredLeaves` covers every way the invariant can fail.
 * The state is deliberately a little stronger than the invariant: a member the
 * directory has changed blocks sending even before any commit puts their new
 * leaf in the tree, which is what ADR 008 asks for — the leaf is what the next
 * reconcile is about to add.
 *
 * @param directory  Each current member and the keys the directory lists for
 *                   them. The claim under test; never a source of truth about
 *                   this device's own account.
 * @param treeKeys   Every leaf signature key the group holds, base64.
 * @param ownKey     This device's own leaf key — the one leaf it never judges,
 *                   because it is the one key it did not learn from anybody.
 */
export function inspect(
  directory: ReadonlyMap<string, readonly string[]>,
  treeKeys: readonly string[],
  ownKey: string | null,
  accepted: (userId: string) => readonly string[],
): ChannelVerification {
  const changed: ChangedMember[] = [];
  const attributed = new Set<string>();
  for (const [userId, keys] of directory) {
    for (const key of keys) {
      attributed.add(key);
    }
    const change = changeOf(userId, accepted(userId), keys);
    if (change !== null) {
      changed.push(change);
    }
  }

  const uncoveredLeaves = treeKeys.filter(
    (key) => key !== ownKey && !attributed.has(key),
  ).length;

  return { changed, uncoveredLeaves };
}

/**
 * The keys this device accepts for a person.
 *
 * For a peer that is exactly their record. For **this device's own account**
 * it is the local signature key plus own devices a human explicitly accepted —
 * and never, under any circumstance, the directory's list.
 *
 * That asymmetry is not a detail; it is the entire security of the ceremony
 * (ADR 008, decision 2). If both sides computed both halves of a safety number
 * from the directory, a planted key would land in both computations, the two
 * numbers would match, and the ceremony would bless the attack. Because this
 * device's own half comes from `signature_public_key()` — which it generated
 * and never learned from the server — a planted key can appear in the peer's
 * view of us and cannot appear in our own, and that mismatch *is* the
 * detection.
 *
 * This cannot drift into reading the directory for the own set: doing so would
 * leave every function still returning plausible numbers, every test about
 * matching still passing, and the ceremony silently worthless. The two tests
 * that exist to catch it are in `service.test.ts`, under "safety numbers":
 * `computes our own half from our own device key, never from the directory`
 * and `mismatches exactly when a key is planted, where a circular one would
 * match`.
 */
export function acceptedKeys(
  userId: string,
  records: VerificationRecords,
  currentUserId: string,
  ownKey: string | null,
): string[] {
  const recorded = records[userId]?.keys ?? [];
  if (userId !== currentUserId) {
    return recorded;
  }
  // The local key is always accepted and is never asked about: a device
  // refusing to encrypt to itself would be a bug, not a defence.
  const own = ownKey === null ? [] : [ownKey];
  return [...new Set([...own, ...recorded])];
}
