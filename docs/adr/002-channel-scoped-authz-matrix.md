# ADR 002 — Channel-scoped authorization matrix

**Status:** accepted, 2026-08-25 · **Owner:** orchestrator · **Implements before:** any 1.2a handler

## Context

Every Phase 1.2 endpoint answers a question the current authz harness cannot ask: *member of
which channel?* The four existing principals (anonymous / member / member-must-change / admin)
are instance-scoped, so the matrix — the project's IDOR defense and the register every later
slice adds its endpoints to — can express "signed in" but not "signed in, and a stranger to this
conversation". The access rules themselves are **not** decided here: `docs/api/openapi.yaml`
already states them per operation (membership is the only visibility rule for every kind; a
non-member, org admins included, gets 404 `channel_not_found`; edit is author-only; delete is
author-or-admin-member; member removal is self, creator, or admin-member). This ADR decides only
how the harness expresses and enforces them.

## Decision

**Columns.** A channel-scoped entry carries seven required principals: the two route-gate
columns, unchanged (`Anonymous` → 401, `MemberMustChange` → 403), plus five channel relations —
`ChannelNonMember`, `ChannelMember`, `ChannelOwner` (the creator; `channels.created_by`),
`AdminNonMember`, `AdminMember`. The two admin columns exist to pin the contract's boundary from
both sides: admin grants nothing outside membership (`AdminNonMember` gets the same 404 as any
stranger) and, within membership, only what the operation's contract text grants (delete yes,
edit no, remove-others yes).

**Authorship refinements.** Operations that distinguish the author (`editMessage`,
`deleteMessage`) add optional extra principals — `MemberAuthor` at minimum — whose cells act on
a fixture message the acting user authored. The runner executes exactly the keys present in
`Want`; the completeness gate enforces the required minimum, so refinements are additive and
cannot be forgotten into silence.

**Fixture bundle.** Every cell provisions, through storage directly: one fresh channel of the
entry's declared kind, its creator, a seed member who authors the fixture message where the
operation needs one (so "member" columns are genuinely non-authors), and the acting user with a
session. Fresh per cell — mutating cells must not poison neighbours.

**Kinds.** Each channel-scoped entry declares its fixture channel kind. Required coverage is
**private and dm** for every `{channelId}` operation — dm answers genuinely differ (400
`dm_membership_fixed`, no topic, fixed pair) and a DM is the most private object in the product.
`public` is pinned once (getChannel, public, `ChannelNonMember` → 404): in Phase 1.2 membership
is the only rule for every kind, so one row is the tripwire for anyone who later "opens up"
public channels without a contract change, and duplicating all columns across a third kind to
prove sameness buys nothing else.

**Gate tightening.** The completeness check fails when an operation whose path contains
`{channelId}` registers with the instance-scoped entry shape. Without this, the path of least
resistance under a deadline is to register the old four columns and dodge the five that matter.

**WS.** The WS security suite (ROADMAP 1.2a tests a–g) consumes the same fixture bundle; the
`WSRegistry` rules (`member`, `member-dm`, `self`) flip from `not_implemented` to `enforced` as
the gateway lands. No separate principal model.

## Consequences

- The matrix grows from ~4 to ~7+ cells per channel endpoint × two kinds; provisioning stays
  direct-through-storage, so the cost is seconds, not minutes.
- `sessionStub` and its 501 expectations disappear with the handlers they describe.
- Registering a 1.2b+ endpoint stays a one-entry ask, now with the channel questions forced.
- The `Want` values are read off the operation descriptions in `openapi.yaml`; a mismatch found
  while registering is a contract bug to report, not a cell to fudge.
