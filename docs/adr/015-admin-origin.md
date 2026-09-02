# ADR 015 — The admin surface gets its own port

**Status:** accepted — 2026-09-02
**Supersedes:** the "separate path" clause of [PLAN.md](../PLAN.md) §6.5, and the two
contradictory ROADMAP entries about an admin IP allow-list (Phase 1.4 deferred it, Phase 5
asked for it).

## Context

PLAN §6.5 promises "admin panel on a separate path with optional IP allow-listing". The
path shipped. The allow-list did not, and the reason it kept not shipping is written down
in [hardening.md](../hardening.md):

> **The path that matters is the API, not the page.** `/admin` is a client-side route — the
> server answers it with the same `index.html` every visitor gets, and the shell renders
> nothing until `/api/v1/admin/*` answers. Blocking only `/admin` hides a page and protects
> nothing.

So the promise as written was thin. The surfaces with power are `/api/v1/admin/*` and
`/scim/v2/*`, and both answered on the same origin, on the same port, as the chat app.

Two roads out: restrict those paths by client address (an allow-list inside the proxy), or
move them somewhere an operator can restrict with the tool they already have. This ADR
takes the second.

## Decision

**The admin dashboard and its API move to their own port on the same host.** Not a path, not
a hostname.

Three separations were possible and only one of them is cheap here:

| | Cookies survive | New certificate | New DNS record | Works on a bare IP |
|---|---|---|---|---|
| separate path | yes | no | no | yes |
| **separate port** | **yes** | **no** | **no** | **yes** |
| separate hostname | **no** | yes | yes | no |

A hostname is what the files origin uses (ADR 003) and it had to become *cookie-less* to
work, because cookies here are host-only — no `Domain` attribute is set anywhere
(`session.go`). The admin dashboard cannot be cookie-less; it is the most
authenticated surface in the product. A port is the axis that separates without taking the
session away, because **cookies are not port-scoped**: the operator signs in to the chat app
and their session is already valid on the admin port, with no second sign-in and no
`Domain` widening.

Three further facts made the port cheap rather than merely possible, each verified before
this was written:

1. **CSRF does not care.** The double-submit compares a cookie to a header and reads neither
   `Origin` nor `Referer` (`middleware.go`). It works unchanged across ports.
2. **The dashboard opens no WebSocket.** The realtime client lives in `useChat`, used only by
   `ChatShell`; `/admin/*` renders `AdminApp` instead. The single-origin WS allow-list
   (`wsgateway.go`) — which *is* port-sensitive and has no widening mechanism — is
   therefore not on this path.
3. **Same host means no new name.** Nothing enters Caddy's certificate cache, so the
   `default_sni` and `cert_issuer` machinery that a bare-IP install depends on is untouched.
   A second *hostname* would have re-run the failure ADR 003 records.

### What moves

`GET /admin`, `GET /admin/`, `/api/v1/admin/*`, `/scim/v2/*`, plus `/assets/` and `/brand/`
so the bundle loads. SCIM moves with them because it is the second powered surface and its
bearer token is exactly as valuable as an admin session.

### Where it is enforced

**In the server, not only in the proxy.** The admin listener is a second `http.Server` on
its own address; the main listener stops routing those paths. Enforcing this only in the
Caddyfile would make it a fiction for home mode, which has no proxy at all (ADR 012), and
for anyone who reaches the server directly.

The port is off by default. Configured off, every route stays exactly where it is today.

### What it is not

**The port is a deployment boundary, never an authorization decision.** Admin authorization
remains one `authz.Can` call site in `securityMiddleware`, unchanged and unweakened. An
attacker who reaches the admin port still faces the same session and the same role check.
Reaching a port is not being an admin, and no code added under this ADR may read the
listener as though it were a permission.

## Consequences

**Gained.** Cloud firewalls filter by port, not by path — "do not open this port" becomes a
control an operator already knows, and it works upstream of the host where a Caddy matcher
cannot reach. Cross-origin isolation comes along for free: a script injected into the chat
origin cannot call the admin API, because a request carrying `X-Hamlaneh-CSRF` is
preflighted and the preflight is never answered.

**Paid.** One more port to publish, to document, and to get wrong; a second listener in the
server; and a lockout risk for an operator who binds it to loopback without a way in. The
installer therefore asks rather than assumes, and prints the tunnel command with the
loopback answer.

**Untouched.** Home mode ships with the split off. It binds loopback already, its threat
model is one machine, and it has no proxy to route a second port with.

**Superseded.** The IP allow-list is not built. It aimed at the same goal from inside the
proxy, and the two ROADMAP entries never agreed on whether it should exist. An operator who
still wants address-based restriction now applies it to one port, in their own firewall,
which is what the Phase 1.4 entry argued for in the first place.
