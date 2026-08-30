# ADR 008 — Key verification: where trust lives, what the humans compare, and where refusal bites

**Status:** accepted · 2026-08-29 · **Scope:** Phase 3, slice 3.3

> Accepted after checking the claim Decision 2 turns on: the own half must not come from the
> directory, and it does not have to — `signature_public_key` returns `self.signer.public()`
> (`webapp/src-mls/src/lib.rs`), which this device generated and never learned from the server,
> while `member_signature_keys` reads the tree. The non-circularity rule is implementable with
> what already exists.

> Written against the code rather than the report of it: the send path already refuses by
> yielding nothing (`encrypt` in `webapp/src/mls/service.ts` returns null and the composer sends
> nothing — the null-refusal rail this ADR extends); the keystore already wraps state under a
> non-extractable AES-GCM key with its ceiling honestly documented (`webapp/src/mls/keystore.ts`);
> `listMlsMemberDevices` already publishes every member's signature-key set and
> `member_signature_keys` already exports the tree's (`webapp/src-mls/src/lib.rs`). Every input
> this design needs exists. The slice is client-only: no endpoint, no migration, no contract change.

## Context

ADR 007 fixed eviction and named its own residue precisely: the sweep trusts the directory's
key↔person mapping at reconcile time, so a directory that attributes an attacker's key to a
*current* member plants a leaf that survives every sweep and reads every epoch that follows. Its
Decision 1 established the only binding whose trust root sits outside the server: two humans
comparing leaf signature key material out of band. This slice builds that binding, plus the
pinning that turns "the server changed its story" into a visible event, and it must make the
roadmap's requirement implementable as written: "an adversarial test server substitutes a device
key mid-conversation → client surfaces a verification warning and refuses to silently encrypt to
the new key." The slice brief put three confirm-or-refute questions; each decision below answers
one, and two short calls — encoding, and the pinning default — close the file.

## Decision 1 — verification records are client-local in the wrapped keystore; the server stores none

Q1 confirmed. A verification record is the one fact in the system whose entire value is that the
server did not author it; stored where the server can write, it is worth less than nothing,
because a server that can set `verified` marks its own planted key verified and the UI then
renders the attack as safety. The question's asymmetry is real and load-bearing: the dangerous
direction is *setting* — a forged "verified" silences the only alarm — while *clearing* merely
produces more warnings. So a design may lean on stores the server can at worst delete, and must
never lean on stores the server can populate.

The middle path — server-stored but client-signed, so the server cannot forge it — fails on who
can check the signature: verifying it needs the signer's public key, which a second device can
only learn from the directory, the party under test. The reader who can check the record is the
writer who already has it, so the signed variant buys nothing client-local doesn't, at the cost
of new machinery. (The encrypted-backup slice is the one legitimate way records ever transit the
server: an opaque blob under a user-held recovery key, whose root is out of band. The server
cannot mint anything inside it; it can only delete or roll back — the warn-more direction. One
narrow rollback residue exists — resurrecting an old verified verdict about an old key set,
dangerous only if that old set's private keys are themselves compromised — and it belongs to that
slice's anti-rollback design, named here so it is not discovered there.)

Concretely: records live beside the device state in the `hamlaneh-mls` IndexedDB store, wrapped
under the same non-extractable AES-GCM key, carrying the same honest ceiling — an attacker
running as this origin reads them; a copied database does not. They are the service's data, not
the wasm module's: nothing about verification enters `src-mls` (the glue-only rule of ADR 006
survives this slice untouched) and records are not part of `export_state`. One record per
*person*, instance-global rather than per-channel — trust is about people, not rooms — holding
the user id, the full accepted key set (the set itself, not a hash alone: the UI must say *which*
device is new), a level (`pinned` — recorded by first sight; `verified` — recorded by a matching
ceremony), and a timestamp.

The cost, accepted plainly: a second sign-in has no records. A fresh browser profile starts with
every conversation unverified and pins what it first sees — the honest state of that device's
knowledge, not a bug. Syncing flags through the server is this decision's refuted branch; moving
records is the backup slice's job, under a client-held root.

## Decision 2 — the number covers a person's whole device-key set, and your own half never comes from the directory

Q2 confirmed, with a refinement that carries more security than the question asked about: *whose
knowledge feeds which half*. First the confirmation: the attack in scope is the directory adding
a key to a person, so a number computed over one device is constant under exactly the change it
exists to expose — the verified device is untouched, its number matches, and the client encrypts
to the planted leaf anyway. The number must be computed over the person's whole set of device
signature keys, so that any addition or removal moves it.

The refinement: if both sides computed both halves from the directory, both would include the
planted key, the numbers would match, and the ceremony would bless the attack. The
non-circularity rule is therefore: **a client computes its own user's half from its own accepted
own-set — the local key plus own devices the human explicitly accepted — and the peer's half from
its record of them.** The directory's only role is to be the claim under test. A planted key then
appears in Bob's computation of Alice and cannot appear in Alice's computation of herself, and
the mismatch *is* the detection.

The own-set needs one honest wrinkle handled now, because verification ships before device
linking: nothing stops a user running two browser profiles today — two legitimate devices under
one id, invisible to each other. So "your own keys" cannot mean "this device's key, full stop": a
directory listing a second key under your own id is either your other browser or an attack, and
no software on this device can tell. The answer is the same machinery pointed at yourself: a
change to your own directory set raises the loudest prompt in the slice — a new device was
registered to your account; is it yours? — and the key joins your accepted own-set only on an
explicit yes. Declining leaves your half computed over the old set, so every ceremony fails until
the planted key is gone, which is the correct outcome. Accepting an attacker's device because it
looks plausible is the residue that remains; it is social engineering rather than protocol, and
the multi-device slice's authenticated linking later narrows it.

Consequence accepted, in §2.4's register: the number is exactly as stable as the set. Every
legitimate device added or removed changes the person's half, invalidates every peer's record,
and asks the humans to compare again. Pre-multi-device that means a reinstall or a cleared
profile re-raises verification everywhere — which is also precisely the event worth warning
about, because from the outside a cleared profile and a swapped key are the same observation, and
pretending to tell them apart would be a lie. The alternative that buys back stability — a
per-person identity key that cross-signs device keys, so verification pins one durable key — is
real and deliberately deferred: it is a new key hierarchy with its own storage, recovery and
revocation design, and key transparency (post-v1) wants the same design conversation. A
per-device number would spare the churn by conceding the attack itself; that trade is refused.

## Decision 3 — refusal lives on the send path, and here is the state machine

Q3 confirmed. ADR 007 already established there is no veto at commit-apply: declining a sequenced
commit forks this device off the log and stalls it, so receipt is not consent, and the one
operation wholly this client's own is producing new ciphertext. The enforced invariant:

> This device encrypts an application message only into a tree whose every non-own leaf key is
> inside the accepted (pinned or verified) set of the member the directory attributes it to.

A leaf attributed to nobody current has no set to be inside, so it blocks — and it is exactly
what the next sweep evicts, so that branch resolves itself without asking anyone anything.
ADR 007's pre-sweep window is thereby also a non-sending window, which is the stronger true
statement the key-swap test asserts.

Machinery: reconcile — which already reads the directory whole-or-not-at-all — compares each
member's current set to their record and recomputes a per-conversation verification state, held
alongside (not inside) the channel's availability state: a channel can be `ready` and still
blocked for sending. `encrypt` re-checks the invariant locally at send time — tree keys from
`member_signature_keys` against accepted sets, no network on the send path — and refuses through
the existing null-refusal rail with a distinguishable reason, so the composer can tell "blocked
pending verification" from "unavailable". The remaining race — a commit adding a planted leaf
sits in the log, unapplied, while the user hits send — is closed by MLS itself, not by timing: the
message is sealed at the old epoch, and a joiner cannot decrypt epochs before its join (the
roadmap's own new-member test asserts this), so sending before applying the commit is not a
bypass but an encryption the new key provably cannot open.

Per-person states drive the conversation state: no record → **pin now**, silently (first sight);
record matches the current set → clear (a badge appears only at `verified`); record differs →
**changed**, and any changed member — or any uncovered leaf — puts the conversation in
needs-attention. In that state, exactly:

- **Reading works.** History renders, incoming messages still decrypt, commits still apply,
  reconcile still runs — the sweep is itself a defence and must not pause for a warning. Blocking
  read would protect nothing and would hide the context a human needs to judge the warning.
- **Sending is blocked.** `encrypt` refuses; the composer is replaced by the warning, naming the
  person and the change (new device, replaced key), offering exactly two exits.
- **Exit 1 — verify:** run the ceremony over the *current* sets; a match records them `verified`.
- **Exit 2 — accept ("I checked"):** one explicit action records the current set at `pinned`. On
  a previously verified peer this visibly downgrades the badge, because an unceremonied
  acceptance is a pin, not a proof. Both exits unblock sending; each acceptance names the set it
  accepted, so it can never generalize to the next change.
- **There is no third exit.** No per-message "send anyway", no timeout, no auto-clear — each of
  those is "silently encrypt to the new key" wearing a delay.

**Trust-on-first-use pinning is on by default.** This is not optional if the roadmap's sentence is
to be true: the warning it demands must exist for users who never ran a ceremony, and only a
pinned first sight gives "changed" a meaning. The concession, stated per §2.4: pinning trusts
first sight, so a directory that lies from the very first claim is caught only by the ceremony —
or, post-v1, by key transparency. What pinning catches is the server that changes its story,
ADR 007's "compromised later" sub-case, which is also the exact case the key-swap test exercises.

## Decision 4 — sixty decimal digits, and a QR that compares the real bytes

Per person: `half = SHA-256("hamlaneh-safety-number-v1" ‖ user id ‖ key count ‖ the signature
public keys, sorted bytewise)`, where **`‖` is the repo's length-prefixed framing (`packFrames`
in `webapp/src/mls/bytes.ts`), not plain concatenation.** That was left unstated in the first
draft and the implementation had to choose; it is written down here because the choice is
load-bearing rather than stylistic. Plain concatenation lets a different (user id, key set) pair
produce identical input bytes by moving a field boundary, and a safety number whose inputs can
collide is a safety number that can be made to match. The committed test vector pins this: if
somebody reimplements from the prose with plain concatenation, every number differs and the
vector fails, which is the intended way to find out. Display: each half rendered as 30 decimal digits (the hash taken
as a big-endian integer mod 10^30, zero-padded — deriving digits from a hash is encoding, not
cryptography), the pair shown as 60 digits in twelve five-digit groups, halves ordered by their
own bytes so both screens print the identical line. Digits rather than words or emoji: the
product is bilingual and no standardized Persian wordlist exists, digits read aloud in either
language, and numeral rendering is the i18n layer's existing concern. Same-room comparison gets a
QR carrying the version, both user ids and both full 32-byte halves, compared exactly on scan —
truncation is for eyes, not machines. The version label is what lets a future ciphersuite or a
cross-signing design change the derivation without pretending old numbers are comparable.

Two rules the implementation had to settle and this record now carries, both found by a test
rather than by argument:

- **A person with no registered device is not pinned as an empty set.** Pinning "nothing" made a
  colleague's very first sign-in — the commonest ordinary event there is — arrive as a
  changed-keys warning. They are pinned on the first reconcile that shows them a device.
- **A changed set on your own account blocks sending in that conversation**, following from "any
  changed member puts the conversation in needs-attention". The peer list filters you out, so the
  own-account prompt is what speaks for that block rather than a warning about a stranger.

## Deliberately not decided

- The ceremony's screens and copy — the design pipeline's: BRIEFS.md rows, mockups, STATUS.md;
  until a mockup lands, agents build unstyled functional plumbing per CLAUDE.md.
- Message-level sender binding — whether the leaf that signed a message matches the account the
  transport attributes it to. Same data, separate concern: this slice gates what this device
  encrypts, not how received authorship renders. Named so it becomes a slice, not a surprise.
- Record portability (the encrypted-backup slice, with its anti-rollback duty as flagged in
  Decision 1) and multi-device linking, whose authenticated own-set replaces Decision 2's
  own-account prompt with something better than a human judgment call.
- Cross-signed identity keys and key transparency — the stability story and the audit story,
  post-v1, one design conversation.

## What must happen next, in order

1. **BRIEFS.md rows** for the two surfaces — the per-person verification sheet and the
   changed-key warning that replaces the composer — so the design pipeline can run;
   STATUS.md rows `PENDING`.
2. **Opus implementation, client-only:** the keystore record slot, reconcile-computed
   verification state, the encrypt gate with its distinguishable refusal, the two exits, the
   own-account prompt, en+fa keys, RTL (digit groups are direction-neutral; test it anyway).
   There is no contract step — the first Phase 3 slice with no server half — so the freeze the
   workflow requires is satisfied vacuously and nobody waits on it.
3. **Tests in the same slice:** the roadmap's key-swap e2e (adversarial substitution → warning
   shown, send refused, accept re-pins and unblocks); units for pin/changed/accept and the
   verified-downgrade; the uncovered-leaf branch (blocks, then self-heals by sweep); a
   number-derivation vector fixed in one shared function.
4. **Fable adversarial review, read-only,** on the falsifiable question: can any sequence of
   directory answers, commits and welcomes bring this client to encrypt an application message to
   a leaf key outside every accepted set, without an explicit user action first?

## Same-PR updates this ADR requires

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (verification: client-local records,
set-based safety numbers, send-path refusal, TOFU pinning on — ADR 008). ROADMAP Phase 3: the
"Key verification UX" bullet becomes slice 3.3 with a pointer here and the key-swap test named as
its gate. The stack tables in CLAUDE.md and PLAN §4 are **unchanged** — no new component, no new
dependency, no OpenAPI change, no migration. OVERVIEW is untouched by the ADR itself; the
implementing slice adds its surfaces.
