# Hamlaneh

**Hamlaneh** (هم‌لانه, Persian for "shared nest") is a self-hosted team communication
platform: text channels, DMs, file sharing, voice/video calls, screen sharing, and
conferencing — installed on your own server with one command, and yours to keep.

Companies and communities should be able to own their communication the way they own
their laptops. The self-hosted communication space is mature but notoriously painful to
deploy and operate. Hamlaneh's wedge is the install experience: one command on a server,
a short guided setup, and a working TLS-enabled instance minutes later — whether that
server is a VPS with a domain, a bare IP address, or a computer at home.

## Install

> **Coming — not yet functional.** This is the promise we are building toward; the
> installer does not exist yet. See [Project status](#project-status).

```bash
curl -fsSL get.hamlaneh.com | bash
```

## Screenshot

*Screenshot coming once there is something worth screenshotting.*

## Quick start (walking skeleton)

What exists today is the Phase 0 walking skeleton: `docker compose up` boots Caddy,
the Go server, and Postgres, and serves a static login page over TLS.

```bash
git clone https://github.com/hamlaneh/hamlaneh.git
cd hamlaneh/deploy
cp .env.example .env   # then edit .env and set real values (domain, generated DB password)
docker compose up
```

Then open `https://<your-domain>/` (or `https://localhost/` with the default
`HAMLANEH_DOMAIN=localhost`, accepting Caddy's locally-issued certificate).

## Tech stack

| Layer | Choice |
|---|---|
| Backend | Go (single static binary) |
| Database | PostgreSQL (server mode); SQLite planned for single-machine home mode |
| Real-time | WebSockets |
| Calls / video | LiveKit (SFU) + TURN |
| E2EE | MLS (RFC 9420) via an audited library — never hand-rolled crypto |
| Web frontend | React + TypeScript + Vite + Tailwind |
| Desktop | Tauri v2 wrapping the web UI |
| TLS / proxy | Caddy (automatic HTTPS) |
| Packaging | Docker Compose + `install.sh` (server); single binary (home) |

The app UI is bilingual — English (default) and Persian with full RTL support — from the
first screen. Repository code and documentation are English.

## Project status

**Pre-development — Phase 0 (Foundation).** Nothing usable is built yet. We are laying
the skeleton: repository hygiene, CI gates, and a `docker compose up` that serves a
login page over TLS. The full plan, with phases and measurable test gates, lives in
[docs/ROADMAP.md](docs/ROADMAP.md); strategy and rationale live in
[docs/PLAN.md](docs/PLAN.md).

## Security

Hamlaneh has **not** been audited. We publish our threat model — including what we
explicitly do not defend against — in [SECURITY.md](SECURITY.md), along with how to
report vulnerabilities. Security here is a process we commit to, not a property we
claim; external penetration testing and a cryptography audit are planned before any
security language enters marketing.

## How Hamlaneh makes money

Stated up front, because projects that change the deal midstream deserve the backlash:

- **The entire product is open source (AGPL-3.0).** No feature gating, no "enterprise
  edition", no SSO tax. SSO, SCIM, audit logs, and compliance features ship free to
  everyone.
- **Revenue comes from operating it, not from restricting it**: official hosting
  (Hamlaneh Cloud) and managed instances on customers' own infrastructure.
- **No license changes are coming.** AGPL-3.0 is the deal, for the whole product.
- **Security patches are free for everyone, simultaneously, forever.** Never paywalled,
  never delayed for non-payers.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for dev setup, repository layout, and workflow
conventions. For vulnerabilities, see [SECURITY.md](SECURITY.md) — never open a public
issue for a security problem.

## License

[AGPL-3.0](LICENSE) — the entire product, no proprietary features.
