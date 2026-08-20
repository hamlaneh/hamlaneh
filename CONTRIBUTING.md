# Contributing to Hamlaneh

Thanks for your interest. Hamlaneh is in **Phase 0 (pre-development)** — the ground is
still being laid, so expect churn. This document covers dev setup, repository layout,
and the conventions every change must follow.

For vulnerabilities, see [SECURITY.md](SECURITY.md) — never open a public issue for a
security problem.

## Dev setup

You need:

- **Go 1.26** (the exact toolchain is pinned in `server/go.mod`; Go downloads it
  automatically)
- **Node.js** (current LTS) + **npm**
- **Docker** with the Compose plugin

Common commands:

| Task | Command (from) |
|---|---|
| Build server | `go build ./...` (`server/`) |
| Test server | `go test -race ./...` (`server/`) |
| Lint server | `golangci-lint run` (`server/`) |
| Webapp dev server | `npm run dev` (`webapp/`) |
| Webapp tests / lint / types | `npm test` · `npm run lint` · `npm run typecheck` (`webapp/`) |
| Locale key parity | `npm run i18n:check` (`webapp/`) |
| Boot the stack | `docker compose up` (`deploy/`) |

## Monorepo layout

```
server/      Go backend (cmd/, internal/)
webapp/      React + TypeScript + Vite + Tailwind
desktop/     Tauri v2 wrapper (later phase)
deploy/      docker-compose.yml, Caddyfile, install.sh
docs/        PLAN.md (strategy), ROADMAP.md (execution), adr/ (decisions)
```

Directories are created only when they receive real content, so some of the above may
not exist yet.

## Workflow

The authoritative conventions live in [CLAUDE.md](CLAUDE.md) at the repo root — read it
before non-trivial work. The short version:

- **English only.** All code, comments, identifiers, commits, PRs, and docs are
  English. The single exception is Persian locale resource files
  (`webapp/**/locales/fa/**`) — that is their job.
- **Conventional Commits.** `feat:`, `fix:`, `sec:`, `docs:`, `test:`, `refactor:`,
  `chore:`, `ci:` — imperative mood, optional scope (`feat(server): ...`). Small atomic
  commits.
- **Tests are not optional.** No feature merges without tests, in the same commit as
  the code. Bug fixes need a regression test that fails before the fix.
- **Branches:** `feat/<slug>`, `fix/<slug>`. `main` stays green; no force-push to
  `main`.
- **Never commit secrets** — no credentials, keys, tokens, or `.env` files (only
  `.env.example` with obviously fake values). CI runs secret scanning.
- **New dependencies need justification**: why it's needed, its maintenance status, and
  a pinned version. Expect this to be questioned in review.
- **No hand-rolled crypto, ever.** No exceptions.

## Developer Certificate of Origin (DCO)

Hamlaneh is expected to require the
[Developer Certificate of Origin](https://developercertificate.org/) rather than a CLA
(pending final confirmation before the first external PR — see `docs/PLAN.md` §12).
Sign your commits off now to be safe:

```bash
git commit -s
```

This adds a `Signed-off-by:` line certifying you have the right to contribute the code
under the project license (AGPL-3.0). There is no copyright assignment and no CLA — and
because contributions can't easily be relicensed later, that is also your guarantee the
license deal can't quietly change.

## Pull requests

- Keep PRs focused — one logical change per PR.
- Fill in the PR template; explain *why*, not just *what*.
- CI must be green: build, lint, tests, secret scan, and (where relevant) the compose
  smoke test.
- Update any docs your change makes stale (README, ROADMAP checkboxes, API spec) in the
  same PR.
