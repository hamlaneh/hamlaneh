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
- [x] API contract bootstrap: `docs/api/openapi.yaml` skeleton (`/healthz` + auth endpoint stubs);
      codegen wired — `oapi-codegen` (Go types/handlers), `openapi-typescript` (webapp client),
      MSW mocks from spec; CI fails on codegen drift (both directions verified deterministic)
- [x] Migrations: `golang-migrate` wired (embedded, auto-run at startup); migration 0001 (users)
      applied + verified against live Postgres, 2026-08-20
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

- [x] Login: argon2id (t=3, m=64MiB, p=4), rate-limited per-IP and per-identifier, identical
      unknown-user/wrong-password responses, public registration **off by default** (no signup
      endpoint exists at all) — *slice 1.1a, 2026-08-21*
- [x] Password reset **backend**: email-delivered, single-use, time-limited (30m) tokens;
      rate-limited per address and per client; uniform responses (no account enumeration);
      completing a reset revokes every session family and sets no cookies — *slice 1.1b*
- [ ] Password reset **UI**: build the `reset-request`, `reset-request-confirmation` and
      `reset-new-password` artboards plus the `BackLink` component (design/LOGIN_HANDOFF.md)
- [x] Mail infrastructure the reset depends on: a `Mailer` interface with a recording fake for
      tests and an SMTP implementation wired in `cmd/`, dispatching asynchronously so SMTP
      latency never sits on a response; SMTP settings plus a public base URL in `.env.example`
      (the server has no other way to build an absolute reset link — Caddy owns the origin);
      a null mailer that logs and drops when SMTP is unconfigured — *slice 1.1b*
- [x] Server-side bilingual email templates need a language-policy amendment: the Persian
      exception in CLAUDE.md currently covers only `webapp/**/locales/fa/**` — widened to any
      localized user-facing template wherever it lives — *slice 1.1b*
- [ ] **Recovery codes have no sign-in entry point.** `login-totp` is six numeric cells; a
      `XXXX-XXXX` recovery code cannot be typed into it, so codes that exist entirely for
      account recovery are unreachable at the moment they are needed. The endpoint accepts them;
      the screen needs a design addendum before the feature is real
- [x] Sessions core: short-lived access (15m) + rotating refresh (30d) **with reuse detection
      (family revocation)**, opaque tokens stored as SHA-256; HttpOnly+Secure+SameSite=Strict
      cookies; CSRF double-submit via `X-Hamlaneh-CSRF`; change-password revokes all other
      families; forced password change for admin-created accounts — *slice 1.1a*
- [x] Session management **endpoints**: one row per live session family (device), current
      first; sign one device out or all the others; another account's family answers 404 so a
      guessed id confirms nothing — *slice 1.1b*
- [ ] Sessions remainder: device list UI; new-device login notification (email infra now
      exists); expired-row cleanup sweep; client reacts to an unrecoverable 401 mid-use by
      returning to sign-in; decide a short server-side grace window for concurrent refresh
      (two tabs racing trips family revocation) — before 1.2
- [x] `Retry-After` declared in the contract and emitted on the login, two-step and
      account-security 429s, so the sign-in form can show a real countdown instead of clearing
      its rate-limited state on the next edit — *slice 1.1b*
- [ ] `Retry-After` on the two password-reset 429s: `passwordreset.Service` returns
      `ErrRateLimited` without a duration, so those endpoints still answer without the header
- [x] `GET /api/v1/instance` **backend** carries `password_min_length`
      and `password_reset_available`, because a zero-config install has no SMTP and a
      "Forgot password?" link that silently goes nowhere is dishonest — *slice 1.1b*
- [ ] TOTP secrets are stored raw — they cannot be hashed, since verification needs the
      plaintext. Encrypting them at rest needs a key-management decision; revisit in Phase 5
- [ ] **Authenticator label waits for the admin dashboard (decided 2026-08-21).** The otpauth
      issuer is the constant `Hamlaneh`, so a user sees `Hamlaneh: username` in their
      authenticator. The design shows the organization name there, but no org-name setting
      exists until Phase 1.4. Deliberately not inventing a scheme now (a domain-derived label
      was considered and rejected): changing the issuer later rewrites the entry in **every**
      user's authenticator app, so it is decided once, when the real name exists. Must be
      settled before the first release that ships two-step verification to users.
- [x] 2FA **backend**, TOTP: three-step enrolment (setup → verify → activate), attempt-limited
      on both the setup and the sign-in code, ten single-use recovery codes stored argon2id;
      login answers 202 with a path-scoped challenge cookie and mints no session until the
      code verifies — *slice 1.1b*
- [x] 2FA **UI**: the `login-totp` screen and the `OtpInput` component — *slice 1.1b*
- [ ] Up to ten argon2 verifications run inside an open transaction holding two row locks on the
      recovery-code sign-in path (~0.8s of held connection). Bounded by the new per-IP limiter,
      but the consumption transaction is worth restructuring — Phase 5 hardening
- [ ] 2FA remainder: WebAuthn/passkeys; org policy to enforce 2FA (needs the admin dashboard)
- [x] JSON `ErrorHandlerFunc` replaces oapi-codegen's plain-text errors everywhere — *slice 1.1a*
- [x] Handlers enforce all contract constraints server-side (lengths, patterns, ranges, body
      size cap 64 KiB) — *slice 1.1a*
- [x] Authz matrix classifies `/api/v1/auth/refresh` deliberately (refresh-cookie-gated;
      works under the must-change gate) — *slice 1.1a*
- [x] Admin bootstrap flow: install.sh generates `HAMLANEH_ADMIN_*` into untracked `.env`;
      server creates the first admin only while the users table is empty (must-change-password
      set); every later user comes from the admin dashboard or an invite link (see 1.4) — *slice 1.1a*
- [x] **Authz matrix harness**: table-driven registry, every contract endpoint × 4 principals
      (anonymous / member / member-must-change / admin) against real server + DB; completeness
      gate parses `openapi.yaml` and fails on unregistered endpoints (WS entries arrive with
      1.2) — *slice 1.1a*; the 1.1b endpoints registered with real (non-stub) expectations
- Tests: registration-off negative (signup returns 403 on fresh default instance);
  credential-stuffing/rate-limit tests (login, signup, reset, TOTP — assert 429/lockout);
  refresh-token replay → family revoked; no session token usable before the 2FA step completes;
  session fixation/expiry/revocation; reset-token reuse/expiry + enumeration (identical
  response for existing vs non-existing email); cookie-flag assertions + cross-site POST cannot
  mutate state; admin-vs-member authz matrix on every admin endpoint

### 1.2 Messaging core

Split into two slices. The contract for both already exists and is deliberately not being
rewritten: `docs/api/openapi.yaml` carries every messaging path (their handlers answer 501
today), `docs/api/ws-protocol.md` is a complete realtime spec that the webapp's realtime client
already implements against mocks, and migration 0003 already models channels, DM pairs,
membership, messages with `client_msg_id`, soft delete, mentions and read positions. What is
missing is the server behind it.

#### 1.2a Thin end-to-end — chat that actually works

The smallest change that turns the mocked chat shell into a real one: pick a channel, read its
history, send a message, and have it appear on someone else's screen without a refresh.

- [x] Rework `httpserver` route-policy keying from exact method+path to `r.Pattern` before
      adding path-parameter routes — **landed early in 1.1b**, because the session-revocation
      route `DELETE /api/v1/sessions/{id}` was itself a path-parameter route and could not be
      classified without it. The table still fails closed: an unclassified pattern is refused,
      so a 1.2 route that nobody adds a policy for is unreachable rather than unguarded
- [ ] **Authz harness rework first, before any handler.** Today's four columns are
      instance-scoped (anonymous / member / member-must-change / admin) and cannot express the
      question this phase turns on: *member of which channel?* Needs channel-scoped principals
      (non-member / member / owner / admin-non-member / admin-member) over shared per-cell
      fixtures. This is the contract every later slice registers into, so it is designed once,
      deliberately, and not grown by accretion
- [ ] Channels (public/private) and 1:1 DMs; membership and roles. **The instance is the
      organization** — no org or team layer, no group DMs (see
      [ADR 001](adr/001-instance-as-org-flat-channels.md))
- [ ] Storage interface + Postgres driver for channels, membership and messages
- [ ] Message history, cursor-paged in both directions (`around` for permalinks lands with
      1.2b, when there is a permalink to resolve)
- [ ] Idempotent send: `client_msg_id` unique per (channel, author), so a message queued
      offline and resent after a reconnect lands exactly once. The unique index already exists
- [ ] WebSocket gateway per `docs/api/ws-protocol.md`: handshake with Origin validation (CSWSH
      defense), auth by cookie or short-lived one-time ticket — **never** a long-lived token in
      the query string — message delivery, presence, typing, heartbeat, reconnect/resume
- [ ] Open socket terminated ≤10s after its session is revoked. Specified in the protocol since
      1.1b and untestable until the gateway exists; it is a 1.2a exit condition, not a later one
- [ ] Markdown through the strict sanitizer, server side as well as client side
- [ ] Strict baseline CSP on all app responses: `default-src 'self'`, no
      `unsafe-inline`/`unsafe-eval`, `object-src 'none'`
- [ ] Generic rate-limit middleware with per-endpoint budgets, replacing the auth-specific
      limiters: message send, WS connect/reconnect, and the existing login/reset/TOTP budgets
- Tests: **IDOR matrix over REST and WS** (user A must never read or write channel B — automated
  via the harness, both protocols); **WS security suite**: (a) connect without session rejected,
  (b) expired/revoked session rejected, (c) open socket terminated ≤10s after remote revocation,
  (d) subscribe/send to non-member channel rejected, (e) presence/typing/message events of a
  private channel never reach a listening non-member socket, (f) reconnect/resume delivers
  missed messages exactly once, (g) disallowed Origin rejected; XSS corpus against the
  sanitizer; CSP header regression test; message-send and WS-reconnect flood tests;
  race-detector clean under concurrent send

#### 1.2b Everything the chat shell draws but 1.2a does not fill

- [ ] Message edit and soft delete (the design keeps a placeholder in place where a message was)
- [ ] Read positions per user per channel feeding the unread divider and sidebar counts;
      own-device read sync only — **no cross-user read receipts** (nothing in the design shows
      another person's read state; privacy default until designed)
- [ ] Message search (`kind=messages`), pulled forward from 1.3 because the delivered chat shell
      has a search column and shipping it dead is not an option; file search stays 1.3. Migration
      0003 deliberately left out the tsvector column and GIN index: the text-search configuration
      is language-dependent, the product is bilingual, and the choice is effectively frozen once
      an index is built on it — so it is made here, with the search code, not before it
- [ ] `around` cursor for permalinks
- Tests: search authz (results never leak channels the user cannot see); edit/delete authorship
  and admin rules in the matrix; unread counts under concurrent read/send

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

### 1.4 Admin dashboard & org policies

The dashboard is a first-class product surface (decided Aug 2026): it ships **inside the same
install** — same webapp, same binary, same compose stack, zero extra setup — and, because
public registration is off by default, it is **the** way users come into existence.

- [ ] Admin dashboard on separate path, optional IP allow-list
- [ ] User lifecycle from the dashboard: create users, generate invite links, deactivate/
      offboard (kills all sessions + sockets), unlock/reset access
- [ ] Role & permission management from the dashboard (org admin/member now; channel-level
      roles from 1.2 surface here)
- [ ] Org customization from the dashboard: org name, logo, default locale (`en`/`fa`),
      org-wide policies (2FA enforcement, session lifetime, password policy, registration mode)
- [ ] Tamper-evident audit log (hash-chained/HMAC; logins, admin actions, exports) — schema
      designed now, SIEM export later
- Tests: every admin mutation appears in the audit log; directly mutating an audit row in the
  DB → chain verification fails and reports the break; with allow-listing on, admin request
  from a non-allowed IP rejected even with a valid admin session; non-admin cannot reach any
  admin route or dashboard API (matrix); invite link is single-use and expires; deactivated
  user's live WS socket dies ≤10s

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
| Dedicated non-superuser Postgres runtime role (app connects as superuser today; extension creation needs owner split) | Phase 1, before 1.2 ships |
| Mobile push architecture (metadata minimization) | Phase 3 spike |
| Opt-in version telemetry design | Phase 4 |
| Company formation timing/structure | Before first paying customer (Managed pre-sales, Phase 4) |
| Cloud jurisdiction / data residency | Phase 6 |
| Pricing numbers | Managed pre-sales (Phase 4) |
| DCO final confirmation | Before first external PR |
