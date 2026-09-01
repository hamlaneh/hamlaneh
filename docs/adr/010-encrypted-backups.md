# ADR 010 — Encrypted backups: what survives a lost device, who holds the key, and what a lying server can still do

**Status:** accepted · 2026-08-30 · **Scope:** Phase 3, slice 3.5

> Written against the keystore rather than the report of it: the wrapping key is generated with
> `extractable: false` (`Keystore.open` in `webapp/src/mls/keystore.ts`), so a copied IndexedDB
> is ciphertext under a key that cannot leave the profile — resisting exactly that copy is the
> module's own stated ceiling. That ceiling is why this slice exists: no copy of the local store
> can ever be the backup, so a backup must be a fresh seal under a root that is not the profile
> and not the server. And the data it must carry already lives beside the device state as
> service-layer plaintext this origin can read (`saveRecords`/`loadRecords`), deliberately not
> part of `export_state` — so nothing here touches `src-mls`, and ADR 006's glue-only rule
> survives untouched.

> The slice's entire crypto surface is `crypto.subtle` natives — HKDF-SHA-256, AES-256-GCM, and
> `importKey` with `extractable: false`. No new dependency enters the client; the passphrase
> variant refused in Decision 2 is the only branch that would have needed one.

## Context

ADR 008's Decision 1 named this slice twice: as "the one legitimate way records ever transit the
server" — an opaque blob under a user-held recovery key, which the server cannot mint anything
inside and can only delete or roll back — and as the owner of one narrow residue it flagged so it
would not be rediscovered here: a rollback can resurrect an old `verified` verdict about an old
key set, dangerous exactly when that old set's private keys are compromised. PLAN §6.4 names the
work item ("encrypted backups with user-held recovery keys, and recovery UX that doesn't lock
honest users out forever"), and the phase gate measures the gap: gate item 2 — honest user loses
device, recovers with recovery key; user without a recovery key hits the documented, non-lying
failure path — is **not met**, and this design must make that drill runnable and honest. The
slice brief put three confirm-or-refute questions; each decision below answers one, and a fourth
records the format, the KDF, and the default.

## Decision 1 — the blob carries what this user knows, never the keys the user is; recovery continues conversations and does not return history

Q1's promise confirmed, its mechanism refuted. The honest promise is exactly "you keep your
conversations going, not you get your history back" — but the blob does not carry the device
signature key or any MLS group state, and restoring those was the part of the question's
mechanism that does not survive contact with the loss scenario.

The refutation, in three parts. First, the premise of recovery is that the old device is out of
the user's control, and "lost" is not "destroyed": whoever holds it holds its signature key, so
the honest move is to treat that key as compromised — revoke it and let ADR 007's sweep evict its
leaf — not to resurrect it onto a new device, which would keep a possibly-stolen key inside the
user's own accepted set and inside every peer's pinned set. ADR 008 already accepted the
consequence and this ADR inherits it rather than engineering around it: a recovered user has a
new key, every peer sees "changed", and that warning is the truth — from the outside, a recovery
and a theft are the same observation, and device loss is precisely the event peers deserve a
warning about. Second, MLS state is a live ratchet, not a document: a snapshot restored while the
group moved on — or while the lost device still runs — is a forked leaf, and resuming it corrupts
the one property the epoch machinery exists to keep. Third, membership needs no backup at all:
who is in which channel is the transport authority (ADR 006), stored overtly server-side, and the
ordinary enroll-and-reconcile path already re-adds a listed device to every group — recovery of
membership is a re-join, by construction, not a restore.

What cannot be recovered by anyone, stated as crypto rather than policy: forward secrecy means
used message keys are deleted, and epoch advance deletes the secrets behind it. A backup cannot
contain what no longer exists anywhere — past plaintext is not withheld from the recovered user;
it is absent from the universe of keys. The only honest route to history is a backup of the local
**plaintext history store**, which is its own roadmap slice ("what you said is as sensitive as
what you received") and, once it exists, becomes a second section of this same blob rather than a
second backup system. Until then, no UI string may imply history returns.

So the v1 blob carries the data that is irreplaceable and nothing that is reconstructible: the
**verification records** — the user's trust decisions, the one fact in the system whose entire
value is that the server did not author it — plus the anti-rollback counter and a sealed
creation date (Decision 3). Restoring a record restores the verdict it holds: a peer whose key
set is unchanged comes back `verified`, because the ceremony's verdict was about *their* set,
which did not change — while a peer whose set changed during the absence raises "changed" instead
of being silently re-pinned as first sight. That last clause is the security content of the whole
slice: TOFU's weakness is first sight, and a server that waited for a device loss to swap a
peer's key gets caught only by a device that remembers. One row is handled specially: the user's
**own** record is not restored as-is — the new device's accepted own-set starts as exactly its
own fresh key, so every other key still listed under the user's id (the lost device's included)
raises ADR 008's own-account prompt, where the recovery flow's answer is revocation.

A user who lost their only device therefore gets: their account (server authentication is
password/SSO and never gated on this slice), their channels and roster, a fresh device re-added
to every conversation by the ordinary path — conversations continue from the re-join epoch — and
their trust records, warning exactly where warnings are due. They do not get: any message from
before the re-join epoch (the roadmap's own new-member rule), any plaintext whose keys forward
secrecy already deleted, the old device's identity, or their peers' verdicts about them — those
live on the peers' devices, correctly reset to "changed", and are re-earned, not restored.

## Decision 2 — the recovery key is generated, full-entropy, shown once, user-held; no reset, no escrow; losing it costs the blob, never the account

Q2 confirmed on every clause, with one strengthening and one refusal the question did not ask
for. Confirmed: the key is generated client-side, displayed once, and never sent to the server in
any form that lets the server derive the backup key — the server stores the sealed envelope and
nothing else — and there is no server-side reset, because a server that can reset the key can
read the blob, which is the entire thing this defends. The strengthening: the recovery key is
never *persisted client-side* either. The ceremony derives the backup key once (Decision 4's
KDF), imports it as a non-extractable AES-GCM `CryptoKey` stored beside the wrapping key — same
store, same honest ceiling — and discards the string. Ongoing backups use the handle; only
restore on a fresh device ever asks for the key again.

The refusal: no passphrase option. The server holds the ciphertext and PLAN §6.1's adversary 3
*is* the server, so the blob must survive an unlimited offline attack, and a user-chosen
passphrase does not. WebCrypto's only native password KDF is PBKDF2, which is not memory-hard;
an honest passphrase mode would need argon2id (the bar §6.3 already sets for passwords) and
therefore a wasm dependency — machinery bought to make a weaker root available. A generated
256-bit key needs no stretching at all, which is why the KDF in Decision 4 is plain HKDF: against
full entropy there is no brute force to slow down, only domain separation to get right.

Two loss cases, in §2.4's register. Lost the recovery key, still have the device: no loss —
the ceremony re-runs, a new key is shown, the blob is resealed and re-uploaded, the old key is
dead. Lost both: **bounded loss, not lockout.** The recovery key gates the blob's contents —
trust records now, history later — and deliberately nothing else: not the account, not channel
membership, not the ability to send and receive from re-enrollment onward. That bounding is how
§6.4's "recovery UX that doesn't lock honest users out forever" is satisfied honestly — not by a
backdoor, but by keeping the thing the key protects small enough that losing it is survivable.
The documented failure path says exactly this and does not lie: "Your backup cannot be opened
without your recovery key. Hamlaneh does not have a copy — that is what makes it private — and
neither we nor your admin can recover it. Your account and conversations continue; your recorded
trust decisions are gone, so you will be asked to verify people again." No support escalation
changes this, and the docs say so.

Org-level recovery, which the roadmap told this ADR to decide: **it does not exist in Strict
mode, and the org-level recovery policy already has a name — Compliance mode.** An org that can
recover a backup can read it; escrow inside Strict mode is therefore not a feature toggle but the
mode boundary redrawn, and §6.4 already ruled on pretending the two coexist. An organization
whose obligations require guaranteed recovery of content chooses Compliance mode at setup,
labeled as what it is; the mode slice owns that half. In Strict mode the admin recovers nothing,
and no Strict-mode surface may imply otherwise.

## Decision 3 — anti-rollback: a sealed counter against a local floor, and the first restore named as the gap it is

Q3 confirmed as necessary, refuted as sufficient — and the insufficiency is structural, not an
implementation gap. The counter: every upload increments a monotonic counter carried in the
envelope's authenticated header (AAD, so GCM's tag covers it and the server cannot alter or strip
it without the decrypt failing). The floor: the client persists the highest counter it has ever
written or accepted, beside the records, under the same wrapping key. Any blob whose counter is
below the floor is refused as a rollback, loudly. This closes the ongoing-device cases: a stale
read cannot make a device build tomorrow's backup on last month's records (a lost-update), and a
device that has once seen counter N never accepts N−1 again.

What it does not stop, exactly as the question predicted: **the first restore.** The recovery
scenario is "the local device is gone", and gone devices take the floor with them — a fresh
install has nothing to compare against, so a server serving the oldest blob it ever stored
presents a counter that looks as valid as any. Under this trust model that gap is irreducible: a
fresh device holding only a recovery key has exactly two channels to freshness, the server (the
party under test) and the human. So the design uses the human, cheaply and honestly: the payload
carries a sealed creation date, and the restore screen shows it as a fact to confirm — "this
backup is from March 3" — because the user roughly knows when they last used the app, and a
sealed date is one thing the server cannot forge forward. That is a speed bump, not a wall, and
is documented as such.

The residue that remains is precisely the one ADR 008 flagged here, now bounded: serving an old
blob to a fresh restore resurrects old verdicts, which is dangerous only when the old verdict's
key set contains a compromised private key *and* the directory re-lists that key — in every other
case a stale record differs from the current directory and produces "changed", the warn-more
direction. And the dangerous case is not silent at the other end: re-listing an old key under a
peer's id trips that peer's own own-account prompt (ADR 008 Decision 2), so the attack's quiet
half has a loud half a human can see. Two more server moves, named so nobody discovers them:
deletion — "no backup exists" to a user who knows they made one is indistinguishable from
never-backed-up on the wire, so the restore surface states the server's answer plainly and treats
a missing expected backup as worth suspicion, but this is DoS-class, the warn-more direction
ADR 008 already priced; and a split view — acknowledging writes to the live device while serving
an old blob to the eventual restorer is the rollback case again, unobservable to the writer, the
same residue not a new one. Two browser profiles under one account (pre-multi-device reality)
race the counter last-writer-wins; the server's non-increasing reject (Decision 4) turns the race
into a visible error rather than silent interleaving, and the real fix is the multi-device
slice's, named there.

## Decision 4 — format, KDF, and the default: one sealed envelope, HKDF, offered at enrollment rather than silently on

**The envelope**, fixed so no implementation chooses: `header ‖ iv ‖ ciphertext`, where `header`
is the magic `HMLB`, a format version byte (1), and `packFrames(counter as 8-byte big-endian,
user id UTF-8)` — the repo's length-prefixed framing (ADR 008's lesson: framing is load-bearing,
not stylistic). The header is the AAD, verbatim; `iv` is 12 fresh random bytes per seal;
the cipher is AES-256-GCM. The plaintext is UTF-8 JSON with named sections:
`{ "v": 1, "createdAt": …, "verificationRecords": … }` — the history slice later adds a section
key rather than a second system (its size will force its own contract revision; this one sets a
small cap, since records are kilobytes and the endpoint must not be an unmetered blob store).
The counter lives in the header only: GCM authenticates AAD, so after a successful decrypt it is
as trustworthy as the payload, readable by the server for its convenience check, and stated once.

**The key path:** recovery key = 32 bytes from `crypto.getRandomValues`, displayed as Crockford
base32 (52 characters in groups of four, ambiguous letters excluded) plus a four-character
checksum — the first two bytes of SHA-256(`"hamlaneh recovery key v1"` ‖ key), so a typo fails
client-side before any network call and the domain label doubles as the version marker. Backup
key = HKDF-SHA-256(IKM = recovery key, salt = empty, info = `"hamlaneh backup seal v1"`),
imported non-extractable for AES-GCM. Wrong key at restore is caught by the GCM tag; there is
nothing else to check against, which is correct — a key-check value stored server-side would be
an offline-attack oracle against a root that, being full-entropy, does not need one anyway.

**The server's half:** store one envelope per user, replacing on upload, rejecting an upload
whose header counter does not exceed the stored one — a convenience rail against stale tabs and
racing profiles, honestly labeled: a hostile server ignores it, and the security rail is the
client-side floor. Owner-only in the authz matrix: no other member, no admin, nobody reads or
writes another user's blob — it is ciphertext, but least privilege is not a function of what the
bytes reveal. Upload rate limit and size cap at the contract step.

**The default: offered, never silent.** A backup sealed under a key the user never saw is
garbage by construction — unrestorable — and storing the key for them recreates the server-reset
hole, so "on by default" is not available honestly. Instead the ceremony is **offered at E2EE
enrollment** (first MLS use on the account), one screen: what it saves (your trust decisions;
message history when that ships — and not your messages today, said plainly), that the key is
shown once and nobody — not Hamlaneh, not the admin — can recover it, and what losing it means.
Completing it turns backup on; declining is recorded and respected, re-surfaced passively in
settings and by a quiet indicator, never as a nag. After that, upkeep is invisible: the client
re-seals and re-uploads (write-behind) whenever records change, bumping the counter; turning
backup off deletes the server blob and the local backup-key handle, and says what that means.
Restore runs only into an empty record set — a device that already holds records refuses to
restore over them, which sidesteps merge semantics v1 rather than inventing them. Copy is the
design pipeline's; these semantics are fixed here.

## Deliberately not decided

- The history section — blocked on the own-message-history slice; its size forces chunking and
  its own contract revision, designed when its producer exists.
- Asymmetric sealing (encrypt-to-public-key, so writes need no resident symmetric handle) —
  refused for v1: WebCrypto has no HPKE, hand-assembling ECDH+GCM is the §6.9-adjacent move, and
  the threat it would narrow — live-origin compromise at write time — is already the keystore's
  stated non-goal. Named in case a future audit disagrees.
- Passphrase-derived backup keys — refused in Decision 2 with reasons; named so the request
  arrives at the reasons rather than at a re-litigation.
- Blob-size padding — envelope size and update timing leak record-count growth, the same
  metadata class §6.1 already concedes for transport; pad-to-bucket is cheap if an audit ever
  prices the leak higher.
- Cross-device floor gossip and record merge — the multi-device slice's, which inherits a format
  that already carries a counter to gossip about.
- Exact endpoint paths, caps, rate limits, and whether lost-device deregistration requires
  step-up re-auth — the contract step's, with `/security-review` mandatory (new endpoints, and
  deregistration is an authz-sensitive write).
- Screens and copy — the pipeline's: BRIEFS.md rows, mockups, STATUS.md; unstyled functional
  plumbing until mockups land, per CLAUDE.md.
- Compliance-mode interaction — the mode slice owns whether its rooms carry client keystores at
  all; nothing here presumes either answer.

## What must happen next, in order

1. **Contract step — required, unlike slices 3.3 and 3.4.** This slice has a server half: a
   `mls_backups` migration (one row per user: envelope, mirrored counter, updated-at) and new
   owner-only endpoints — upload, fetch, delete of the backup envelope, plus lost-device
   deregistration (the directory write whose absence would leave a stolen device's leaf
   allow-listed forever; ADR 007's sweep already evicts anything the directory drops). Every one
   registers in the authz matrix; `/security-review` is triggered by definition. **The
   orchestrator must freeze `docs/api/openapi.yaml` and the migration before spawning
   implementation agents.**
2. **BRIEFS.md rows** for the four surfaces — the enrollment offer, the show-once key ceremony,
   the restore screen (key entry, sealed-date confirmation, the missing-backup statement), and
   the no-key failure path; STATUS.md rows `PENDING`.
3. **Opus implementation, both halves:** server storage + endpoints with the counter reject;
   client ceremony, HKDF/seal/unseal, write-behind upload, floor persistence beside the records,
   restore-into-empty flow with the own-row reset and the revocation prompt; en+fa keys; RTL.
4. **Tests in the same slice:** unit vectors for the envelope and KDF (fixed in one shared
   function, ADR 008's vector discipline); the floor refusing a lower counter; the checksum
   catching typos; restore mapping unchanged→`verified`, changed→warning, own-row→fresh; and the
   gate drill as e2e — two browser contexts: back up, destroy the profile, recover with the key
   typed from the shown string, assert records and continued conversation; then the same loss
   with no key, asserting the documented failure copy, a usable account, and everything re-pinned.
   That makes gate item 2 runnable as written.
5. **Fable adversarial review, read-only,** on three falsifiable questions: confirm or refute —
   (i) a server holding every envelope ever uploaded plus the directory can bring a restored
   client to encrypt to a key outside its latest backup's accepted sets without a
   changed-or-own-account warning first (is the rollback residue really bounded to the
   compromised-old-key case); (ii) some path stores or transmits the recovery key, or anything
   the server could derive the backup key from; (iii) any sequence of server answers to the
   backup endpoints can make an *ongoing* device accept a counter below its floor or silently
   lose a record write.

## Same-PR updates this ADR requires

Per CLAUDE.md "changing a decision", once accepted: a PLAN.md §12 row (encrypted backups:
knowledge-not-keys blob under a generated user-held recovery key, HKDF+AES-GCM envelope, sealed
counter with local floor, no reset and no Strict-mode escrow — ADR 010). ROADMAP Phase 3: the
backups bullet becomes slice 3.5 with a pointer here, and the gate table's row 2 notes the
drill's two-half e2e form so the gate is run as designed. ADR 008 Decision 1's parenthetical
gains the one-line "resolved by ADR 010" annotation, the courtesy ADR 009 paid ADR 006. The
stack tables in CLAUDE.md and PLAN §4 are **unchanged** — no new component, no new dependency:
the crypto is WebCrypto, the storage is one Postgres table. OVERVIEW is untouched by the ADR
itself; the implementing slice adds the recovery surfaces it earns.
