# Hardening Hamlaneh

**A default install is the hardened install, and this page is assumed unread.**

`deploy/install.sh` generates every secret, closes the database to the network, runs every
container non-root on a read-only filesystem with all capabilities dropped, sets the security
headers, rate-limits the authentication endpoints, and leaves public registration off. Nothing
on this page is required in order to be safe. If you find something here that *is* required,
that is a bug in the installer — please report it rather than following the instructions.

This page exists for two other things:

1. **Optional extras** an organization may want on top of the defaults — restricting the admin
   surface to known networks, running behind a proxy you already own.
2. **Choices no installer can make for you**, because they live outside the machine — where an
   offline copy of a key is kept, and who can reach it.

Every section says what it protects against **and what it does not**. None of this makes an
instance unbreakable; there is no such thing, and Hamlaneh does not claim one.

---

## Start here: check that the defaults actually took

```sh
deploy/verify-defaults.sh
```

Run it against a booted stack (`docker compose up -d` from `deploy/`). It probes one running
instance from the outside and checks the security headers survive the proxy, that the built
application is served, that uploaded files come back as sandboxed attachments, that anonymous
callers are refused, that LiveKit's admin API and debug dumps are *not* reachable on the public
origin, that Postgres publishes no host port, and that every container runs non-root. It prints
each check and exits non-zero listing the failures.

**Protects against:** an install that drifted — a hand-edited compose file, a proxy in front
that strips headers, a container restarted with different flags, an upgrade that half-applied.

**Does not protect against:** anything about your data, users, passwords, or network. It reads
the posture of one running instance at one moment. A green run means the defaults are intact,
not that the instance is secure.

Run it after every upgrade and after any change to `deploy/`.

---

## Ports: what to open, what never to open

`install.sh` prints this at the end of every run. It is repeated here because a **cloud
security group is a different firewall** — upstream of the host, invisible to any script
running on it, and the reason calls fail silently on a locked-down VPS while chat works fine.

| Port | Why it is open |
|---|---|
| `80/tcp` | HTTP → HTTPS redirect, and one of the two ACME challenge types. Closing it means a visitor who types the bare hostname gets a connection error instead of a redirect; certificates still renew over TLS-ALPN on 443. |
| `443/tcp` | The application. |
| `443/udp` | HTTP/3. Optional — closing it costs some latency and nothing else. |
| `3478/udp` | TURN: the relay that makes calls work from restrictive networks. |
| `7881/tcp` | Call media over TCP, for networks that block UDP. |
| `7882/udp` | Call media. |

Nothing else. Two ports specifically:

- **`7880` must never be reachable.** LiveKit serves its RoomService admin API (`/twirp/*`) and
  goroutine and room dumps (`/debug/*`) on the same port as the signal endpoint. Anything that
  authenticates with the LiveKit API secret is an administrator of every room on the instance.
  Compose does not publish 7880, and the Caddyfile proxies exactly `/rtc` and `/rtc/*` — never a
  prefix of the whole service. `verify-defaults.sh` checks both halves.
- **`5432` must never be reachable.** The `db` container publishes no host port and sits on an
  `internal:` Docker network with no route off the host.

**Protects against:** direct attack on the media and database planes.

**Does not protect against:** anything reachable on 443 — which is the entire application, and
where the bugs that matter live.

---

## Restricting the admin surface to known networks

The admin dashboard is on its own path and refuses non-admins, but an allow-list adds a second
condition an attacker must satisfy: being on your network, not just holding a credential.

**The path that matters is the API, not the page.** `/admin` is a client-side route — the server
answers it with the same `index.html` every visitor gets, and the shell renders nothing until
`/api/v1/admin/*` answers. Blocking only `/admin` hides a page and protects nothing. The two
surfaces that carry administrative power are:

- `/api/v1/admin/*` — users, invitations, org settings, encryption mode, the audit log
- `/scim/v2/*` — provisioning, authenticated by a bearer token rather than a session

In `deploy/Caddyfile`, inside the `{$HAMLANEH_DOMAIN}` site block and **above** the existing
catch-all `handle { reverse_proxy server:8080 }`:

```caddyfile
@admin path /api/v1/admin/* /scim/v2/*
handle @admin {
	@untrusted not remote_ip 203.0.113.0/24 198.51.100.7
	respond @untrusted 403
	reverse_proxy server:8080
}
```

Replace the ranges with your own. `remote_ip` here is the true peer address: Caddy is the edge
and forwards no client-supplied header (see the next section — if that stops being true, this
allow-list stops being an allow-list).

The Caddyfile is baked into the proxy image, so apply it with:

```sh
docker compose up -d --build
```

Two consequences worth knowing before you start: an upgrade that ships a new Caddyfile will need
this re-applied, and an admin travelling outside the allow-list is locked out of administration
until someone edits this file on the host.

If your organization already enforces network location at the VPN or the corporate proxy, do it
there instead — same protection, one less local edit to carry across upgrades.

**Protects against:** an attacker who obtained a valid admin credential somewhere else —
phishing, password reuse, a leaked SCIM token — and tries to use it from the internet.

**Does not protect against:** an attacker already inside an allowed network; a compromised admin
browser on an allowed address (the requests come from the right IP); or anything at all on the
chat surface, which every user reaches from everywhere by design. It is a second condition, not
a wall.

---

## Running behind a reverse proxy you already own

The stack ships its own edge (Caddy) and expects to be the thing the internet talks to. The
simplest correct answer is to let it: point DNS at the host and let Caddy get its own
certificate. Everything below is for environments where that is not allowed.

### Read this before putting anything in front of Caddy

With its shipped configuration, Caddy sets no `trusted_proxies`, which means it **strips any
client-supplied `X-Forwarded-For` and forwards only the true peer address.** The Go server takes
the leftmost `X-Forwarded-For` value as the client IP on exactly that basis, and that value is
what rate-limits sign-in, password reset, invite redemption and the SSO flow.

Reading that header at all is a **server-mode** behaviour and nothing infers it: the compose
stack tells the server a proxy is in front of it, because Caddy is there by construction. The
single binary (home mode) tells it nothing is, so there the direct peer is the client and the
header is not read — otherwise any process on that machine could pick its own sign-in budget and
its own address in the audit log. A home instance published to a LAN behind your own TLS
terminator therefore rate-limits every client onto that terminator's address; that is the
fail-closed direction, and the paragraph below is why there is no switch for it.

Put a CDN or another proxy in front without changing anything, and every request arrives from
one address, so per-IP rate limiting collapses onto that address. Configure `trusted_proxies` so
the forwarded header is honored, and a client can now supply its own header — sign-in rate
limiting becomes spoofable, which is worse.

**There is no supported configuration for this today.** Doing it correctly needs the server
switched to rightmost-untrusted `X-Forwarded-For` parsing, matched to a `trusted_proxies` list,
and neither is a setting yet. If you need to front the instance with a CDN, say so in an issue —
it is a code change, not a configuration you can get right on your own.

### TLS terminated upstream, Hamlaneh reached over the internal network

Workable, with three requirements:

- Forward **all** of `/`, and preserve the `Host` header. The app origin serves the WebSocket
  gateway (`/api/v1/ws`) and the LiveKit signal path (`/rtc`, `/rtc/*`) — both need upgrade
  handling. Never proxy anything else of LiveKit; `/twirp/*` and `/debug/*` must stay internal.
- Forward the **files origin** (`files.<domain>`) as a separate hostname to the same backend.
  It is cookie-less on purpose: a separate origin is why an uploaded file can never script
  against the application. Collapsing it onto the app origin gives that up.
- Send `Strict-Transport-Security` from your proxy, since it is now the thing that has TLS. Do
  **not** add CSP, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy` or the
  `Cross-Origin-*` pair — the Go server sets those, and a second, drifting copy produces a policy
  nobody wrote and nobody can debug. Then run `verify-defaults.sh` and confirm the headers still
  arrive.

Media does not go through your proxy at all: `3478/udp`, `7881/tcp` and `7882/udp` reach LiveKit
directly and must still be open.

**Protects against:** nothing by itself. This section is about not *losing* protections you
already have.

**Does not protect against:** the rate-limiting problem above, which no proxy configuration
fixes.

---

## Home mode beyond the machine it runs on

`hamlaneh-server home` is the single-binary install: SQLite, no Docker, no Caddy, and therefore
no TLS. It binds `127.0.0.1:8080` for that reason — `localhost` is a secure context, so the
session cookies (`Secure`, `HttpOnly`, `SameSite=Strict`) and every header the Go server sets
hold exactly as they do in the compose stack. The two absences are deliberate and complete:
HSTS, which only a TLS terminator can set meaningfully, and TLS itself.

`HAMLANEH_HOME_ADDR` moves that bind, and **plain HTTP beyond loopback does not work** — it
fails closed rather than degrading. A browser will not send a `Secure` cookie over `http://` to
anything but localhost, so a home instance published to a LAN over plain HTTP visibly refuses to
sign anyone in. That is the intended outcome, not a bug to route around, and there is no setting
that turns the `Secure` attribute off.

The supported way to reach a home instance from another machine is the same as everywhere else:
put something in front of it that has TLS.

- A reverse proxy you already run — see the requirements above; forward all of `/`, preserve
  `Host`, send HSTS from the proxy and no other security header. Set `HAMLANEH_PUBLIC_URL` to
  the https origin, because the WebSocket handshake's `Origin` check compares against it and a
  mismatch refuses every socket.
- Or a private network that provides the TLS name itself (Tailscale and similar). Same
  requirement: the origin the browser uses is the origin `HAMLANEH_PUBLIC_URL` must name.

A self-signed certificate is **not** on this list. It trains a household to click through
browser warnings, which costs more than it buys.

Both loopback spellings work on the bound port: `localhost`, `127.0.0.1` and `[::1]` are one
machine and one trust boundary, so the `Origin` check accepts all three there. That allowance
exists only when the instance is actually bound to loopback — set `HAMLANEH_PUBLIC_URL` to a name
or a LAN address and the check is a single exact origin again, as it is in server mode.

### The first administrator

There is no installer here, so home mode creates the first account itself on a fresh database:
`admin`, with a password generated from `crypto/rand` and **printed to the console once**. It is
never written to disk and never appears in the log, and the account must change it at first
sign-in. Setting `HAMLANEH_ADMIN_USERNAME` and `HAMLANEH_ADMIN_PASSWORD` uses those instead.

Because the password exists only in that console output, losing it before signing in means
starting over: delete the data directory and run the binary again. A restart prints nothing — the
account already exists, so the bootstrap is a no-op, which is also what stops a second admin ever
being created or a live credential reappearing on every start.

### The data directory

Everything home mode has lives in one directory — `%AppData%\hamlaneh` on Windows,
`~/Library/Application Support/hamlaneh` on macOS, `~/.config/hamlaneh` on Linux, or wherever
`HAMLANEH_DATA_DIR` points. It holds the database, the uploaded bytes, and the two instance
secrets, which home mode generates on first run because there is no installer to generate them.
That directory is the backup unit; see the custody rules below, which apply to those two files
exactly as they apply to the compose stack's `.env`.

**On Windows, move it only somewhere your account owns.** The `0600` the server asks for is a
POSIX mode Windows does not have: no permissions are written on the files, so what protects them
is whatever the parent directory hands down. Under `%AppData%` that is your user profile's rule
and the keys are yours alone. Point `HAMLANEH_DATA_DIR` at a volume root such as `C:\hamlaneh` or
a second drive and it inherits the volume's rule instead, where every local account can read —
including `audit.key` and `file-url.key`. The server warns at startup when the directory is
outside your profile; if you need it there anyway, restrict it yourself with `icacls` first.

**Protects against:** a home instance silently serving sessions over plaintext to a network.

**Does not protect against:** anything reaching the machine itself. Home mode has no media
server, so calls are off and the instance says so. The audit chain is narrower here than in the
compose stack for the same reason: `audit.key` sits beside the database it protects, with the
same owner, so it shows tampering that did not have this machine's user account — an edited
backup, a direct write to the file, corruption — and not tampering by someone who did.

---

## Backup key custody

Two different secrets, two different owners, and confusing them is the expensive mistake.

### The instance secrets — yours to keep

`deploy/.env` is generated by the installer, `chmod 600`, and never committed. Four values in it
are not recoverable from anything else:

| Value | If you lose it |
|---|---|
| `POSTGRES_PASSWORD` | The database is unreadable, including from any backup of the volume. |
| `HAMLANEH_AUDIT_KEY` | Every audit entry recorded under it stops verifying. The rows survive and are still readable; the instance can no longer vouch that they were not rewritten. |
| `HAMLANEH_FILE_URL_KEY` | Cheap: every already-minted file URL breaks, clients re-fetch and get fresh ones. |
| `HAMLANEH_LIVEKIT_API_SECRET` | Cheap: regenerate it and restart; live calls drop. |

Keep an offline copy of `deploy/.env` somewhere that is **not** the instance and not the same
backup as the database — a password manager, an offline encrypted volume, a sealed envelope in a
safe. A copy of the key stored beside the ciphertext it opens is not a backup.

Restrict who can read it. Anyone who can read `deploy/.env` can read the database directly, mint
join tokens for every room, and rewrite the audit log without the rewrite showing.

**Automated encrypted backups now exist** — `deploy/hamlaneh-backup.sh`, documented in
[`docs/backups.md`](backups.md), daily and on by default once it has run once. They cover the
database and the uploaded files and **not** `deploy/.env`, deliberately: a key that travels with
the ciphertext it opens is not a key. So the offline copy above is not optional once you have
backups — it is the other half of them. The backup key is a third secret with the same rule, and
`hamlaneh-backup.sh` will keep warning you until you have moved it off the machine.

### The users' recovery keys — deliberately not yours

In Strict E2EE mode, each user's encrypted backup is sealed with a recovery key generated in
their browser, shown once, and never sent to the server in any form the server can use
([ADR 010](adr/010-encrypted-backups.md)). There is no reset, no escrow, and no admin path to
recover it — a server or an administrator that could recover the blob could read it, which is
the whole thing this defends.

Do not build a process that collects them. If your organization's obligations require guaranteed
recovery of content, that requirement has a name — **Compliance mode**, chosen at setup and
labeled as what it is — and not a workaround inside Strict mode.

Losing a recovery key is bounded, not fatal: the account, channel membership, and sending and
receiving all continue. What is lost is the contents of that user's backup — their recorded
trust decisions — so they are asked to verify people again.

**Protects against:** losing an instance to a dead disk with no way back in; and, for the
recovery keys, a compromised server yielding readable backups.

**Does not protect against:** a backup that is never tested. An untested restore is a hope, not a
backup. The drill is written down in [`docs/backups.md`](backups.md) and is yours to actually run,
on a machine that is not the live one.

---

## What to monitor

Concretely, from what the instance actually exposes today:

- **`GET /healthz`** — liveness, 200 when the process is up. Point your uptime check here.
- **`GET /readyz`** — readiness: database reachable and the schema at the expected version.
  A 503 here with a 200 on `/healthz` means the server is running and cannot serve.
- **The audit log** (admin dashboard → Audit, or `GET /api/v1/admin/audit`). Every response
  carries `chain_valid`. **A `false` there is the alarm this feature exists for**: it means the
  hash chain over the returned entries does not verify, which means rows were edited or deleted
  by someone with database access. It does not tell you who or what — it tells you to stop
  trusting the log and start investigating the host.
  Worth eyeballing regularly: sign-ins, admin actions, invitations issued, encryption-mode
  changes, SCIM token creation.
- **Container logs** (`docker compose logs`). Capped at 10 MB × 3 files per service by default,
  so they rotate away — ship them somewhere if you need history. Certificate renewal failures
  show up in the `caddy` service and are the most common quiet breakage on a long-running
  instance.
- **`verify-defaults.sh` after every upgrade**, as above.

There is **no metrics endpoint** — nothing Prometheus-shaped to scrape yet. If you want one,
open an issue rather than exposing something by hand.

**Protects against:** nothing, directly. Monitoring is how you find out, not how you prevent.

**Does not protect against:** a tamper-evident log being tamper-*proof*. Anyone who can write to
the database can rewrite the chain; the key that keys it lives in `deploy/.env`, outside the
database, so what they cannot do is rewrite it invisibly. That distinction is the entire feature.

---

## Already done — please do not redo these

Listed so that hardening effort goes somewhere useful, and so nobody breaks a working default
trying to improve it:

- **TLS and HSTS** — Caddy obtains and renews certificates automatically; HSTS is on.
- **Security headers** — CSP with no `unsafe-inline` and no `unsafe-eval`, `frame-ancestors
  'none'`, nosniff, Referrer-Policy, Permissions-Policy, `Cross-Origin-*`. Set by the Go server
  so home mode (no proxy) gets them too. Adding a second copy at a proxy is how you end up with
  a third policy nobody wrote.
- **Public registration is off**; there is no self-serve signup endpoint at all.
- **Rate limiting** on sign-in, two-step verification, password reset, SSO and invite
  redemption — per IP and per account, in the server.
- **Container hardening** — non-root, read-only root filesystem, `cap_drop: ALL`,
  `no-new-privileges`, base images pinned by digest.
- **Uploaded files** — served from a separate cookie-less origin through signed expiring URLs,
  as sandboxed attachments with nosniff, with image metadata stripped at ingest.
- **Link previews** — fetched through an egress guard that validates the address dialled, not
  the name resolved.
- **Secrets** — generated per install; there are no default credentials to change.

---

## Reporting

If anything on this page is wrong, out of date, or describes a workaround for something the
installer should be doing, that is worth an issue. For a **vulnerability**, do not open a public
issue — see [SECURITY.md](../SECURITY.md).
