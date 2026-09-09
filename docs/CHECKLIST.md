# Hamlaneh — Project Checklist

> **A derived snapshot, not an authority.** Every line here is a compression of a file that
> holds the real detail, and each line names that file. When this document and the file it
> points at disagree, **the file is right and this one is stale**.
>
> The three authorities: [OVERVIEW.md](OVERVIEW.md) (what the project *is* right now, updated
> every slice), [ROADMAP.md](ROADMAP.md) (task-level execution and the phase gates),
> [PLAN.md](PLAN.md) (vision, security plan, business model).
>
> **Snapshot taken:** 2026-09-09

---

## 1. Where we are, in one table

| Phase | State | What stands between it and done |
|---|---|---|
| **0 — Foundation & skeleton** | code-complete, gate met | — |
| **1 — Chat core** | code-complete | Gate needs **2 weeks of daily-driving** (user's task) |
| **2 — Calls & meetings** | code-complete | Gate needs the **manual NAT drill** (user's task) |
| **3 — E2EE** | most of the way | Multi-device key sync · Compliance server half · mentions client half · 2 drills unrun |
| **4 — Packaging** | running in parallel with 3 | Sub-5-min install at structural risk · keyless signing never run · desktop smoke e2e |
| **5 — Hardening & audit** | not started | Gates security *marketing*, not launch |
| **6 — Hamlaneh Cloud** | not started | After launch |

**Public launch fires when the Phase 4 gate passes** — repo flips public, Show HN. Audits
(Phase 5) gate the security *claims*, not the launch itself.

**Scale of the thing today:** 16 ADRs · 20 migrations (`0001` through `0020`) · 81 API
operations across 63 paths in the contract, every one with a handler behind it (nothing answers
501) · two storage drivers (PostgreSQL + SQLite) with the suite running against both · e2e
against the real Docker stack in two locales.

---

## 2. What is done

Condensed. [OVERVIEW.md](OVERVIEW.md) "Current state" is the long version and the authority.

### Foundation

- [x] `docker compose up` boots a hardened 3-container stack (Caddy auto-TLS → Go server → Postgres)
- [x] The **real React app ships inside the Go binary** — no placeholder, no separate web server
- [x] The application owns its CSP and security headers, not the proxy — home mode gets them free
- [x] Embedded migrations run at startup; `/healthz` and `/readyz` answer
- [x] Contract-first pipeline: OpenAPI + WS protocol + SCIM, all three machine-checked, codegen drift fails CI
- [x] Full CI gate set: race tests, gosec/semgrep/govulncheck, gitleaks, locale parity, authz-matrix completeness, compose-smoke

### Identity and account security

- [x] argon2id passwords; opaque tokens in HttpOnly cookies with rotation and reuse detection
- [x] CSRF double-submit; login rate limiting; forced password change for admin-created accounts
- [x] Central `internal/authz` choke point plus an authz matrix harness (every endpoint × 4 principals)
- [x] **TOTP two-step verification** — three-step enrolment, recovery codes, attempt caps on both halves
- [x] **Password reset by email** — enumeration-safe by construction; off unless SMTP is configured
- [x] **Session management** — one row per device, sign out one or all the others
- [x] **Enterprise identity** — OIDC/SSO, SCIM provisioning, audit logs (free, never paywalled)

### Chat

- [x] Channels, DMs, presence, distinct unread and mention treatments, run grouping, edit/remove markers
- [x] Composer, search as a third column, file/image/link cards
- [x] Markdown through an allowlist sanitizer — raw HTML never parsed
- [x] Files, previews, file search
- [x] Admin dashboard: users, invites, org policies, provisioning, encryption mode
- [x] Bilingual en/fa with true RTL from `dir` alone — **no RTL-specific CSS**

### Calls

- [x] 1:1 and group voice/video, screen share, conferences, guest meeting links (LiveKit + TURN)
- [x] Screen-share leak warning as a persistent band carrying its own Stop control

### E2EE — Phase 3, the shipped half

- [x] MLS via OpenMLS compiled to WASM in the browser; the server stays MLS-blind ([ADR 006](adr/006-mls-library-and-boundaries.md))
- [x] Device identity and key verification, safety numbers ([ADR 007](adr/007-device-identity-and-verification.md), [ADR 008](adr/008-key-verification.md))
- [x] Media E2EE for calls ([ADR 009](adr/009-media-e2ee.md))
- [x] Encrypted backups and a recovery key ([ADR 010](adr/010-encrypted-backups.md))
- [x] Org encryption mode — Strict by default, and nothing converts ([ADR 011](adr/011-org-encryption-mode.md))
- [x] Encrypted attachments ([ADR 013](adr/013-encrypted-attachments.md))
- [x] Mentions under E2EE — **server half only** ([ADR 014](adr/014-mentions-under-e2ee.md))

### Packaging — Phase 4, the shipped half

- [x] Second storage driver: `internal/sqlitestore`, 102 methods, a full `-race` leg per driver in CI
- [x] **Home mode** — single binary, SQLite, loopback, mints its own keys on first run ([ADR 012](adr/012-home-mode.md))
- [x] Signed releases (SBOM + cosign) and an updater whose only authority is `verify-release.sh`
- [x] Operator-triggered updates from the dashboard ([ADR 016](adr/016-operator-triggered-updates.md))
- [x] Operator backups, and a restore that verifies before it stops or writes anything
- [x] An installer covering four distribution families, idempotent
- [x] Admin-only second listener on its own port ([ADR 015](adr/015-admin-origin.md))
- [x] Docker-free layout test tier (`npm run e2e:layout`, about twenty seconds)

---

## 3. What is left

### 3a. Code tasks — ordered, and this order is the path

Taken from [OVERVIEW.md](OVERVIEW.md) "Where to pick this up", which is the authority.

- [ ] **1. Mentions under E2EE, the client half.** The composer never sends the declared list, so
      an encrypted mention **notifies nobody** while the picker and the rendered `@Name` both say
      otherwise. Smallest remaining slice, and the only one with a live user-visible bug behind
      it. → ROADMAP Phase 0 tasks, last entry
- [ ] **2. The admin design round.** Not a code task — see 3b. Three slices below it are blocked
      on it.
- [ ] **3. Multi-device key sync.** Phase 3's largest remaining piece: nothing carries device
      state between a person's own devices, so each browser profile enrols separately and starts
      with no history. → ROADMAP Phase 3
- [ ] **4. Compliance mode, the server side** — encryption at rest, retention policy, compliance
      export. Until these exist the mode stays **deliberately unselectable**, which is documented
      rather than broken. → [ADR 011](adr/011-org-encryption-mode.md)
- [ ] **5. The sub-5-minute install.** `install.sh` runs `compose up -d --build`, which is a Go
      compile plus a Vite build **on the stranger's own VPS**. That alone exceeds five minutes on
      a modest machine, and on a 1 GB one the web build is OOM-killed. The release pipeline
      already builds and signs images nobody pulls; pointing compose at the published image is
      the fix, and it also puts the signed-image supply chain on the path operators actually take.

**Blocked behind the admin design round (item 2):**

- [ ] Org logo — asked for in BRIEFS §3, never built; nothing stores, serves or draws one today
- [ ] Encryption-mode screen reskin
- [ ] Provisioning-token screen reskin

**Smaller open items, from the ROADMAP checkboxes:**

- [ ] New-device login notification (the Sessions remainder)
- [ ] WebAuthn / passkeys
- [ ] Up to ten argon2 verifications run inside one open transaction holding a row lock
- [ ] The invite token rides the URL path, while the reset token deliberately does not
- [ ] Desktop smoke e2e — see 3d; partly not writable as scoped
- [ ] `get.hamlaneh.com` does not serve the installer
- [ ] No correct configuration is documented for fronting the stack with a CDN or upstream proxy
- [ ] Weekly fuzz runs of at least 4 CPU-hours per parser (Phase 5)

### 3b. Not code — these are Amir's, and nothing in the tree blocks them

- [ ] **Deliver the admin designs.** The pipeline prompt is written and waiting at
      [`CLAUDE_DESIGN_ADMIN_ADDENDUM_PROMPT.md`](design/CLAUDE_DESIGN_ADMIN_ADDENDUM_PROMPT.md);
      the requirements are [BRIEFS.md](design/BRIEFS.md) §3 addendum. **Five admin surfaces ship
      unstyled today** — encryption mode, its switch dialog, provisioning tokens, the
      just-in-time toggle, and the whole set at phone width. [STATUS.md](design/STATUS.md) is the
      authority on what still says `awaiting-design`.
- [ ] **Phase 1 gate — daily-drive Hamlaneh for 2 consecutive weeks.** At least one real
      conversation and five messages per day, one file and one search per week, a brief daily
      log. Any day it had to be abandoned for another tool **resets the clock**.
- [ ] **Phase 2 gate — the manual NAT drill.** A full meeting with video and screen share across
      two genuinely hostile networks, flowing for five continuous minutes. The procedure is
      written and waiting: [`drills/nat-drill.md`](drills/nat-drill.md)
- [ ] **Phase 3 gate 1(b) — the media E2EE drill.** Buildable, never run.
      [`drills/e2ee-drill.md`](drills/e2ee-drill.md)
- [ ] **Phase 3 gate 2 — the key-loss and recovery drill.** Both halves are built; what remains
      is a real device loss, a real recovery, and the documented non-lying failure path for a
      user who never kept a key.
- [ ] **Phase 4 gate 1 — time at least 3 strangers on fresh VPSes**, median under five minutes.
      Blocked on code item 5 being real first.
- [ ] **Run the install matrix on real VMs.** Containers have no systemd and no Docker of their
      own, so they prove detection and idempotency and nothing at all about installing.

### 3c. Accepted limitations — recorded, not unfinished work

Listed here so nobody re-opens them as bugs.

- Search matches **characters, not word stems**, in either language — `رفتم` does not find `می‌رود`
- A search snippet is the **whole message**, because the contract's parts array has to reconstruct it
- **TOTP secrets are stored raw.** They cannot be hashed, since verification needs the secret;
  proper key management is deferred to Phase 5
- **One MLS device per browser profile**, shared across tabs
- A message sealed before a device joined the group can never be opened by that device

### 3d. Known-incomplete, written down rather than hidden

- **Keyless signing has never run.** Fulcio, Rekor and identity matching cannot be exercised
  offline, so the first real tag is where a wrong identity pattern would surface. The pipeline
  verifies both halves of its own output, which makes that a CI failure rather than an
  operator's.
- **The desktop app builds on three platforms in CI and is smoke-tested on none of them.**
  Tauri's WebDriver support is Linux and Windows only, and the e2e stack's internal-CA
  certificate is refused by the system webview with no in-app override. It needs the CA in each
  runner's trust store, or a home-mode HTTP instance for the desktop leg. The macOS leg is arm64
  only and every bundle is unsigned — both written into the workflow rather than left implicit.

---

## 4. Gate status at a glance

| Gate | Status |
|---|---|
| Phase 0 — fresh VM boots, CI fails correctly, install.sh idempotent, README verbatim in ≤15 min | met |
| Phase 1 — 2 weeks of daily-driving | **user's task** |
| Phase 1 — authz/IDOR matrix, XSS, SSRF, e2e in both locales, `verify-defaults.sh` | met |
| Phase 2 — the manual NAT drill | **user's task** |
| Phase 2 — relay-only TURN test, call on a fresh compose, bilingual call screens | met |
| Phase 3 1(a) — message canary is ciphertext only in a real `pg_dump` | met, automated |
| Phase 3 1(b) — a no-key subscriber cannot decode a `chan-` call | buildable, **never run** |
| Phase 3 2 — key-loss and recovery drill | buildable, **never run** |
| Phase 3 3 — mode choice is irreversible-safe | met **by construction** (nothing converts) |
| Phase 4 1 — 3 strangers, median under 5 minutes | **at structural risk** — see 3a item 5 |
| Phase 4 2 — a signed release applies, a tampered one is rejected, an older one needs force | met |
| Phase 4 3 — restore drill with the four-assertion encryption check | not run |
| Phase 4 4 — home mode on 3 OSes; Tauri builds **and passes a login + send smoke** | builds yes, smoke no |

---

## 5. Open questions

### Tracked, not blocking

[ROADMAP.md](ROADMAP.md) "Open questions" is the authority.

| Question | Decide by |
|---|---|
| Jalali (Shamsi) calendar for the `fa` locale — dates and date-pickers | Phase 1.5 |
| Dedicated non-superuser Postgres runtime role (the app connects as superuser today) | Phase 1, before 1.2 ships |
| Mobile push architecture, with metadata minimization | Phase 3 spike |
| Opt-in version telemetry design | Phase 4 |
| Company formation timing and structure | Before the first paying customer |
| Cloud jurisdiction and data residency | Phase 6 |
| Pricing numbers | Managed pre-sales (Phase 4) |
| DCO final confirmation (`git commit -s`) | Before the first external PR |

### Design questions waiting on Amir

Raised by the delivered sheets and unanswerable by the shell. [STATUS.md](design/STATUS.md)
holds the full text of each.

- **Channel actions in a DM** — the sheet says absent, but the DM branch is the only calm entry
  to the E2EE verification sheet, so today it renders there when verification is available
- **The Admin row in the account menu** — the sheet says Admin "stays separate", but no separate
  Admin control exists, so removing the row would remove the only route to `/admin`
- **The encrypted-call indicator** — how a true claim about content sits beside an *absent*
  claim about metadata, without either being read as the other

---

## 6. Where the authority actually lives

| Question | Read this |
|---|---|
| What does the project do today? | [OVERVIEW.md](OVERVIEW.md) — updated every slice |
| What do I work on next? | [OVERVIEW.md](OVERVIEW.md), "Where to pick this up" |
| Task-level state and the phase gates | [ROADMAP.md](ROADMAP.md) |
| Why is it built this way? | [PLAN.md](PLAN.md) and [adr/](adr/) — 16 records |
| How do I work on it? | [CLAUDE.md](../CLAUDE.md) — stack, workflow, testing, git, CI |
| Which screens have designs? | [design/STATUS.md](design/STATUS.md) |
| What must a screen contain? | [design/BRIEFS.md](design/BRIEFS.md) |
| The API contract | [api/openapi.yaml](api/openapi.yaml) and [api/ws-protocol.md](api/ws-protocol.md) |
| Cutting a release | [releasing.md](releasing.md) |
| Backups and restore | [backups.md](backups.md) · Hardening: [hardening.md](hardening.md) |
| The unrun drills | [drills/nat-drill.md](drills/nat-drill.md) · [drills/e2ee-drill.md](drills/e2ee-drill.md) |
