# ADR 014 — Mentions under E2EE: the sender declares the routing, the server keeps the badge, and the trade is named

**Status:** proposed · 2026-08-31 · **Scope:** Phase 3, mentions-under-e2ee slice

> Written against the queries rather than a diagram of them. The gap:
> `server/internal/storage/messages.go` derives mention rows with `ParseMentions(nm.Content)`,
> and on an e2ee channel `content` is `""` by contract, so no `message_mentions` row is ever
> written. The consumer: `callerJoins` in `storage/channels.go` computes the sidebar's
> `mention_count` from those rows in the same pass as `unread_count` — the badge is a server
> artifact, not a client one. And the shape of the fix is already in the SQL: both
> `insertMessageQuery` and `updateMessageQuery` take the mentioned ids as a *parsed array
> parameter*, joined against `channel_members` (strangers dropped, duplicates collapsed) —
> the statements do not care whether the array came from a parser or a request field. The
> mention wire format is already token-based end to end (`<@{user_id}>` in content per the
> contract; the composer inserts ids, the renderer draws names), so both clients of an
> encrypted message already see the mention — only the notification is missing.

## Context

In an encrypted conversation the mention experience half-works, which is worse than not
working: the composer offers the picker, the token rides inside the ciphertext, the recipient's
renderer draws `@Name` after decrypting — and the sidebar badge, the one artifact that reaches
a person who is *not looking at the channel*, never fires, because the server derives mentions
from a content column that is empty by design. Under ADR 011 every new conversation is
encrypted, so this is not an edge case: mentions silently stopped notifying on every fresh
install.

The server cannot parse what it cannot read, so someone must tell it — or nobody must, and the
notification must move to the clients. The slice brief put three confirm-or-refute questions:
what the server actually learns from a client-declared mention list, whether notification can
move entirely client-side without permanent misses, and whether a third option beats both.
Each decision below answers one. The boundary this ADR must not cross: the server stays
MLS-blind (ADR 006) — whatever it is told, it must never need to verify against plaintext,
because it has none.

## Decision 1 — the envelope declares whom to notify; what the server learns is real, bounded, and accepted with its name on it

Q1 answered by enumeration rather than instinct. What the server already knows about an
encrypted message: who sent it, into which channel, when, at what ciphertext size, under which
declared epoch (the envelope's routing hint); who the channel's members are; and — via
`channel_read_positions` — who has read up to where, per member, per channel. For calls it
already concedes who is in the room and speaking patterns (ADR 009's stated metadata reality).
What a declared mention list adds: **a directed edge — this message names members X and Y** —
per message, at send time. That is the first *content-derived* fact a client hands the server
about an encrypted message, and this ADR treats that crossing as the decision it is, not as a
detail.

Measured against what is already conceded, the increment is real but modest: it is
membership-bounded (the ids are channel members or they are dropped), sender-controlled (the
sender always chose whom to ping — what moves is only whether the server sees the routing, not
whether the ping happens), and it carries no text, no topic, no position in the message. In a
DM it adds nearly nothing — there is one possible addressee. In a channel it enriches the
interaction graph beyond membership and read cursors: who addresses whom, when. A server that
wanted this signal could partially reconstruct it today from timing and read patterns; with the
declaration it gets it exactly. That is the leak, stated without discount.

It is accepted because the alternative pays more (Decision 2) and the smaller-leak variants
buy nothing against the adversary that matters (Decision 3) — and it is accepted *visibly*:
the E2EE threat-model documentation (the same §6.1-register list that names timing, sizes and
call metadata) gains the line "which members an encrypted message mentions, as declared by the
sender". Not free, and never presented as free.

Trust framing, fixed so no implementer re-derives it: the declared list is **advisory
notification routing, not truth about content**. The server cannot check it against the
ciphertext and must not pretend to; recipients never render from it (the field is write-only —
rendering comes from the decrypted tokens, exactly as today). A lying sender can ring a member
without a visible `@` or write a visible `@` that never rings — both are attributable
annoyance by someone already inside the conversation, the class of nuisance a member can
already commit by other means, not a security property. The one guarantee the plaintext path
had that this one keeps: no mention row can name someone outside the channel, because the same
`channel_members` join enforces it in the same statement.

## Decision 2 — notification cannot move entirely client-side; the permanent-miss case is real

Q2 refuted in its hopeful half, and the refutation is the reading list's own fact. What
already works client-side, with no server involvement: the *live* experience — a connected
device decrypts the incoming frame, sees its own token, and can toast, flash and highlight
locally. If live notification were the whole problem, no contract change would be needed.

Three things break, in ascending order of severity:

1. **The badge stops being cheap.** `mention_count` is computed server-side in one indexed
   pass beside `unread_count`, for every channel in the sidebar at once. A client-side
   equivalent must fetch and decrypt **every unread message in every encrypted channel** at
   session start just to decide whether to draw a red dot — a cost that grows with exactly the
   absence the badge exists to summarize, paid on every device, worst on the future mobile
   client where wake-ups are the budget.
2. **Anything that must reach a user with no running client becomes impossible.** Push and
   email digests do not exist yet, but the server can only ever wake the right person if it
   knows whom to wake; a purely client-side design forecloses that path permanently, and
   forecloses it silently.
3. **The permanent miss, exactly as the question suspected.** A mention lives inside the
   ciphertext, and ciphertext has a readable lifetime: a message from before a device joined,
   or from an epoch whose secrets have been dropped, "cannot be opened by anyone holding this
   state" (the wrapper's own `decrypt` contract). A member offline past that horizon — or
   recovering onto a fresh device per ADR 010, which restores trust records and deliberately
   not history — loses the mention *with* the words, forever, and never learns they were
   pinged. With server-side rows the badge outlives the ciphertext's decryptability: "you were
   mentioned here" remains true, deliverable and actionable even when the message itself can no
   longer be rendered — which is precisely the degraded-but-honest state the rest of Phase 3
   renders for undecryptable history.

So client-side-only fails the brief's own test: an offline device does miss mentions
permanently, in the dropped-secrets and fresh-device cases, and degrades every other case from
one indexed query to a full backlog decrypt.

## Decision 3 — the third options leak the same or lose more; one retention variant is named, priced, and not built

Q3: examined and refused, each for a stated reason rather than in bulk.

- **Sealed per-recipient hints** — the sender writes a server-stored, server-unreadable "you
  were mentioned" note addressed to each mentioned member. The server cannot read the note but
  must route it, and *the addressing is the same directed edge* Decision 1 concedes — identical
  leak, purchased with a second delivery pipeline, per-recipient sealing machinery the message
  path does not otherwise need, and a fan-out write amplification. Refused: same leak, more
  protocol, §6.9-adjacent.
- **Decoy padding** — always declare a fixed-size list, padding with random non-member ids the
  join will drop. The rows that survive are exactly the real mentions, so the at-rest record is
  unchanged; the only thing hidden is the list length on a TLS-protected wire the server
  terminates anyway. Refused: hides nothing from the party the design distrusts.
- **Declare-then-delete** — write the rows, drive the badge, and delete each row once its
  reader's cursor passes it, so the durable record holds only *pending* notifications and a
  later database acquisition shows nearly nothing. This is the one variant that genuinely
  leaks less than Decision 1 while losing nothing Decision 2 names — and it is still not
  built, for the reason the house prices these things: against PLAN §6.1's adversary 3, the
  live server, it buys zero — that server saw every declaration arrive and can retain what it
  likes; deletion defends only against the weaker later-acquisition adversary, at the cost of
  cursor-hooked deletion machinery and a data lifecycle that silently diverges between the two
  channel modes. Named here with ADR 010's padding deferral as precedent: if an audit prices
  the at-rest record higher than we do, delete-behind-cursor is the shelf-ready answer.

**The decision:** `MlsMessageEnvelope` gains an optional, write-only `mentions` field — an
array of member user ids, the same ids the composer already inserts as tokens — and the write
path uses it, when the envelope is present, as the array the SQL already takes: the same
statement, the same `channel_members` join dropping strangers and non-existent ids, the same
primary-key collapse of duplicates, the same atomicity with the message row, both storage
drivers. Plaintext channels are untouched in both directions: the envelope itself is already
refused there (`e2ee_not_enabled`), so no second source of mention truth ever exists beside a
readable content column. Edits re-declare: the edit envelope carries the field, the existing
delete-and-insert rewrite applies, and an edit that omits it clears the rows — exactly as a
plaintext edit that removed the tokens would, because an edit replaces the message's mentions
as it replaces its words. Absent or empty means no rows. The cap is 50 entries (the plaintext
path's own ceiling is ~100 tokens in a 4000-character message; 50 is beyond real use and
before abuse), refused over-cap by schema validation. Live fan-out, badge queries, read
positions, and the renderer change not at all — the rows exist again, and everything that
consumed them resumes working.

## Deliberately not decided

- **Client-side mismatch surfacing** — a recipient's renderer could compare the decrypted
  tokens against the badge it received and mark divergence (a lying-sender tell). Cheap to
  add, noisy to design; named for the day someone actually lies.
- **Delete-behind-cursor retention** — priced and shelved above; revisit on audit, not on
  taste.
- **Keyword and thread notifications** — no such feature exists in either mode; whatever
  slice builds them inherits this ADR's question and should read it first.
- **Push and email delivery** — this ADR keeps them *possible* (the server knows whom to
  notify); building them is its own work with its own metadata story.
- **Screens and copy** — nothing new to draw: the badge, the picker and the rendering all
  exist; if the threat-model disclosure ever gets a settings surface, that is the design
  pipeline's row.

## Contract and schema changes this ADR implies (the freeze list)

No further decisions are needed to implement these; the orchestrator freezes them before any
implementation agent spawns.

1. **`docs/api/openapi.yaml`:** `MlsMessageEnvelope` gains
   `mentions` — `type: array`, items `type: string, format: uuid`, `maxItems: 50`,
   `writeOnly: true`, optional — with a description stating: sender-declared notification
   routing for an encrypted message; ids that are not current channel members are dropped
   silently, duplicates collapse, order is irrelevant; never echoed on reads — recipients
   render mentions from the decrypted content's tokens; and the privacy sentence: this list is
   visible to the server and is the declared metadata of an encrypted message. The envelope is
   shared by send and edit, so one schema change covers both; the `SendMessageRequest.content`
   mention paragraph gains one sentence pointing e2ee mention counts at `mls.mentions`.
2. **Migrations: none.** `message_mentions` and its index exist in both drivers; no column
   changes.
3. **Server:** the handler passes the envelope's list through to storage when `mls` is
   present; `storage.Store` and `sqlitestore` use it in place of `ParseMentions` output for
   e2ee writes (send and edit) — the existing array parameter of the existing statements. No
   endpoint is added or removed; no WS message type changes shape (the stored envelope echoed
   in frames still carries only `epoch` and `ciphertext`).
4. **Authz matrix: no new rows** (no new endpoint or WS type). `/security-review` should
   still run — the slice widens what a client-supplied field writes on the e2ee message path
   and changes the contract at the encryption boundary.
5. **Webapp:** at encrypt time the composer emits the same ids it inserted as tokens into
   `mls.mentions`; the renderer is untouched. No new user-facing strings, so no locale keys.
6. **Docs:** the E2EE threat-model / metadata disclosure list (and `docs/OVERVIEW.md`'s
   privacy notes if it carries one) gains the declared-mentions line from Decision 1.
7. **Tests the slice owes:** e2ee send with declared list writes exactly those member rows
   (stranger and duplicate dropped, badge count moves); over-cap refused; edit re-declaration
   rewrites and omission clears; plaintext path byte-identical before and after; and the
   existing mention fuzz/parity suites untouched.

## Same-PR updates this ADR requires (on acceptance)

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (mentions under e2ee: sender-declared
member-bounded notification routing in the envelope, server keeps the badge, declared metadata
named in the threat model — ADR 014). ROADMAP Phase 3: the mentions gap becomes this slice
with a pointer here. The stack tables in CLAUDE.md and PLAN §4 are unchanged — no new
component, no new dependency. `docs/OVERVIEW.md` is untouched by the ADR itself; the
implementing slice updates the mentions line it earns.
