# Hamlaneh — Executable Roadmap

Companion to [PLAN.md](PLAN.md) §9. PLAN.md holds strategy; this file holds execution:
concrete tasks, orders of work, and — most importantly — **test gates**. A phase is not done
when its code exists; it is done when its gate passes. Gates are measurable, not vibes.

Rules of engagement:

- Work in vertical slices: each slice = API contract → backend + frontend (parallel agents) →
  integration → security review → merge. See CLAUDE.md "Development workflow".
- Every checkbox below implies "with tests" — untested work does not check a box.
- When reality disagrees with this file, update this file in the same PR.

**Bilingual requirement (decided, new since PLAN.md v1.0):** the app UI ships in English
(default) and Persian with full RTL, from the first screen. i18n is Phase 1 infrastructure,
not a retrofit. All repo documents remain English.

---

## Phase 0 — Foundation & walking skeleton (~2–4 weeks)

Goal: a fresh machine can boot a TLS-secured login page with one command, and every CI gate
that later phases depend on already exists and can turn red.

### Tasks

- [ ] Namespace grabs (user): GitHub org `hamlaneh`, Docker Hub, social handles. Domain: done.
- [x] Repo hygiene: `LICENSE` (AGPL-3.0), `README.md` (pitch + "How Hamlaneh makes money"),
      `SECURITY.md` (disclosure policy **+ published threat model incl. explicit non-goals per
      PLAN.md §6.1**), `CONTRIBUTING.md`, `CODEOWNERS`, issue/PR templates, `.gitignore`, `.env.example`
- [x] Monorepo scaffold: `server/` (Go module, `cmd/hamlaneh-server`, `internal/`),
      `webapp/` (Vite + React + TS + Tailwind, strict mode), `deploy/`; fill CLAUDE.md
      "Commands & toolchain" with the real values
- [ ] API contract bootstrap: `docs/api/openapi.yaml` skeleton (`/healthz` + auth endpoint stubs);
      codegen wired — `oapi-codegen` (Go types/handlers), `openapi-typescript` (webapp client),
      MSW mocks from spec; CI fails on codegen drift
- [ ] Migrations: `golang-migrate` wired; `server/internal/storage/migrations/` with migration 0001
- [x] i18n scaffold in webapp: i18next (or equivalent), `en`/`fa` locale files, RTL switching,
      locale key-parity CI check — before the first real screen exists
- [x] `deploy/docker-compose.yml`: Caddy (auto-TLS) → Go server → Postgres. Hardening baked in:
      non-root containers, read-only FS, dropped capabilities, minimal/distroless images,
      DB not exposed to host network. Baseline security headers in Caddyfile: HSTS,
      `X-Content-Type-Options: nosniff`, `frame-ancestors 'none'`.
      **No static secrets in committed files** — `install.sh` generates random DB password and
      server keys at first boot into an untracked `.env`; compose references env vars only
- [x] `deploy/verify-defaults.sh`: scripted secure-defaults check (grows every phase) — starts
      with: HSTS header present, security headers present, db container publishes no host ports,
      all containers report non-root UID (11 checks passing locally, 2026-08-20)
- [x] `deploy/install.sh` v0: detect OS, install Docker if missing, ask domain/IP, boot the stack
      (written + shellcheck-clean; real-VM smoke = gate 3)
- [ ] CI (GitHub Actions): everything in CLAUDE.md "CI gates" incl. **compose-smoke** job and
      CI hygiene rules (SHA-pinned actions, minimal permissions); branch protection on `main`
      — *workflow files written and validated; activates once the GitHub remote exists*
- [x] Walking skeleton: Go server serves a static login page (no real auth yet) + `/healthz`
      (booted locally via compose: TLS + all security headers + healthz verified, 2026-08-20)
- [ ] Weekend recon (user + Claude): deploy Mattermost, Rocket.Chat, Element+Jitsi; write
      `docs/recon-notes.md` — every friction point becomes install-experience spec

### Test gate ✅

1. Fresh VM/VPS: `docker compose up` → `https://<domain>/` shows the login page with a valid
   certificate **and the baseline security headers** (`curl -sI` check, scripted). Repeat with
   bare IP (self-signed/internal CA path documented).
2. CI is green and **fails** when: a test fails, a secret is committed (gitleaks test), codegen
   drifts, or `fa`/`en` locale keys diverge — inject each failure once to prove the gate works.
   A scripted scan proves the repo contains no working credential.
3. `install.sh` smoke: succeeds on one fresh Ubuntu LTS VM; a second run is a clean no-op
   (idempotent); on an unsupported OS it exits non-zero with a clear message instead of
   half-installing.
4. You, on a fresh VM you have never configured, following README.md **verbatim** (no memory
   shortcuts — any deviation is a README bug, filed and fixed), boot the skeleton in ≤ 15 minutes.
   (Real strangers are the Phase 4 gate.)

---

## Phase 1 — Chat core (~2–3 months)

Goal: Hamlaneh replaces the user's daily chat tool. Secure defaults built in from the start —
retrofit is how security fails.

### 1.1 Identity & sessions

- [ ] Signup/login: argon2id, rate-limited, public registration **off by default**
- [ ] Password reset: email-delivered, single-use, time-limited tokens; rate-limited; uniform
      responses (no account enumeration); completing a reset invalidates all sessions
- [ ] Sessions: short-lived access + rotating refresh tokens **with reuse detection (family
      revocation)**; refresh token in HttpOnly+Secure+SameSite cookie (or an explicitly
      documented header-only scheme); CSRF defense decided and documented (SameSite + custom
      header for mutations); device list; remote revocation; new-device login notification
- [ ] 2FA: TOTP first (attempt-limited), then WebAuthn/passkeys; org policy to enforce 2FA
- [ ] Admin bootstrap flow (first user = admin, created via install, not open signup)
- [ ] **Authz matrix harness** (see CLAUDE.md testing policy): table-driven, REST + WS entries,
      completeness machine-checked against `openapi.yaml` in CI
- Tests: registration-off negative (signup returns 403 on fresh default instance);
  credential-stuffing/rate-limit tests (login, signup, reset, TOTP — assert 429/lockout);
  refresh-token replay → family revoked; no session token usable before the 2FA step completes;
  session fixation/expiry/revocation; reset-token reuse/expiry + enumeration (identical
  response for existing vs non-existing email); cookie-flag assertions + cross-site POST cannot
  mutate state; admin-vs-member authz matrix on every admin endpoint

### 1.2 Messaging core

- [ ] Orgs → teams → channels (public/private) → DMs/group DMs; membership and roles
- [ ] WebSocket gateway: message delivery, presence, typing, read receipts, reconnect/resume.
      Handshake validates Origin (CSWSH defense); auth via cookie/header or short-lived one-time
      ticket — never a long-lived token in the URL query string
- [ ] Message history, edit, delete, pagination; markdown through strict sanitizer
- [ ] Strict baseline CSP on all app responses: `default-src 'self'`, no
      `unsafe-inline`/`unsafe-eval`, `object-src 'none'`
- [ ] Generic rate-limit middleware with per-endpoint budgets: message send, upload,
      WS connect/reconnect, TOTP verify
- Tests: **IDOR matrix over REST and WS** (user A must never read/write channel B — automated
  via the harness); **WS security suite**: (a) connect without session rejected, (b) expired/
  revoked session rejected, (c) open socket terminated ≤10s after remote revocation,
  (d) subscribe/send to non-member channel rejected, (e) presence/typing/read/message events of
  a private channel never reach a listening non-member socket, (f) reconnect/resume delivers
  missed messages exactly once, (g) disallowed Origin rejected; XSS corpus against the
  sanitizer; CSP header regression test (no inline/eval); message-send and WS-reconnect flood
  tests; race-detector clean under concurrent send/edit/delete

### 1.3 Files, previews, search

- [ ] Upload pipeline: content-type enforcement, size caps, EXIF strip, sandboxed processing.
      Served from a **separate cookie-less origin** (e.g. `files.<domain>`, provisioned by
      Caddy); where a separate origin is impossible (bare-IP/home mode), serve uploads with
      `Content-Disposition: attachment` + `nosniff` + sandboxing CSP
- [ ] Link previews via isolated egress proxy: private-IP ranges blocked, timeouts, size caps
- [ ] Message + file search (Postgres FTS to start)
- Tests: upload negatives — over-cap → 413; content-type spoof (EXE bytes as image/png)
  rejected; uploaded SVG/HTML/XML **never executes script** (Playwright, both serving modes);
  path traversal (`../../`) neutralized; zip/decompression bomb bounded; EXIF GPS absent from
  stored derivative. Upload fuzz (malformed images/archives). SSRF suite (169.254.x, 10.x,
  redirect-to-private, DNS rebinding). Search authz (results never leak channels the user
  can't see)

### 1.4 Admin panel & org policies

- [ ] Admin panel on separate path, optional IP allow-list; org settings (2FA enforcement,
      session lifetime, password policy, registration mode)
- [ ] Tamper-evident audit log (hash-chained/HMAC; logins, admin actions, exports) — schema
      designed now, SIEM export later
- Tests: every admin mutation appears in the audit log; directly mutating an audit row in the
  DB → chain verification fails and reports the break; with allow-listing on, admin request
  from a non-allowed IP rejected even with a valid admin session; non-admin cannot reach any
  admin route (matrix)

### 1.5 Bilingual UI + PWA baseline

- [ ] All Phase 1 screens fully translated (`fa`), RTL-correct, no hard-coded strings
- [ ] Language switcher; per-user locale preference
- [ ] PWA baseline: web manifest, installability, mobile-responsive pass (interim mobile story
      per PLAN.md §11 until native apps)
- Tests: e2e per the CLAUDE.md tiering (full `en` + `fa` smoke per PR; full both nightly);
  CI fails on locale key divergence; **RTL snapshot tests**: Playwright in `fa` asserts
  `<html dir="rtl" lang="fa">` and compares committed screenshots of the 5 core screens —
  login, channel list, message view, user settings, admin panel — failing on unapproved diffs

### 1.6 Enterprise identity (may overlap Phase 2–4, must ship before public launch)

- [ ] SSO: OIDC first (SAML post-v1 unless a Managed pre-sale demands it) — free, per PLAN.md §6.3
- [ ] SCIM provisioning — deprovisioning kills all sessions/devices instantly
- Tests: SCIM deprovision → every session and WS socket of that user dead within 60s;
  SSO-created users still subject to org 2FA policy; SSO cannot bypass registration-off logic

### Test gate ✅

1. **The user daily-drives Hamlaneh for 2+ consecutive weeks**, defined as: ≥1 real conversation
   and ≥5 messages sent per day, ≥1 file shared and ≥1 search per week, brief daily log kept.
   Any day Hamlaneh had to be abandoned for another tool resets the clock.
2. Authz/IDOR matrix green in CI **with machine-checked completeness** (every `openapi.yaml`
   endpoint × method + every WS path has a matrix entry; CI fails otherwise). XSS corpus and
   SSRF suite green.
3. E2e green per tiering (full suite, both locales, on the nightly/pre-merge lane).
4. `deploy/verify-defaults.sh` exits 0 against a fresh `docker compose up` — now also asserting:
   signup 403 by default, admin routes 401/404 for non-admins, CSP present without
   inline/eval.

---

## Phase 2 — Calls & meetings (~1–2 months)

Goal: 1:1 and group calls that survive real-world NATs.

- [ ] LiveKit integration: server-side room/token service — LiveKit API keys never leave the server
- [ ] 1:1 calls from DMs; group calls in channels; conference rooms with links
- [ ] Screen share; mute/camera controls; active-speaker UI
- [ ] TURN (coturn or LiveKit embedded) in the compose stack, auto-configured by install.sh
- Tests: token service authz via the matrix harness (no token for rooms you're not in); token
  negatives — expired rejected, tampered signature rejected, token for room X rejected at room Y;
  **key-leak scan in CI**: LiveKit API key/secret never appears in any HTTP response, WS
  message, or built webapp bundle; room lifecycle integration tests; **automated TURN test**:
  a client forced to `relay`-only ICE policy completes a call against the compose stack in CI

### Test gate ✅

1. Manual drill: a full meeting (video + screen share) between two machines on different
   networks that break naive WebRTC (e.g., corporate NAT / CGNAT mobile hotspot), flowing for
   **5 continuous minutes**; the exact network pair is documented in `docs/drills/nat-drill.md`.
2. The relay-only automated TURN test is green in CI.
3. Call still works when `docker compose up` is run fresh with only domain/IP input.
4. Bilingual UI holds (call screens translated, RTL-correct, in the snapshot suite).

---

## Phase 3 — E2EE (~2–3 months, hardest phase)

Goal: a compromised server yields only ciphertext. Assemble, never invent.

- [ ] Library pick finalized (OpenMLS vs libsignal): short spike comparing maturity, audit
      status, multi-device story → ADR
- [ ] MLS for messages; group state management; member add/remove flows
- [ ] Multi-device: per-device keys, device verification, key sync
- [ ] Key verification UX: safety numbers/QR (key transparency log = post-v1)
- [ ] Encrypted backups + user-held recovery keys; org-level recovery policy; recovery UX drills
- [ ] Media E2EE via LiveKit insertable streams
- [ ] Per-org mode choice at setup: **Strict E2EE** vs **Compliance mode** — clearly labeled,
      documented bluntly (search/export/retention impossible in Strict, by design)
- [ ] Compliance-mode server-side half actually built: encryption at rest, retention policy,
      compliance export (promised free in PLAN.md §7 — a mode toggle without them is dishonest)
- [ ] Research spike: mobile push architecture with metadata minimization (decision in PLAN.md §12)
- Tests: **MLS integration tests** (the glue, not the audited library): two independent client
  instances create a group, exchange, persist state, restart, resume; a removed member cannot
  decrypt anything sent after removal; a new member cannot decrypt anything sent before joining;
  group state survives server restart and epoch advancement. **Downgrade test**: server cannot
  silently flip an E2EE room to plaintext. **Key-swap test**: an adversarial test server
  substitutes a device key mid-conversation → client surfaces a verification warning and refuses
  to silently encrypt to the new key. Multi-device join/leave/rejoin matrix. Backup restore drill.

### Test gate ✅

1. **Compromised-server drill**, scripted in `docs/drills/e2ee-drill.md`: (a) send a known
   canary message; full DB + disk dump is scanned — the canary appears only as ciphertext;
   (b) media: packet capture at the SFU during a live call — RTP payloads fail to decode
   without the E2EE key.
2. **Key-loss drill**: honest user loses device → recovers with recovery key; user without a
   recovery key hits the documented, non-lying failure path.
3. Mode choice is irreversible-safe: switching modes can't silently decrypt or expose history.

---

## Phase 4 — Packaging polish (~1 month, overlaps 2–3)

Goal: median stranger, fresh VPS → working instance, **under 5 minutes, measured**.

- [ ] `install.sh` hardened: Ubuntu LTS, Debian, Fedora, RHEL-clone; idempotent re-runs; clear errors
- [ ] Installer served from `get.hamlaneh.com` (redirect/proxy to repo raw) — the documented
      one-liner uses this URL so tutorials never break
- [ ] Bare-IP mode polished; home mode: single binary with SQLite (storage suite now runs
      against **both** drivers in CI, per CLAUDE.md); Tauri desktop app builds
- [ ] Signed releases (Sigstore/cosign) + SBOM; auto-update channel, on by default for security
      patches, **with anti-rollback** (older validly-signed release refused unless forced)
- [ ] Automated encrypted backups on by default; documented restore
- [ ] Publish `docs/hardening.md` (defaults already carry the load; guide covers optional
      extras: IP allow-lists, reverse-proxy variants, backup key custody)
- [ ] Start pre-selling Managed to interested orgs (validates pricing, near-zero build cost)
- Tests: install matrix in **real VMs, not containers** (installer touches Docker + systemd —
  containers test nothing; use nested-virt CI runners or scheduled cloud VMs): Ubuntu LTS,
  Debian, Fedora, RHEL-clone, each from a clean image, asserting healthz + login page +
  verify-defaults.sh; update→rollback drill; backup→restore drill

### Test gate ✅

1. ≥3 strangers, fresh VPS each, median time-to-working-instance **< 5 minutes** — timed, logged.
2. Auto-update applies a signed release; a tampered release is rejected; an **older
   validly-signed release is rejected** unless explicitly forced (anti-rollback negative test).
3. Restore drill on a fresh machine: pre-backup canary message present, file checksums match,
   existing users log in. Negatives: backup archive unreadable without key (scripted scan finds
   no plaintext canary); restore with wrong key fails with a clear error, not a corrupt instance.
4. Home mode: the single binary starts on Windows, macOS, Linux; first run creates the SQLite
   DB; a sent message survives process restart. Tauri app builds for all three OSes in CI and
   passes a login + send-message smoke e2e.

→ **Public launch happens when this gate passes** (repo flips public, Show HN). Audits gate
security *marketing*, not launch.

---

## Phase 5 — Hardening & audit (ongoing; gate before "secure" marketing)

- [ ] Continuous, with red/green signals: every parser's fuzz target runs weekly ≥4 CPU-hours
      with zero unresolved crashes (crashes file blocking issues); automated header test keeps
      CSP free of inline/eval on every served page; rate-limit tests assert documented
      thresholds on login, signup, reset
- [ ] `security.txt`, disclosure policy, security contact, stated patch SLA
- [ ] External pentest → fix findings → publish report
- [ ] Cryptography audit of the E2EE integration → fix → publish

### Test gate ✅

Pentest + crypto audit completed, criticals/highs fixed, reports published. Only then does the
word "secure" enter marketing copy.

---

## Phase 6 — Hamlaneh Cloud

- [ ] Provisioning automation on top of install.sh; `*.hamlaneh.app`; instance-per-customer
- [ ] Merchant-of-record billing (Paddle/Lemon Squeezy); status page; Managed tier operations
- Tests: provision→suspend→resume→delete lifecycle; **tenant-isolation probe in every
  provisioning smoke test**: from a shell inside customer A's instance, connections to customer
  B's DB port, internal API, and hostname all fail (asserted by script)

### Test gate ✅

First self-serve customer signs up, pays, and gets a working instance with zero manual steps.

---

## Open questions (tracked, not blocking)

| Question | Decide by |
|---|---|
| Jalali (Shamsi) calendar support for `fa` locale — dates/date-pickers | Phase 1.5 |
| Mobile push architecture (metadata minimization) | Phase 3 spike |
| Opt-in version telemetry design | Phase 4 |
| Company formation timing/structure | Before first paying customer (Managed pre-sales, Phase 4) |
| Cloud jurisdiction / data residency | Phase 6 |
| Pricing numbers | Managed pre-sales (Phase 4) |
| DCO final confirmation | Before first external PR |
