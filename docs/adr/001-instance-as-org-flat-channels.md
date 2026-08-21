# ADR 001 — The instance is the organization: flat channels, 1:1 DMs

**Status:** Accepted (2026-08-21)
**Context phase:** Finalizing the Phase 1.2 messaging contract and schema.

## Context

ROADMAP 1.2 lists the conversation model as "Orgs → teams → channels (public/private) →
DMs/group DMs". The delivered chat design (`docs/design/mockups/Hamlaneh Chat.dc.html`,
handoff `docs/design/CHAT_HANDOFF.md`) shows something narrower and concrete: one organization
identity in the sidebar, a flat list of channels, and 1:1 direct messages. No team layer, no
group DMs, no org switcher.

Committing to the wider model means an `org_id` (and eventually `team_id`) on every table from
migration 0003 onward, an org-scoping term in every query and every authz decision, and an
org dimension in the authz matrix. Committing to the narrower model and retrofitting later is
the most expensive deferral available in this slice — it touches every row of every table.

Hamlaneh is **self-hosted**: a company installs one instance for itself. The install flow
creates exactly one admin and one implicit organization (PLAN §4 deployment modes). A second
organization inside one instance would mean tenant separation — precisely the class of bug
PLAN §8 says instance-per-customer exists to make impossible ("tenant-separation bugs can't
exist without shared tenancy").

## Decision

**The instance is the organization.** No `org_id` column, no org scoping in queries or authz.
Organization identity (name, logo, default locale, policies) is instance-level configuration,
owned by the admin dashboard (Phase 1.4).

**Channels are flat.** No team layer. Channels are `public`, `private`, or `dm`; visibility in
Phase 1.2 is membership for every kind (there is no channel directory yet, so `kind` carries
no visibility meaning on its own — it is stored so a directory and join flow can arrive later
without a schema change).

**Direct messages are 1:1.** Exactly one DM channel per user pair, canonicalized so the pair
is unique. Group DMs are out of scope.

## Consequences

**Good.** Every query, index and authz check is one dimension simpler. The authz matrix stays
tractable. The schema matches the delivered design exactly, with no columns that exist only
for a hypothetical. Multi-tenancy stays impossible by construction, which is the security
posture PLAN §8 already sells.

**Bad / accepted risk.** If a genuine multi-org requirement ever appears, it is a migration
across every table plus an authz rework — deliberately expensive, deliberately deferred.
Group DMs need a different uniqueness story than the pair index (membership-set identity), so
adding them later is a new table shape rather than a widened one.

**Follow-ups.** ROADMAP 1.2's conversation-model line is corrected to match this decision so
the two documents cannot drift. Revisiting requires a superseding ADR (CLAUDE.md "Changing a
decision").
