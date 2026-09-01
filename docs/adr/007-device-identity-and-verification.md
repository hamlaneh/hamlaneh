# ADR 007 — Device identity: what eviction selects by, and what verification actually closes

**Status:** accepted · 2026-08-29 · **Scope:** Phase 3, slice 3.2

> Accepted after checking the claim the whole record rests on against the code rather than the
> report of it: `remove_user` filters leaves on `member.credential.serialized_content() ==
> identity` (`webapp/src-mls/src/lib.rs`), and `credential_for` writes whatever string the
> enrolling client passed. A leaf credentialed under another member's id is therefore
> unremovable today, which is what makes this ADR a fix rather than a precaution.

## Context

Slice 3.1 shipped working message E2EE under ADR 006's boundaries: the server is MLS-blind, a
leaf is a device, and the credential identity inside each leaf is the user's id **as a string the
enrolling client chose** (`credential_for` in `webapp/src-mls/src/lib.rs`). Its security review
found the hole this ADR closes, now named on the roadmap: removal evicts by that client-asserted
string — `remove_user` filters leaves on credential identity, and `reconcileMembers` in
`webapp/src/mls/service.ts` computes staleness from `member_identities`, which returns the same
strings. RFC 9420 does not require identities to be unique, and this design legitimately shares
one identity across a person's devices — so nothing stops any member from minting a leaf
credentialed under a *current* member's id. Such a leaf is never stale (its claimed identity is
in the roster) and never selected by any removal (its claimed identity is not the removed
user's). It survives every eviction and reads every epoch that follows.

The server's membership gate does stop the API from handing ciphertext to a removed account. But
PLAN §6.1's adversary 3 is a compromised or untrusted server — Phase 3's goal line — and against
that adversary the transport gate is not a defence; it *is* the adversary. Meanwhile the only
mapping of people to signature keys anywhere in the system is the server's device directory:
`mls_devices`, written by `RegisterMlsDevice` under an authenticated session, never
signature-verified, handed out by `ClaimMlsKeyPackages`. That is the uncomfortable shape of the
whole problem: **the party E2EE defends against operates the directory E2EE depends on.** The
slice brief put three confirm-or-refute questions; each decision below answers one.

## Decision 1 — no server-signed credentials; the directory is a claim only humans can turn into a proof

Q1 confirmed: the server signing "key K belongs to user X" into the MLS credential is not
adopted, because it adds no security under any §6.1 adversary.

Under adversary 3 the argument is one sentence: the signer is the adversary, and a signature by
the adversary over the fact in dispute is not evidence — a compromised server mints "attacker key
K′ belongs to alice" with the same key it signs truths with.

The adversary such a signature *does* bind is the weaker, in-model one: a malicious member
(§6.1 adversary 2) cannot forge the server's signature, so signed credentials would stop a member
minting a leaf credentialed as someone else. But that defence needs no signature, because every
client already reaches the directory over the same authenticated TLS the server's signing key
would have to be distributed over. A live directory read carries the same authority — the
server's word — with better freshness (a signature is a cached directory claim that outlives
revocation of the device it names) and no new machinery. So the signature is redundant under
adversary 2, worthless under adversary 3, and under every adversary in the model it adds moving
parts, not security. Decision 2 gets the insider defence from the directory read it needs anyway.

What has independent force under a hostile server is a binding whose trust root sits outside the
server: **two humans comparing leaf signature key material out of band** — the safety-numbers/QR
work PLAN §6.4 already names. Everything else in this space is one of: UX around that root;
pinning/TOFU, which detects a key that *changes* after first sight and so defends against the
strictly weaker sub-case "server compromised later" (this is the roadmap's key-swap warning, and
it belongs to the verification slice); or, post-v1, key transparency, which trades prevention for
detection under a different root — public append-only consistency. None of these upgrade the
directory into a proof. They make its lies survivable or visible.

## Decision 2 — eviction selects by leaf signature key, against a directory allow-list; the credential identity is demoted to a display hint

Q2 confirmed on the selector and the source — eviction must select leaves by signature key, with
the person↔key mapping read from the server's directory — with one refinement that carries the
security. Read as "look up the removed user's directory keys, evict those", the rule re-opens the
hole: a planted leaf's key is precisely a key the directory never listed under the removed user.
The rule that closes it is an allow-list sweep over the whole tree:

> After reconciliation, every leaf whose signature key the directory does not map to a
> **current channel member** has been evicted.

Per-user removal falls out — a user leaving the roster removes their keys from the allowed set —
and a rogue leaf is evicted no matter what its credential claims, because nothing security-
relevant reads the credential any more. The identity string stays in the credential as a display
hint (mapping leaves to names in UI and diagnostics); it is never again an eviction key. The
own-leaf exclusion stays: a group cannot evict itself, and a device the directory has dropped
learns that by being evicted by others.

Two structural facts force this shape rather than the tempting alternative, validating adds:

- **A client cannot refuse a sequenced commit.** The server's log is the group's order
  (ADR 006); a member that declines to apply a commit forks itself off the log and stalls — there
  is no veto path. Add-time inspection can therefore only ever warn (verification-slice work);
  the only enforcement point this design has is the next commit, which is eviction.
- **The adder cannot tell a real device from a forged one anyway.** `add_members` validates key
  package cryptography, not attribution; a key package's credential is written by whoever built
  the package. Checking "does the claimed credential match the user I asked for" tests the
  attacker's spelling, nothing else.

Residual trust, stated precisely. The sweep trusts the directory *data* at reconcile time, and a
lying directory has exactly two moves: attribute an attacker's key to a current member — the leaf
survives the sweep, which is the confidentiality residue, is exactly the key↔person binding, and
is exactly what Decision 1's out-of-band verification closes; or omit a real device — a
legitimate leaf is evicted, which is denial of service, something a hostile transport always has
cheaper versions of, and it self-heals (the evicted device is re-added by the ordinary reconcile
path once listed again; likewise a device registered between a reconciler's directory fetch and
its sweep is falsely evicted once and re-added on the next pass). On the question's "exactly, and
only": within confidentiality, yes. Verification does not validate the roster itself — who is a
member at all is the transport authority, overt in every client's UI, not a covert-crypto matter
— and it does not stop DoS. Neither was ever E2EE's claim.

## Decision 3 — the sweep ships now, before any verification UX, and here is what it does and does not make true

Q3: the smallest change that makes the roadmap's "a removed member cannot decrypt anything sent
after removal" hold as strongly as it can hold pre-verification is Decision 2's sweep. Three
touchpoints, no new cryptography:

1. **Contract:** one channel-scoped read of the roster's device signature public keys, frozen at
   the slice-3.2 contract step. Co-members already learn device ids at claim time, so this is the
   same information class — but it is a new endpoint: authz matrix entry and `/security-review`,
   per the workflow.
2. **Wasm:** export member leaf signature keys alongside identities, and `remove_leaves` selecting
   by key. Plumbing over `member.signature_key`, which `add_members` already reads — glue only.
3. **Service:** `reconcileMembers` computes the allowed set (union of directory keys over current
   members), evicts group keys outside it, then adds as today; `memberRemoved` becomes a
   reconcile trigger rather than an identity lookup.

In the honest register (PLAN §2.4), what this buys: **the guarantee stops resting on every past
member having been honest and rests on the directory data having been honest at reconcile time.**
After the change, a removed member cannot decrypt what follows their eviction, whatever they
credentialed their leaves as, and the same holds against any in-model insider — the class of
attacker slice 3.1's review actually demonstrated. Before it, the guarantee failed with no server
compromise at all.

What it does not defend against: a compromised server planting a mis-attributed key in the
directory. Nothing pre-verification can — that residue is Decision 2's, closed by the
verification slice and later audited by key transparency — so the drills and e2e tests that
assert removal must name the adversary they assert against, and no doc or UI string may describe
this slice's result as making removal hold against a hostile server. Timing, stated rather than
hidden: the ordinary path evicts at the removal commit itself, raised by the first remaining
client to see `member_removed`, so "after removal" means after that commit lands; a *planted*
leaf's window runs until any remaining member's next reconcile (channel open, commit nudge,
member events, reconnect).

## Deliberately not decided

- Safety-number/QR encoding and the comparison UX — the verification slice, which also owns
  pinning and the key-swap warning the roadmap's test demands.
- Key-transparency log design — post-v1, per the roadmap.
- Multi-device key sync, backups, recovery — their own slices, unchanged by this ADR.
- The exact contract shape of the directory read (path, pagination) — the contract step decides;
  this ADR fixes the information exposed and its authz class (channel members only).

## What must happen next, in order

1. **Contract step for slice 3.2** — the member-device-keys read — finalized before any
   implementation agent spawns, per the workflow.
2. **Opus implementation** of the three touchpoints, carrying the regression test this ADR
   exists for: a leaf credentialed under a staying member's id, planted by a since-removed
   member, is evicted by the first reconcile. Removal e2e keeps passing; the authz matrix gains
   the new endpoint.
3. **Fable adversarial review, read-only.** The falsifiable question: confirm or refute — after
   one reconcile pass by an honest member, can any leaf whose signature key the directory does
   not map to a current channel member still hold the group's current epoch secrets?
4. **Verification slice next** (safety numbers, pinning, key-swap warning), then multi-device,
   which inherits a directory whose consumption is already key-based.

## Same-PR updates this ADR requires

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (device identity: key-based eviction
against the directory; out-of-band verification is the binding — ADR 007). ROADMAP Phase 3: the
multi-device bullet's verification note gains a pointer here, and the eviction fix becomes its
own slice-3.2 checkbox rather than waiting inside multi-device. The stack tables in CLAUDE.md and
PLAN §4 are **unchanged** — no new component, no new dependency. OVERVIEW is untouched by the
ADR itself; the implementing slice updates it with the endpoint it adds.
