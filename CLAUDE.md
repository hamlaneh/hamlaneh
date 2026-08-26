# Hamlaneh — Project Instructions

Hamlaneh (هم‌لانه, "shared nest") is a self-hosted team communication platform: chat, DMs, file
sharing, voice/video calls, screen share, and conferencing. One-line install, secure by default,
100% open source (AGPL-3.0), revenue from official hosting.

**Read before any non-trivial work:**
- `docs/PLAN.md` — master plan: vision, architecture, security plan, business model. Strategy lives there.
- `docs/ROADMAP.md` — executable phases with test gates. Execution lives there.

**Current status:** Phase 1.2 is complete — chat works end to end, proven by Playwright against
the real stack. Phase 0 (hardened compose stack, contract-first codegen, CI) and Phase 1.1
(identity core, password reset, two-step verification, session management) shipped before it.
Next: 1.3 (file upload, link previews, file search).
`docs/OVERVIEW.md` always carries the current picture — read it, not this line, for detail.

## Tech stack (decided — see "Changing a decision" below for the only way to relitigate)

| Layer | Choice |
|---|---|
| Backend | Go (single static binary) |
| Database | PostgreSQL (server mode) + SQLite (home mode) — one storage interface, two drivers |
| Real-time | WebSockets |
| Calls/video | LiveKit (SFU) + TURN (coturn / LiveKit embedded) |
| E2EE | MLS (RFC 9420) via audited library (OpenMLS or libsignal — final pick at Phase 3 start; **never hand-rolled crypto**) |
| Web frontend | React + TypeScript + Vite + Tailwind |
| Desktop | Tauri v2 wrapping the web UI |
| TLS/proxy | Caddy (automatic HTTPS) |
| Packaging | Docker Compose + `install.sh` (server); single binary (home) |
| Migrations | golang-migrate; SQL files in `server/internal/storage/migrations/NNNN_desc.{up,down}.sql`; never edit an applied migration |

**SQLite timing:** the SQLite driver is Phase 4 work. Until then: design against the storage
interface, implement and test Postgres only. Do not write SQLite code or dual-driver tests before
Phase 4. From Phase 4 on, the full storage-interface test suite runs against both drivers in CI.

**Changing a decision:** material technical decisions (including any change to this table) get a
one-page ADR in `docs/adr/NNN-slug.md` (context, decision, consequences) plus an update to this
file and PLAN.md §12 in the same PR. That is the only way to relitigate — agents who think a
decision is wrong report it; they don't deviate silently.

## Monorepo layout

```
server/      Go backend (golang-project-layout conventions: cmd/, internal/)
webapp/      React + TypeScript
desktop/     Tauri wrapper
deploy/      docker-compose.yml, Caddyfile, install.sh, verify-defaults.sh
docs/        PLAN.md, ROADMAP.md, OVERVIEW.md, adr/, api/openapi.yaml, design/
```

Create directories only when they receive real content.

## Commands & toolchain

Filled/updated by the Phase 0 scaffold PR — keep this section current; every agent reads it.

- Go: latest stable, pinned via `toolchain` in `go.mod`. Module: `github.com/hamlaneh/hamlaneh/server`
- Node: current LTS + npm (lockfile committed, `npm ci` in CI)
- Server: `go build ./...` · `go test -race ./...` · `golangci-lint run` (from `server/`)
- Webapp: `npm run dev` · `npm test` · `npm run lint` · `npm run typecheck` · `npm run i18n:check` · `npm run e2e` (all from `webapp/`)
- E2e drives the **real stack**: `npm run e2e` builds and starts the compose stack under its own
  project name and runs Playwright against it over HTTPS. It never touches a running instance's
  volumes. `HAMLANEH_E2E_ALL_LOCALES=1` widens `fa` from the smoke subset to the full suite
  (what the nightly workflow runs). Needs Docker Compose ≥ 2.24.
- Stack: `docker compose up` (from `deploy/`) · `deploy/verify-defaults.sh` checks secure defaults
- Locale parity: `npm run i18n:check` (fails on en/fa key divergence)

## Language policy

- **Everything in the repo is English**: code, comments, identifiers, commit messages, PRs,
  issues, all docs, this file.
  **Single exception:** the localized user-facing content itself — a resource whose whole
  purpose is to carry a translation. That is `webapp/**/locales/fa/**` and the `fa` half of any
  localized template wherever it lives (e.g. `server/internal/mailer/templates/*.fa.*`). The
  exception covers only the translated strings; the file names, the keys, the code that loads
  them, and every comment around them stay English.
- **The app UI is bilingual**: English (default) + Persian (`fa`) with full RTL support.
- Consequences, enforced from the first screen:
  - No hard-coded user-facing strings — every string goes through i18n keys (use `i18n-expert` skill).
  - Layout must work in RTL. Use CSS logical properties (`margin-inline-start`, not `margin-left`).
  - Locale files: `en` is the source of truth; `fa` must maintain key parity (CI-checked).
- `README.md` is English; a `README.fa.md` translation may exist and must be kept in sync or deleted.

## Engineering principles

1. **Installation is the product.** Every feature must survive "does this still work after
   `docker compose up` with zero config?" If a feature needs manual YAML editing, redesign it.
2. **Secure by default.** Defaults carry the entire security load (see PLAN.md §6.5). Never add
   an insecure-but-convenient default, even temporarily.
3. **Assemble, don't reinvent.** LiveKit for media, an audited MLS library for crypto, Caddy for
   TLS. We write product and glue. Hand-rolling crypto or an SFU is grounds to stop and reconsider.
4. **Honesty over hype.** Never write "unhackable", "military-grade", or "100% secure" in any
   doc, comment, or marketing string.
5. **Clean code, boring code.** Small functions, meaningful names, no dead code, no speculative
   abstraction (YAGNI), no clever tricks where boring works. Go style per the `golang-code-style`,
   `golang-error-handling`, `golang-naming` skills (golangci-lint is the arbiter); TS per strict
   ESLint + `tsc --strict`.
6. **Every error is handled or explicitly propagated.** No `_ = err`, no empty `catch`, no
   swallowed promise rejections.

## Development workflow

### The loop for every vertical slice

1. **Contract first.** The orchestrator finalizes changes to `docs/api/openapi.yaml` and the
   migration files **before** spawning implementation agents. Codegen keeps everyone honest:
   `oapi-codegen` (Go server types), `openapi-typescript` (webapp client types), MSW mock
   handlers typed against the generated schema; CI fails if generated code drifts from the spec.
2. **TDD.** Red → green → refactor (use the `tdd` skill). Tests land in the same commit as the code.
3. **Implement** following the domain skill (see roster below).
4. **Review gates — per slice (the branch about to merge), not per commit:**
   - `/code-review` — every slice.
   - `/security-review` — mandatory when the slice touches: auth, sessions, crypto, file
     upload/download, URL fetching, permissions, deploy config, CI/workflow files (`.github/**`),
     dependency changes (`go.mod`/`go.sum`/`package.json`/lockfiles), or any **new** endpoint or
     WS message type. Routine changes behind existing authz middleware don't re-trigger it.
   - `/simplify` — not a merge gate; run when a slice feels bloated, or as an end-of-phase pass.
5. **Update docs** touched by the change (API spec, README, ROADMAP checkboxes).
   `docs/OVERVIEW.md` is the living description of the whole project: any slice that adds or
   removes a feature, screen, component, or dependency updates it **in the same commit** —
   it must always match reality (the user hands it to external tools for context).

### Agent orchestration

Claude orchestrates; subagents implement. Standard split for a vertical slice:

- **Backend agent** owns `server/`; **frontend agent** owns `webapp/`. They run in parallel
  safely because they own disjoint directories. Shared files (`openapi.yaml`, locale `en` keys,
  ROADMAP.md) are frozen during agent runs — the orchestrator finalizes the contract before
  spawning and applies doc updates after integration. Use git worktrees only in the rare case
  two agents must touch the same directory.
- Frontend never waits for backend: it builds against MSW mocks generated from the spec.
- **Security agent** reviews the combined diff against PLAN.md §6.2 hot spots (IDOR, XSS, SSRF,
  uploads, rate limiting) after implementation, before merge.
- Orchestrator integrates, runs the full test suite + e2e, and resolves conflicts.

Agent rules:
- Agents commit freely on their own feature branch, never to `main`. Only the orchestrator
  merges. Small atomic commits, tests included, apply on agent branches too.
- Never weaken or delete a test to make it pass — report the conflict instead.
- If the agreed API contract turns out wrong, stop that part and report the needed spec change —
  do not edit `openapi.yaml` unilaterally.
- New dependencies: add only when strictly needed, and list each in a **NEW DEPENDENCIES**
  section at the top of your final report with a one-line justification. The orchestrator vets
  each (need, maintenance status, `govulncheck`, pinned version) before merge.

### Model selection protocol

Before starting any user-assigned task or phase, recommend the model + effort tier for the
**main session** in one line (e.g. "Fable 5 / high") and, if the session isn't on it, ask the
user to switch before proceeding. Subagent models/effort are the orchestrator's own call per
task. Rough map:

- Architecture, security design, auth/session/crypto code, E2EE (Phase 3), hard debugging → Fable 5 (high–max)
- Standard feature slices (routine backend/frontend implementation) → Opus or Sonnet
- Mechanical work (boilerplate, config tweaks, translations, doc formatting) → Sonnet or Haiku

Cost optimization: the session may run on Opus for day-to-day slices while the orchestrator
pins `model: "fable"` on individual security-critical subagents (security reviews of
auth/crypto diffs, contract design help). The contract-first step at each slice start is
short — a brief Fable session (or Fable subagent) followed by Opus implementation is the
economical default.

### Testing policy (non-negotiable)

- **No feature merges without tests.** Bug fixes require a regression test that fails before the fix.
- Go: table-driven tests, `go test -race` always; integration tests against real Postgres
  (dockerized) — not mocks — for storage and authz code.
- **Authz matrix harness**: a table-driven harness where every endpoint (REST **and** WS
  subscribe/publish) registers one entry; the harness auto-runs the user-A-cannot-touch-B matrix
  (anonymous/non-member/member/admin × read/write/delete). Registering a new endpoint is a
  one-line ask per slice, and a CI script parses `openapi.yaml` and fails if any endpoint lacks
  a matrix entry. IDOR is the #1 real-world bug class — this is the defense.
- Web: Vitest for units, Playwright (`webapp-testing` skill) for e2e. **Per-PR CI:** full e2e in
  `en` + an `fa` smoke subset (login, channel view, one RTL-critical screen). **Nightly / before
  merge to main:** full suite in both locales. Locale key-parity check runs per-PR.
- Parsers and any input-handling code get Go native fuzz tests (~30s smoke per target in PR CI;
  nightly long-fuzz with persisted corpus).
- **Enforced coverage floors in CI:** packages under `internal/authz`, `internal/session`, and
  crypto-adjacent packages require ≥95% statement coverage (threshold script fails the build).
  Elsewhere coverage is informational.

### Skills roster — use these, don't wing it

| Domain | Skill(s) |
|---|---|
| Go: style, errors, naming | `golang-code-style`, `golang-error-handling`, `golang-naming` |
| Go: project structure | `golang-project-layout` |
| Go: tests | `golang-testing` + `tdd` |
| Go: concurrency, context | `golang-concurrency`, `golang-context` |
| Go: security | `golang-security` (plus `/security-review` gate) |
| Go: DB access | `golang-database` |
| Schema design | `postgresql-table-design`, `supabase-postgres-best-practices` |
| API design | `openapi-spec-generation` |
| Real-time/WebSockets | `websocket-engineer` |
| React/frontend | `vercel-react-best-practices`, `tailwind-design-system` |
| UI/UX review | `ui-ux-pro-max` |
| i18n / RTL | `i18n-expert` |
| Desktop | `tauri-v2` |
| Deploy | `docker-compose-orchestration` |
| E2e testing | `webapp-testing` |
| Debugging | `diagnose` |

If a named skill is unavailable in your environment, note it in your report and proceed with
official docs — never silently skip the concern or stall. LiveKit and the MLS libraries have no
skill — use their official docs and pin exact versions.

## Git rules

- **Never commit:** secrets, `.env*` (except `.env.example`), private keys, TLS certs, tokens,
  `*.db`/`*.sqlite*`, user uploads, `node_modules/`, build output (`dist/`, `target/`, binaries),
  coverage reports, `.idea/`, `.vscode/` (except shared `extensions.json`), scratch/temp files.
- **No default credentials, ever.** No placeholder passwords or keys that work; all real secrets
  are generated at install/first-run into untracked files. `.env.example` documents every env
  var with obviously-fake values. Committed compose files reference env vars only.
- **Conventional Commits**: `feat:`, `fix:`, `sec:` (security fix), `docs:`, `test:`,
  `refactor:`, `chore:`, `ci:`. Scope optional: `feat(server): ...`. English, imperative mood.
- Small atomic commits; tests in the same commit as the code they test.
- `main` stays green — never merge with failing CI. Feature branches: `feat/<slug>`, `fix/<slug>`.
- No force-push to `main`. No `--no-verify`.
- Security fixes for released versions follow coordinated disclosure (PLAN.md §6.6) — prepare
  privately, release with advisory. Pre-release (now), `sec:` commits are normal commits.
- DCO (`git commit -s`) is the leaning choice — confirm per PLAN.md §12 before the first
  external PR, then enforce.

## CI gates (all must pass; set up in Phase 0)

- Go: build, `go vet`, `golangci-lint`, `go test -race ./...`, `gosec`, `semgrep`, `govulncheck`,
  coverage floors (see testing policy)
- Web: `tsc --noEmit`, ESLint, Vitest, Playwright per the e2e tiering above
- Repo: `gitleaks` (secret scanning), locale key-parity check, OpenAPI codegen drift check,
  authz-matrix completeness check, Dependabot enabled
- **compose-smoke** (required on every PR touching `server/` or `deploy/`): `docker compose up -d`
  from clean state → `/healthz` 200 → login page 200 → `deploy/verify-defaults.sh` exits 0 → `down -v`
- CI hygiene: third-party Actions pinned by commit SHA; minimal workflow token permissions;
  `npm ci` + `go mod verify`; base images pinned by digest; secrets never echoed to logs;
  no `pull_request_target` with PR-code checkout
- Releases (Phase 4+): Sigstore/cosign signing, SBOM generation, anti-rollback updater checks

## Security non-negotiables (PLAN.md §6.9)

- Security patches ship to everyone, simultaneously, free. Never paywalled.
- No claim of "unhackable", ever. No hand-rolled cryptography, ever.
- All authorization goes through the central authz package — one choke point. Route-level checks
  (authn, admin) live in middleware; resource-level checks are explicit
  `authz.Can(ctx, user, action, resource)` calls at the top of the handler. Handlers never
  inline their own permission logic, and every `authz.Can` call site is covered by matrix tests.
- New dependency = justify need, check maintenance status, run `govulncheck`, pin version.

## Definition of Done (per slice)

- [ ] Tests written and green (unit + integration; e2e if user-facing)
- [ ] New endpoints/WS paths registered in the authz matrix harness
- [ ] No hard-coded UI strings; `fa` keys added; renders correctly in RTL
- [ ] `gofmt`/`vet`/lint clean (Go), `tsc`/ESLint clean (TS)
- [ ] `/code-review` and (if triggered — see workflow) `/security-review` passed
- [ ] OpenAPI spec and affected docs updated; codegen regenerated
- [ ] `docs/OVERVIEW.md` updated if the slice added/removed a feature, screen, component, or dependency
- [ ] compose-smoke CI job green

## UI pipeline

**Claude never invents visual design. Ever.** All visual design (layout, colors, typography,
spacing, look & feel) is produced externally in Claude Design — the user drives this and
delivers mockups. Claude's job is faithful implementation, not design authorship.

Per-screen functional requirements (what a screen must contain — content, components, states)
live in `docs/design/BRIEFS.md`; the user feeds them to the design pipeline.

**When a design is delivered, the orchestrator mirrors it into the repo before spawning any
implementation agent** — the mockup file into `docs/design/mockups/` and its written handoff
into `docs/design/`, with delivered token sheets copied verbatim into `webapp/src/tokens.css`.
Subagents cannot reach the design canvas; a mockup that lives only online blocks them, and an
agent that cannot see the design must stop and report rather than invent one.

Design status lives in `docs/design/STATUS.md` — a table of screen → mockup link or `PENDING`.
Frontend agents check it before building a screen: mockup exists → implement it faithfully
(review with `ui-ux-pro-max` against the mockup); `PENDING` → build **unstyled functional
plumbing only** (plain semantic HTML, no custom styling beyond structure) and mark the row
`awaiting-design`. When the design lands later, the screen is reskinned to match it exactly.
