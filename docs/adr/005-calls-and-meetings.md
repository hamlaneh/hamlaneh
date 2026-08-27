# ADR 005 — Calls and meetings: LiveKit, the token service, TURN, and conference links

**Status:** accepted, 2026-08-27 · **Owner:** orchestrator · **Scope:** Phase 2

## Context

Phase 2 adds real-time media to an instance that so far moves only JSON. The stack decision is
already made (PLAN §4: LiveKit as the SFU, plus TURN). Every remaining question is an
*attachment* question — how media joins a codebase whose authorization is one choke point, whose
route and budget tables fail closed, and which must survive `docker compose up` with only a
domain or a bare IP.

Three precedents this phase reuses rather than reinventing: SCIM tokens (a credential-minting
surface), the files origin (a cookie-less surface authorised by a signed artifact), and invite
links (a public capability URL). Phase 2 needs all three shapes at once.

One structural fact simplifies several questions: **channel deletion does not exist.** There is
no `DELETE /channels/{id}`; channels are left, and the last member cannot leave. The "room whose
channel was deleted" case is unreachable today.

## Decisions

**One stable room per conversation, created on demand.** A LiveKit room maps to a `channels` row
— DM or named channel alike — as `chan-<uuid>`; conferences are `conf-<uuid>`. Rooms are never
pre-created: LiveKit instantiates one when the first authorised token joins, and closes it after
the last participant leaves.

A per-call id would need a `calls` table, a current-call pointer, and a race when two people
start a call at once. A stable name needs none of that: minting is deterministic, and two
simultaneous starters land in the same room, which is the wanted outcome. Reuse costs nothing —
a token minted during a previous call is entitled by the same membership as the next one, and it
expires long before that matters. **There is no `calls` table at all**; LiveKit's live room state
is the truth, and duplicating it into Postgres would buy a cache-invalidation problem. The
consequence, stated rather than hidden: a LiveKit restart ends every call.

**The token service is the security core.** Our server holds the API key and secret and is the
only minter. A token is scoped to one room and one identity, carries the least-privilege grant
set, and lives two minutes. It is a join ticket, not a session. `roomList` is absent so no
participant can enumerate rooms — which is also why deriving a room name from a channel id is
safe. `roomAdmin` is absent so no participant can eject another. `canPublishData` is absent so
chat stays on the one write path with the one authz choke point, which also keeps message E2EE
on the MLS path in Phase 3.

**A token can outlive its membership, briefly, and the exposure is bounded in two places rather
than denied.** A JWT is valid until it expires and membership can be revoked a second later.
Minted-but-unused is bounded by the two-minute TTL, and nothing shorter is honest: closing that
window entirely would need LiveKit to consult us per join, which its model does not do.
Already-joined is bounded by us calling `RemoveParticipant` as after-commit work of the two
events that end entitlement — removal from the channel, and account deactivation, which extends
ADR 004's offboarding from sockets to calls. Two residual windows are accepted and documented:
revoking a single session does not eject an already-joined participant, and a failed removal RPC
leaves them until the call ends.

**The signal path rides the app origin.** Caddy proxies `/rtc*` to LiveKit. One choice solves
four problems: no new subdomain, so **bare-IP installs work identically**; no new certificate; no
client-side URL configuration; and no CSP change, since `connect-src 'self'` already admits
same-origin `wss:`. Nothing else of LiveKit is public — its RoomService API is reachable only
from inside the compose network.

**Embedded TURN, not coturn.** The deciding reason is credentials: embedded TURN issues
per-participant short-lived credentials over the already-authenticated signal connection, so we
mint nothing, store nothing, and have no credential-handout endpoint to authorise and rate-limit.
A non-member never receives any, because no join token means no signal connection. A leaked
credential is worth relay bandwidth until it expires — never room access, which only the JWT
grants. With coturn we would own that entire story ourselves.

**No TURN/TLS in the default install, and this is the accepted gap.** TURN/TLS wants a
certificate; Caddy owns ours, occupies 443, and cannot terminate TURN's TLS. The default stack
traverses NATs with STUN plus external-IP discovery, TURN over UDP, and ICE over TCP. What it
does not traverse is a network permitting *only* TLS on 443 — that needs a dedicated IP, which a
single-box zero-config install cannot have. This goes in the hardening guide rather than being
papered over.

**Three published ports** — TURN/UDP, ICE/TCP, media/UDP. Compose opens Docker's own firewall
path, but a cloud provider's security group is beyond any script's reach. Those ports are the
single manual step a locked-down VPS operator must take, `install.sh` prints them, and the NAT
drill is what proves the instruction suffices.

**Our gateway carries three events and no new client frames**: `call_started`, `call_updated`,
`call_ended`, scoped to channel membership. A DM peer's sockets receive `call_started` with no
subscribe, and the client rings from it — that is the entire 1:1 ringing design for this phase.
The events are hints, not truth: no sequence numbers, no replay, and clients reconcile against
REST on channel open and on reconnect, exactly as presence does. A five-minute-old call event is
worthless or wrong.

**A conference link is a bearer capability whose blast radius is one media room.** It never
authenticates: no session is minted, no user row is created or read, and no endpoint beyond the
one join route honours it. A link-holder who is not an instance member may join — that is the
feature — but a closed-registration instance stays exactly as closed, because the guest receives
a token for one `conf-` room and nothing else. Unknown, expired and revoked links all answer the
same 404, as invites do. Revocation ends the live room rather than merely refusing the next join.

**Conference links do not expire by default.** Secure-by-default argues for expiry; the dominant
use is a standing weekly link, and an expiry that surprises people drives worse behaviour —
minting a fresh link for every meeting, pasted into more places. Instead the link is always
revocable, always visible to an administrator, and its creation and revocation are audited. An
optional expiry may be set at creation for a link genuinely meant to be short-lived.

## Consequences

- One migration, for conferences. Channel calls need no schema.
- New dependencies at merge, each pinned and vetted: the LiveKit Go server SDK and protocol
  module, `livekit-client` in the webapp, and a digest-pinned server image.
- `GET /api/v1/instance` gains a `calls` flag, so the UI omits call controls rather than offering
  a door that goes nowhere. A half-configured LiveKit environment stops startup, as a
  half-configured mail transport does.
- A webhook receiver registered outside the contract — the files-origin precedent — authenticated
  by verifying LiveKit's signature over the body. Delivery is at-least-once and unordered, so
  every event is treated as a hint that triggers a fresh read of live state, which makes the
  handler idempotent and order-proof by construction.
- Call activity in channels is deliberately **not** audited: high volume, and no security
  question that membership does not already answer. Conference creation, revocation and guest
  joins **are** — they are the instance's only anonymous-access events.
- A guest types their own display name, so a guest can present as anyone. That is inherent to
  anonymous-guest meetings and is mitigated only by the humans in the room. Named, not hidden.

## Not in this phase

Recording, egress, ingress and SIP — each a whole service, absent from the roadmap, and recording
collides with Phase 3's strict mode. Moderator controls, because no channel role model exists
beyond creator and inventing one for calls is a product decision. Decline/busy/missed-call
signalling, multi-device call presence, "in a call" presence, and call history — each cut where
it was named. Group DMs, breakout rooms, waiting rooms, livestreams. Multi-node LiveKit: the
instance is the organisation (ADR 001), so one node per instance is the architecture rather than
a limitation.

## Slices

1. **Media plane in the stack** — deploy only, and it exists partly to *verify three
   assumptions*: that embedded TURN runs cert-less over UDP, that it relays in-process without a
   further port range, and that LiveKit takes its whole config from the environment. Falsifying
   any of them triggers the documented fallback rather than a redesign.
2. **Call core, server** — the two channel endpoints, token minting, the webhook receiver, the
   three gateway events, and the removal hooks on membership loss and deactivation.
3. **Call UI, webapp** — in parallel with 2, against mocks.
4. **NAT truth** — the relay-only CI test, the key-leak scan, and the manual two-network drill.
5. **Conference rooms** — last, on ADR 004's logic: the widest door lands after every gate it
   must respect is tested. It also forces the webapp's first unauthenticated route.

## Open questions

- Three LiveKit assumptions are verified in slice 1, not assumed here. If the signal channel
  turns out not to refresh a connected participant's token, the TTL rises and this ADR's exposure
  statement must be rewritten rather than quietly left standing.
- Whether a conference should distinguish a verified member from a guest. Deferred with the
  impersonation weakness named.
- Home-mode calls: LiveKit is a second process a single binary cannot trivially absorb. Phase 4
  decides; nothing here worsens either branch.
- Phase 3's one real tension with this design: conference guests have no identity for MLS key
  exchange, so strict mode must either exclude them from end-to-end encrypted conferences or
  design a key-in-fragment scheme. Left genuinely open; nothing here forecloses either.
