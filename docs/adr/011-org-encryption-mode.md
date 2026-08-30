# ADR 011 — Org encryption mode: the mode governs births, a switch touches nothing, and Compliance waits for its substance

**Status:** accepted · 2026-08-30 · **Scope:** Phase 3, the Strict/Compliance mode slice

## Context

PLAN §6.4 resolves the E2EE ↔ compliance tension by refusing to fake it: strict E2EE makes
server-side search, retention and compliance export impossible *by design*, so each organization
chooses a mode — **Strict E2EE** or **Compliance** — clearly labeled, because "pretending both can
coexist simultaneously destroys credibility with anyone technical." The roadmap carries this as
two adjacent bullets — the per-org choice, and the compliance server-side half (encryption at
rest, retention policy, compliance export, promised free by §7) with the parenthetical that a
mode toggle without them is dishonest — and phase gate item 3 demands the choice be
irreversible-safe: switching modes can't silently decrypt or expose history.

Everything this mode would govern already has a fixed shape. `channels.e2ee` is per conversation,
chosen at creation, immutable — migration 0017's comment calls flipping it on a live conversation
"the silent mode-switch the downgrade test forbids," and the write path enforces the boundary in
both directions. The contract already predicts this slice in so many words: `CreateChannelRequest.e2ee`
is "opt-in per channel until the per-org mode slice lands, which will set the default and, in
strict mode, forbid creating it false"; `OpenDirectMessageRequest.e2ee` says the mode slice "will
make the default uniform and remove the ambiguity" of get-or-create races. ADR 006 fixed
conferences outside E2EE in either mode; ADR 009 fixed media to ride the group — on exactly where
message E2EE is on — and deferred one collision here. The slice brief put three
confirm-or-refute questions; each decision below answers one.

## Decision 1 — the mode sets what conversations are born as; the per-channel flag stays the truth

Q1 confirmed: the org mode sets the default and the allowed range of the existing `e2ee` flag at
creation time, and replaces nothing. **Strict:** every new channel and DM is created `e2ee=true`;
a request carrying an explicit `false` is refused with a named error. **Compliance:** every new
conversation is `e2ee=false`; an explicit `true` is refused the same way. The flag's per-channel
immutability is untouched, because that immutability is what makes gate 3 true by construction:
no conversation ever changes its encryption state, so there is no code path a mode switch could
ride to decrypt or expose anything. The mode is the policy for births; the flag is the birth
certificate.

The alternative — an org-level switch that *replaces* the per-channel flag as the live source of
truth — is refuted by what it would have to do to history. Flipping an org to Compliance under
that design means decrypting existing conversations, which the server cannot do (it is MLS-blind
by ADR 006 and holds no keys — that incapacity is the entire phase); having clients mass-decrypt
and re-upload is a hand-rolled migration protocol over everyone's history, exactly the silent
mode-switch the downgrade test exists to forbid, with §6.9's no-hand-rolled-machinery bar on top.
Flipping to Strict is the same protocol in reverse. Nothing replaces per-channel immutability;
it is load-bearing.

Two sub-choices, fixed here so the contract can freeze:

- **Refusal, not coercion.** A strict server could silently create the channel `e2ee=true` when
  asked for `false`. It must not: the explicit mismatch means the client's view of the org mode
  is stale at the exact moment it matters — fixing an immutable property — and silently handing a
  user the opposite of what their screen said is how an immutable surprise is manufactured. The
  request field therefore survives as an intent assertion: omitted means "the mode's value,"
  matching is fine, mismatching is a 4xx that teaches the client the real mode. The static
  `default: false` in the schema goes; the default is the mode's.
- **Compliance's range is `{false}`, not "false by default."** A compliance regime any member can
  opt a conversation out of is not a compliance regime; "retention and export cover everything
  except the conversations you can't see" is precisely the have-both illusion §6.4 says destroys
  credibility. DMs are the first thing a real retention obligation covers, so they get no carve-out
  either. An org that wants compliance-with-privileged-exceptions is asking for a policy that does
  not exist yet — named in deliberately-not-decided, designed when a real org asks, per the
  precedent of ADR 006's conference-link policy.

Conferences are unchanged in either mode: outside E2EE, plainly labeled, per ADR 006 decision 3.
Media needs no new rule: ADR 009 already keys it off the group, so it is encrypted exactly where
the channel is, and since no channel's flag ever changes, no call changes character mid-flight.

## Decision 2 — switching modes touches no existing conversation, and says so in numbers

Q2 confirmed. A mode switch changes the rule for future births and nothing else: no data, no
flag, no write-path behavior of any existing conversation. Strict→Compliance cannot decrypt what
exists — nobody but the members can, which is the property the phase was built to create, not a
limitation of this slice. Compliance→Strict cannot retroactively protect what was stored
plaintext — the server already read it, and no cryptography applied after the fact un-reads it;
wrapping old plaintext in new encryption would be theater performed by the party the encryption
is supposed to exclude. The honest design leaves both alone and says so where the admin is
standing.

The freeze alternative — a switch makes mismatched conversations read-only so the new mode's
promise holds for all new content — is refuted on its cheap half by its expensive half.
Strict→Compliance freezing has a case (encrypted channels keep accruing unexportable content),
but Compliance→Strict freezing would lock every pre-switch conversation in the org read-only —
and since every instance upgraded from Phase 2 is wall-to-wall plaintext, that is "adopting
Strict bricks your whole workspace." An asymmetric freeze is a worse rule to explain than the
uniform one. So: conversations outside the current mode remain fully usable, individually
labeled as what they are (the existing per-conversation encryption indicator), permanently
counted on the settings screen, and retired by their owners when the org wants them gone. If a
real compliance customer needs enforcement, that is the same future policy as Decision 1's
carve-out question — designed then, not smuggled in now.

What the admin must be told at the moment of switching, in §2.4's register — semantics fixed
here, pixels and exact copy the design pipeline's (ADR 009's convention):

- **Strict→Compliance:** "Nothing already encrypted becomes readable — this server holds no keys
  and cannot decrypt the N existing end-to-end-encrypted conversations. They stay encrypted and
  permanently outside search, retention and export. Compliance coverage begins with conversations
  created after this switch. A complete export of the past is impossible — that impossibility is
  what end-to-end encryption is."
- **Compliance→Strict:** "Nothing already stored becomes end-to-end encrypted — the M existing
  conversations were stored server-readable and their history remains so. End-to-end encryption
  begins with conversations created after this switch."

Both dialogs show live counts before confirming — the house pattern `accounts_without_totp`
established: report the blast radius before the change, not from support requests after. The
switch is audited (`org.encryption_mode_changed`). And it does not travel through the
field-by-field settings PATCH: `updateOrgSettings` is documented as "saved immediately — no Save
button," which is the wrong home for the one org setting that needs a ceremony. The mode is read
in `OrgSettings` but written only through a dedicated endpoint, so the generic settings write
cannot flip it in passing; as a new endpoint it registers in the authz matrix and triggers
`/security-review` per CLAUDE.md.

This is what makes gate 3 testable rather than asserted. Structurally: no code path mutates
`channels.e2ee` or message rows on a mode write — the endpoint touches one `org_settings` column.
Drill form: canary in an e2ee conversation and a plaintext control, switch the mode, dump the
database — the canary appears only as ciphertext, the control proves the scan sees plaintext,
every channel's flag and every message row is byte-identical, both directions. This slice
automates every leg that exists; the leg that begins from a selectable Compliance runs the day
Decision 3's gate lifts, and the gate table should say so rather than claim the row met.

## Decision 3 — Compliance is defined now, selectable only when its substance exists

Q3: of the two arms, this slice ships **Strict-only, with Compliance named and refused as
not-yet-available** — it does not build the compliance trio, and it does not let the mode be
selected without it. An org that selected Compliance today would receive exactly one behavioral
change: e2ee becomes unavailable. All cost, none of the promised substance — no encryption at
rest, no retention, no export. That is the dishonest toggle the roadmap bullet names, wearing a
settings screen. Refusing to build the trio *here* is the other half of the same judgment: three
server-side features, none of them MLS-related, each with real design weight of its own (at-rest
key custody alone is ADR-shaped: where a key lives that the database it protects cannot contain),
do not belong inside the E2EE phase's mode slice — the roadmap already holds them as their own
bullet, and this decision makes the dependency direction explicit: **that bullet is what unlocks
Compliance selectability.**

Concretely: the contract defines `encryption_mode` with both values from day one — the field's
shape is final and does not churn when the trio lands — and the server refuses
`mode=compliance` with a named error until the unlock. The admin screen shows Compliance
disabled with the blunt truth: not yet available, because retention, export and encryption at
rest are not built yet. The refusal is itself test-asserted, so the honest gate cannot rot into
a reachable lie.

Defaults, both populations: **fresh installs and migrated instances are Strict.** Secure by
default is the house rule and Strict is this product's secure posture; with one selectable mode
there is no setup question to ask, so the five-minute install stays zero-config, and the real
choice surfaces at setup and in admin settings when Compliance unlocks. For migrated instances
this ends the slice-3.1 opt-in era — an explicit `e2ee=false` creation that used to succeed now
gets a refusal — which is a visible, labeled behavior change on a pre-release product with one
dogfood instance, accepted. The alternative — a nullable "unset" mode preserving today's
opt-in free-for-all — is refused: it is a third behavior state living in the write path forever,
for zero current users, and §6.4 named two modes, not three. Existing plaintext conversations on
a migrated instance are Decision 2's case, present from day one: untouched, usable, labeled, and
counted on the settings screen ("Strict mode; N conversations predate it and are not end-to-end
encrypted").

## Deliberately not decided

- **The compliance trio's own designs** — at-rest key custody, retention semantics (what
  deletion means for files, pins, exports), export format and its authz — belong to the slice
  that builds them, almost certainly under its own ADR. This ADR fixes only their role as
  Compliance's unlock condition.
- **Compliance-org e2ee exceptions** (a privileged-DM policy) and **forced migration or freezing
  of conversations outside the current mode** — both are the same future "org encryption policy"
  surface; designed when a real org asks, per ADR 006's conference-link precedent.
- **Conference links disabled under Strict** — ADR 006's named-not-built item, still not built.
- **Recording × modes** — recording does not exist (out per ADR 005). When it is ever proposed:
  strict `chan-` recording is impossible by construction (the server cannot decode what it would
  record), and compliance recording is a compliance feature; decided then. This closes ADR 009's
  deferred "the mode slice owns its own collision" note without opening the feature.
- **Screens, badges and copy** — the design pipeline's: BRIEFS.md rows, mockups, STATUS.md;
  unstyled functional plumbing until mockups land, per CLAUDE.md.

## What must happen next, in order

1. **Contract change — yes.** The orchestrator freezes: `OrgSettings` gains `encryption_mode`
   (enum `strict`/`compliance`) and a read-only `conversations_outside_mode` count (the dialogs
   and the permanent settings note read it); a dedicated `PUT /api/v1/admin/org/encryption-mode`
   (admin-only; **new endpoint → authz matrix entry + `/security-review`**), with the mode
   deliberately absent from `UpdateOrgSettingsRequest`; `CreateChannelRequest.e2ee` and
   `OpenDirectMessageRequest.e2ee` lose `default: false` and gain the mode-derived default plus
   refusal semantics; named error codes for the three refusals (strict refuses plaintext
   creation, compliance refuses e2ee creation, compliance refuses selection while locked).
2. **Migration — yes, one, additive:** `0018` (next number free at implementation time) adds
   `encryption_mode text NOT NULL DEFAULT 'strict' CHECK (encryption_mode IN ('strict',
   'compliance'))` to the one-row `org_settings` table. **No change to `channels` or `messages`**
   — that nothing touches them is Decision 2's point.
3. **Opus implementation:** the creation-rule check at the existing channel/DM creation choke
   points (the compliance branch lands too, reached in tests by setting the column directly —
   the API cannot reach it until unlock, and the write path must already be correct the day it
   can); the mode endpoint with audit entry; the settings screen with the disabled Compliance
   option, counts and switch dialogs; en+fa keys; RTL.
4. **Tests in the same slice:** authz matrix rows for the new endpoint; table-driven creation
   tests (both modes × channel/DM × omitted/matching/mismatching flag, including the DM
   get-or-create reopen returning the existing conversation as-is); the gate-3 integration
   drill from Decision 2 (mode write, then flags, rows and a dump-scanned canary byte-identical);
   the compliance-selection refusal; an e2e pass: strict org refuses a plaintext creation with
   the honest error and the settings screen shows the disclosure.
5. **Fable adversarial review, read-only,** on three falsifiable questions: confirm or refute —
   (i) some sequence of mode operations changes any `channels.e2ee` value or makes any stored
   ciphertext readable; (ii) some creation path (channel, DM, get-or-create race, stale-client
   retry) lands a conversation whose flag violates the org mode at its creation instant;
   (iii) some caller can set the mode without admin authz, or through `updateOrgSettings`, or
   can select Compliance before the unlock.

## Same-PR updates this ADR requires (on acceptance)

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (org encryption mode: governs creation
only, switch touches nothing existing, Compliance gated on its server-side substance — ADR 011).
ROADMAP Phase 3: the mode-choice bullet becomes this slice with a pointer here; the
compliance-half bullet gains "unlocks Compliance selectability (ADR 011 decision 3)"; gate
table row 3 notes the structural form is met by this design and the full switch drill activates
at unlock, so nobody marks the row met early or vacuously. The stack tables in CLAUDE.md and
PLAN §4 are **unchanged** — no new component, no new dependency. OVERVIEW is untouched by the
ADR itself; the implementing slice adds the mode line it earns. ADR 009's "recording and
compliance-mode media handling" deferral gains the one-line "resolved by ADR 011" annotation,
the courtesy ADR 006 received from 009.
