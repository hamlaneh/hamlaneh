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

On a fresh server with Docker available, from a clone of this repository:

```bash
sudo deploy/install.sh --domain chat.example.com
```

It detects the distribution (Ubuntu, Debian, Fedora, RHEL-clones including AlmaLinux),
installs Docker and cosign if they are missing, generates every secret, brings the stack
up behind automatic HTTPS, and arms the update and backup timers. Re-running it is safe:
it will not regenerate a secret or overwrite an existing `.env`.

Without a domain, pass `--domain <your-ip>`. The install works, and the script tells you
plainly what the certificate situation is in that mode rather than pretending it is the
same.

> **The one-line `curl | bash` install is not live yet.** `get.hamlaneh.com` is not
> serving, and the installer needs the repository present because the stack currently
> builds from source rather than pulling a published image. That first build is a Go
> compile plus a web bundle, so budget several minutes and at least 2 GB of RAM on the
> machine. Published images are what will make the one-liner and the sub-5-minute
> install real; see [docs/ROADMAP.md](docs/ROADMAP.md).

## Screenshot

*Screenshot coming once there is something worth screenshotting.*

## Running it locally

```bash
git clone https://github.com/hamlaneh/hamlaneh.git
cd hamlaneh
sudo deploy/install.sh --domain localhost
```

Then open `https://localhost/` and accept Caddy's locally-issued certificate.

Use the installer rather than `cp .env.example .env && docker compose up`: the example
file ships deliberately non-working placeholders, and the server refuses to start on
them — a signing key has to be a real key. Copying it also leaves a database volume
initialised with a password published in this repository, which the installer will then
refuse to adopt. The installer generates all of it.

## Tech stack

| Layer | Choice |
|---|---|
| Backend | Go (single static binary) |
| Database | PostgreSQL (server mode); SQLite (single-binary home mode) |
| Real-time | WebSockets |
| Calls / video | LiveKit (SFU) + TURN |
| E2EE | MLS (RFC 9420) via OpenMLS, compiled to WASM — never hand-rolled crypto |
| Web frontend | React + TypeScript + Vite + Tailwind |
| Desktop | Tauri v2 wrapping the web UI |
| TLS / proxy | Caddy (automatic HTTPS) |
| Packaging | Docker Compose + `install.sh` (server); single binary (home) |

The app UI is bilingual — English (default) and Persian with full RTL support — from the
first screen. Repository code and documentation are English.

## Project status

**Early development — Phase 4 (packaging polish).** Phases 0 through 3 are
code-complete: install stack, identity and enterprise SSO, chat, DMs, files, search,
admin dashboard, a bilingual UI, calls and conferences on LiveKit, and end-to-end
encryption with MLS. Phase 4 is the work that makes it installable by a stranger, and
public launch is gated on that phase's test gate rather than on a date.

Not yet done, and stated here rather than discovered later: there is no tagged release,
so the update pipeline has never run against a real tag; the desktop app does not build
in CI yet; and the install has not been timed on real VMs across the distributions it
claims. [docs/OVERVIEW.md](docs/OVERVIEW.md) is the living description of what exists.
The full plan, with phases and measurable test gates, lives in
[docs/ROADMAP.md](docs/ROADMAP.md); strategy and rationale live in
[docs/PLAN.md](docs/PLAN.md).
