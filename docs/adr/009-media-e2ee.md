# ADR 009 — Media E2EE: the exporter keys the call, the epoch rotates the key, and the boundary holds

**Status:** accepted · 2026-08-30 · **Scope:** Phase 3, slice 3.4

> Written against the pinned code rather than its docs: at `livekit-client` 2.22.1,
> `ExternalE2EEKeyProvider.setKey` calls `onSetEncryptionKey(derivedKey)` with **no key index**
> (`webapp/node_modules/livekit-client/src/e2ee/KeyProvider.ts`), so the stock provider holds one
> static shared key and cannot express a rotation — while the `protected`
> `BaseKeyProvider.onSetEncryptionKey(key, participant?, keyIndex?)` one layer down is exactly the
> rotation call: it fills a keyring slot and, when an index is passed, marks it current. That one
> fact decides Decision 4's shape.

> And against the wrapper: `webapp/src-mls/src/lib.rs` exposes `encrypt`, `decrypt`,
> `member_signature_keys`, `epoch` — no exporter. OpenMLS's `export_secret` sits one call below;
> surfacing it is plumbing over an audited primitive, so ADR 006's glue-only rule survives this
> slice untouched.

## Context

Everything around this slice is already decided; this ADR fills the one hole ADR 006 named and
deferred: "how the MLS exporter keys LiveKit insertable streams is its own design item inside
Phase 3." ADR 005 gives calls their shape — one stable room per conversation (`chan-<uuid>`) or
conference (`conf-<uuid>`), short-lived least-privilege join tickets, `canPublishData` present and
false, transport-level ejection as after-commit work of membership loss, with its residual windows
named. ADR 006 fixes the encryption boundary at the room kind, at birth: members-only rooms are
inside, guest-capable conferences are outside, and a client knows which it is entering from its own
join path, so no server signal can flip it. ADRs 007 and 008 supply the trust machinery this slice
inherits whole: eviction by leaf signature key against the directory allow-list, client-local
accepted-key records, and a send path that refuses rather than encrypting to an unaccepted key.

What is missing is the media key itself, and the phase gate measures the gap: test gate item 1(b) —
RTP payloads at the SFU fail to decode without the E2EE key — is **not met**, and this design must
make that drill runnable and non-vacuous. The slice brief put three confirm-or-refute questions;
each decision below answers one, and a fourth records the library mechanics in the pinned
version's own terms.

## Decision 1 — the media key is MLS exporter output; nothing distributes it, everyone derives it

Q1's conclusion confirmed, with one premise refuted, and the refutation is worth the ink because
the argument as posed would not survive its first review. The premise "any separately distributed
key needs a distribution channel, and the only channel available is the server" is false: MLS
application messages are a channel the server cannot read, so a random per-call key minted by a
member and sent over MLS would also be end-to-end secure. It is refused anyway, for what it trusts
that the message path does not: a member-run distribution protocol — who mints, who re-mints on
membership change, how a joiner catches up, how rotation orders against frames in flight — is a
second, hand-rolled key-management layer of exactly the class §6.9 bars and ADR 006 already
refused once in fragment form; it elects the minting member as a keying authority the message path
has no analogue of; and its key messages would persist in the message log as ciphertext that
outlives the call — a retention nobody asked media to have. The server-minted variant fails for
the question's own reason and faster: the party that operates the room would underwrite the
promise made about itself.

The exporter needs none of it. RFC 9420's MLS-Exporter exists precisely to key an external
protocol from group state: every member **derives the same secret independently from the epoch's
key schedule** — nothing travels, so there is no distribution to get wrong — and it changes with
the epoch, so key lifecycle is synchronized to membership by construction, which is the entire
substance of Decision 3. Parameters, fixed here so no implementation chooses: label
`hamlaneh media e2ee v1`, empty context, 32 bytes. The label is the domain separator; any future
exporter use takes a new label rather than a context convention. One new wasm export —
`exporter(group_id)`, a call into OpenMLS's `export_secret` — is the entire crypto surface of the
slice.

Scope of the key, stated because it is the promise's true shape: the exporter keys the **group**,
not the call. Every device of every channel member at the current epoch can derive it, in the call
or not — the same confidentiality scope messages have. That is honest and sufficient: a member who
is not in the call was entitled to join it and listen overtly, so a per-call participant subgroup
would be new MLS machinery defending against someone the room's own membership model does not
consider an adversary. Named in "deliberately not decided" rather than built.

## Decision 2 — DM and channel calls are inside; conferences are outside; every gate fails closed

Q2 confirmed, and there is deliberately nothing novel in it: ADR 006 decision 3 fixed the boundary
at the room kind, and this slice implements rather than revisits it. `chan-` rooms are calls
between authenticated members who all hold MLS leaves in the channel's group — the group whose
exporter Decision 1 uses. `conf-` rooms admit guests, a guest has no credential, no leaf and no
group, so there is nothing to derive an exporter *with*; that is not a policy that could soften
but a missing input. Conferences are therefore not media-E2EE in either mode, and the client
decides from its own join path — no runtime flag exists for a hostile server to flip, which is
what keeps the downgrade test passable by construction.

What the UI promises, in §2.4's register, fixed here as semantics (the pipeline owns the pixels
and the exact copy):

- **DM and channel calls:** end-to-end encrypted — the server relays media it cannot read.
  Explicitly not promised: metadata (the SFU necessarily sees who is in the room, when, for how
  long, and speaking patterns — that is transport reality, not a defect to paper over);
  protection when a member's own device is compromised; and the pre-verification directory
  residue of ADR 007, which is the same residue messages carry, closed by the same ADR 008
  machinery — literally the same records and the same per-conversation state, not a parallel copy.
- **Conferences:** not end-to-end encrypted; encrypted in transit; the server can access the
  meeting's audio and video. The unqualified word "encrypted" never appears on the conference
  join surface — presenting transport encryption as E2EE is the exact §2.4 violation this table
  exists to prevent.

Fail closed, three gates at join, because a chan- call that cannot be keyed must not happen rather
than happen in plaintext (the media form of the null-refusal rail):

1. **Room kind.** `conf-` is never keyed and never claims to be.
2. **Capability.** `isE2EESupported()` false — no insertable streams in this browser — refuses
   the chan- call join with an honest error. A plaintext fallback here is the silent downgrade
   the whole phase exists to prevent.
3. **Group state.** No ready MLS channel state (device unavailable, channel `waiting`/`failed`)
   means no join; and a conversation in needs-attention blocks publishing exactly as it blocks
   the composer — Decision 3 gives the rule its precise form.

Mode interaction, so the future slice has nothing to re-decide: media E2EE rides the group. It is
on exactly where message E2EE is on, because the key exists exactly where the group exists; when
the Strict/Compliance mode slice lands, toggling the group toggles both.

## Decision 3 — the key rotates on epoch change; a fixed per-call key concedes the removed listener

Q3 confirmed. MLS epochs advance mid-call — every membership change commits one — and the media
key must follow, because the alternative concedes precisely the property ADR 007 spent a slice
restoring for messages. With a per-call key fixed at call start, a member removed mid-call keeps
the key; transport ejection (`RemoveParticipant`, ADR 005) is the honest server's courtesy, and
under PLAN §6.1's adversary 3 the server simply declines the courtesy and keeps forwarding them
frames. **A removed member keeps hearing the call for as long as it lasts.** The same fixed key
also breaks the join side: a member added mid-call has no way to get it except a distribution
protocol, which Decision 1 just refused. Rotation on epoch change buys both directions at once
and is why the exporter was the right answer to Q1: added members derive from the epoch their add
created and nothing earlier; removed members are excluded by MLS itself from the commit that
removes them and can derive nothing later.

Mechanics, chosen so that rotation needs **no signaling at all**: the keyring slot is
`epoch mod keyringSize` (16 by default; each encrypted frame already carries its slot index in
the library's frame format). Every member derives the new exporter and fills the slot at the
moment it merges the commit — the existing merge points in `catchUp` and the `commit_accepted`
paths — and a sender's outbound slot switches when its own merge lands. Both sides compute the
same slot from the same epoch; nobody tells anybody anything. Slot collision means sixteen epochs
inside one frame's flight time, which is not a real case; `keyringSize` rises to 256 before it
ever becomes one. The three cases the question names:

- **(a) A participant who has not yet applied the commit** receives frames tagged with a slot it
  has no key for. They do not render — the shared-key provider's `failureTolerance: -1` means
  failures never invalidate the keyring, so decoding resumes the instant the key arrives — and
  the member hears silence or sees a frozen tile until its own catch-up merges the commit and
  fills the slot. In a live call every participant is connected, so the `mls_commit` frame that
  drives `syncChannel` arrives promptly; the library's decrypt-failure events double as one more
  catch-up nudge. Degraded honestly, healed automatically, seconds wide.
- **(b) Frames already in flight** were sealed at the old slot, and the old slot is still in the
  keyring — rotation fills a new slot rather than overwriting the old — so they decode normally.
  Old keys age out only when the ring index is reused, sixteen epochs later, or when the call
  ends and the provider is discarded. That retention is a bounded forward-secrecy concession —
  an old media key lingers in worker memory, never persisted, never in the keystore, covering
  seconds of ephemeral frames — accepted and named rather than engineered away.
- **(c) A member removed from the group but still connected to the room** cannot derive the new
  epoch's exporter: the removal commit's secrets exclude the removed leaf — this is MLS's own
  guarantee, the one the message path already leans on. The hostile server can keep them
  subscribed forever; what they receive after remaining senders rotate is noise. What they still
  get, stated plainly: transport metadata (that the call continues, who is connected, activity
  patterns), and any frames from a laggard sender still sealing under an epoch they held. The
  timing statement is ADR 007 decision 3's, transposed: "cannot hear after removal" means after
  each sender merges the removal commit; connected senders merge within seconds, and the honest
  server's `RemoveParticipant` runs as well — but the crypto, not the ejection, is the
  load-bearing defence.

The planted-leaf case closes the loop with ADR 008 rather than adding machinery. A leaf outside
every accepted set holds the epoch's exporter — deriving is what group members do — but the
conversation it taints is in needs-attention, and the rule that follows is the same one messages
apply, at the same place: **this device publishes media only under an epoch
whose every non-own leaf key is inside the accepted set of the member the directory attributes it
to.** The check runs before the outbound slot switches at each rotation, exactly as `encrypt`
re-checks at each send. Refusal unpublishes every local track and surfaces the same warning with
the same two exits and no third; subscribing and decrypting continue, because ADR 008's "reading
works" holds for media too — deriving a key to *listen* hands the attacker nothing, sealing your
outbound frames under it is the act that does. And the sweep that evicts the planted leaf bumps
the epoch, so the rotation that follows is what locks it out of everything after.

> **Correction, 2026-08-30.** An earlier draft of the sentence above also said needs-attention
> "blocks call join", which contradicts the rest of it three clauses later: a device that cannot
> join cannot subscribe, and subscribing is what "reading works" means for media. The
> implementation followed the argument rather than the stray clause — join and subscribe are
> allowed, publishing is blocked — and the clause is removed rather than left for somebody to
> implement the half that was never reasoned for.

## Decision 4 — LiveKit's machinery as-is, its provider minimally subclassed, its ratchet unused

The library's E2EE stack is used whole: its worker (`livekit-client/e2ee-worker`, bundled
same-origin by Vite — no CSP change), its AES-GCM frame encryption, its HKDF key-material
derivation, its keyring and frame format, its server-injected-frame handling,
`room.setE2EEEnabled(true)`. Nothing cryptographic is written on our side; "assemble, don't
reinvent" applies to the media plane as it did to the SFU itself.

The one thing the stock `ExternalE2EEKeyProvider` cannot do is the one thing this design is: as
verified above, its `setKey` takes no index — it is built for a single static passphrase, and
rotating under it would overwrite the old key mid-flight. So the slice ships a minimal subclass
of `BaseKeyProvider` — the same three options `ExternalE2EEKeyProvider` hard-codes
(`sharedKey: true`, `ratchetWindowSize: 0`, `failureTolerance: -1`) plus one method: take 32
exporter bytes, import them through the library's own HKDF material path, and call the protected
`onSetEncryptionKey(material, undefined, epoch % keyringSize)`. A wrapper in the thinnest sense:
key routing, zero key making. One provider instance per call connection, created at join,
discarded at leave, so no keyring ever spans two rooms or two groups. `keySize` stays at the
default 128 — matching the MLS ciphersuite's AES-128-GCM strength.

The ratchet story, in the library's terms: **MLS is the only ratchet.** LiveKit's own key ratchet
(`ratchetKey`, `ratchetWindowSize`, auto-ratchet on decrypt failure) is deliberately unused —
epoch-derived slots only agree because both sides compute keys from group state, and a local
ratchet advancing keys out of band would desynchronize exactly that. Shared-key mode already
disables auto-ratchet, so this is alignment with the library's defaults, not a fight against
them. The data channel needs no key because it does not exist: `canPublishData` is present and
false (ADR 005), and stays so.

## Deliberately not decided

- Per-call participant subkeys (keying the call more narrowly than the channel) — refused above
  as defending against a non-adversary; named in case a future threat model disagrees.
- Members-only conferences with E2EE — the boundary is ADR 006's to move, not this slice's.
- Screens, badges and copy — the design pipeline's: BRIEFS.md rows, mockups, STATUS.md; unstyled
  functional plumbing until mockups land, per CLAUDE.md.
- Recording and compliance-mode media handling — recording is out per ADR 005, and the mode
  slice owns its own collision with strict mode.
- The multi-tab ceiling: one MLS device per profile, last-write-wins across tabs (the roadmap's
  SharedWorker slice). Calls inherit it unchanged — the media layer only reads group state and
  derives; it commits nothing — so the ceiling is not widened, and its fix lives where it lived.
- Home-mode calls — ADR 005's open question, untouched.

## What must happen next, in order

1. **No contract step.** Client-only slice: no endpoint, no migration, no OpenAPI change — join
   tickets, room naming and the webhook are untouched. The workflow's freeze is satisfied
   vacuously, as it was for slice 3.3.
2. **BRIEFS.md rows** for the three surfaces — the encrypted-call indicator, the conference
   plain-labeling notice, and the mid-call blocked-publish warning (which reuses slice 3.3's
   warning semantics); STATUS.md rows `PENDING`.
3. **Opus implementation:** the wasm `exporter` export; the provider subclass and room wiring
   for `chan-` rooms; the three join gates; the rotation hook at the commit-merge points
   (surfaced as a per-channel epoch in published MLS state, with the call layer reacting to
   changes); the publish gate reading the existing per-conversation verification state; en+fa
   keys; RTL.
4. **Tests in the same slice:** wasm two-device exporter parity (same group, same epoch, same
   bytes; commit advances → old bytes unreachable, both devices agree on the new); unit tests
   for the gates (conf- never keyed, unsupported browser refused with the honest error,
   needs-attention unpublishes and the two exits republish); an e2e two-browser encrypted call
   over fake media devices in which a mid-call membership change rotates the key and the call
   survives it.
5. **The drill, written into `docs/drills/e2ee-drill.md`** — the file the roadmap already owes —
   in its non-vacuous form. A raw packet capture is the vacuous form: SRTP makes captured
   payloads unreadable with or without E2EE, so "the capture fails to decode" proves nothing
   about the server. The adversary sits *after* SRTP termination, so the drill takes the
   server's actual position: mint a token server-side (the operator can always do this), join
   the live `chan-` room as a bare subscriber with no MLS state, and assert the received tracks
   cannot be decoded — decrypt failures, no renderable frames. The control that keeps a clean
   result honest, in the message drill's plaintext-control tradition: the same probe against a
   `conf-` room decodes fine. That is gate 1(b)'s intent met exactly: the SFU's position cannot
   decode without the E2EE key.
6. **Fable adversarial review, read-only,** on three falsifiable questions: confirm or refute —
   (i) a member removed at epoch N, still connected to the room, can decrypt some frame sealed
   by a sender that has merged the removal commit; (ii) some path exists on which a `chan-` call
   publishes an unencrypted frame (provider failure, worker failure, unsupported browser,
   race at join included); (iii) some conference-guest path can reach the exporter or a `chan-`
   room ticket.

## Same-PR updates this ADR requires

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (media E2EE: exporter-keyed insertable
streams, epoch-driven rotation, room-kind boundary — ADR 009). ROADMAP Phase 3: the media bullet
becomes slice 3.4 with a pointer here, and the gate table's 1(b) row notes the drill's
non-vacuous form (post-SRTP probe with a conference control) so nobody runs the pcap version and
calls the gate met. ADR 006's closing line of decision 3 gains the one-line "resolved by ADR 009"
annotation, the courtesy ADR 005 received. The stack tables in CLAUDE.md and PLAN §4 are
**unchanged** — no new component, no new dependency: `livekit-client` is already pinned and its
E2EE worker ships inside it. OVERVIEW is untouched by the ADR itself; the implementing slice adds
the encrypted-calls line it earns.
