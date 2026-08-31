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

- [x] **The e2e suite arranges conversations the way Strict makes people arrange them** —
      *2026-08-31*. `sendMessageApi` could not be repaired, because under Strict no REST call
      produces a legal message; it is replaced by a helper that drives a real browser's MLS
      composer. `attachFile` now checks the upload status rather than accepting any response,
      which is what turned a silent 400 into a visible failure.

- [ ] **File sharing is unreachable on a fresh install — a headline feature, absent rather than
      degraded.** Two individually-correct decisions meet badly: Strict is the schema default
      (`0018_org_encryption_mode.up.sql` — `NOT NULL DEFAULT 'strict'`) and the only selectable
      mode, so every channel and DM is born encrypted; and an encrypted conversation refuses
      attachments (`e2ee_attachments_unsupported`) because encrypted attachments were never
      built, refusing rather than storing in the clear — which is the right call in isolation.
      Together they mean **there is no conversation on any install into which any file can be
      uploaded**, while `README.md` and `CLAUDE.md` both lead with file sharing. This needs
      encrypted attachments, and it is Phase 3 work that was missed rather than Phase 4 polish
- [ ] **Mentions notify nobody in an encrypted conversation.** `storage/messages.go` derives them
      with `ParseMentions(nm.Content)`, and on an encrypted message `content` is `""` by
      construction, so no `message_mentions` row is ever written. The composer still offers the
      picker, the recipient's client still renders `@Name` after decryption — so both people see
      a mention that happened, and only the badge meant to carry it is missing. Silent, and the
      fix is a design decision rather than a patch: the server cannot read the message, so either
      the client declares the mention list (metadata the server would then hold) or notification
      moves client-side. That is an ADR, not a bug fix
- [x] **The backup offer covering the call button** — *2026-08-31*, fixed at the cause rather
      than patched a third time. `.hm-plumbing` meant both "this has no design yet" and "this
      floats", so every undesigned surface inherited popover positioning whether or not it was a
      popover, and each earlier fix added another opt-out to the floating class instead of
      inverting the default. The base class no longer positions; `--overlay` is opted into by the
      six real popovers. The objection recorded against putting these in flow was real and
      misdiagnosed — `.hm-chat` is the sidebar/conversation *row*, so an in-flow child there
      becomes a third column. What stops a fourth recurrence is a Docker-free layout tier
      (`npm run e2e:layout`, ~20s) in which a reintroduced `absolute` fails **by class name**
      rather than by whichever control it happens to land on: the two specs that should have
      caught this need the runner to be the Docker host, which is why they never saw any of the
      three

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
- [x] `Retry-After` on the two password-reset 429s — `passwordreset.Service` reported an
      exhausted budget without saying for how long; refusals now carry the duration in a typed
      error that still unwraps to `ErrRateLimited`. Every 429 in the server goes through one
      function, so carrying the header is a property of the code rather than a habit — *1.2a*
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
- [x] **Ship the web application.** Found while starting this slice, not planned: nothing
      in `deploy/` ever ran `npm run build`, so `docker compose up` served the Phase 0
      placeholder page and none of the built UI existed outside `npm run dev` — a direct
      violation of "installation is the product". The image now builds `webapp/` and the
      Go binary embeds it, so home mode (Phase 4, no proxy) gets the same code path.
      The compose-smoke gate was passing against the placeholder because it discarded the
      response body; it now asserts the real bundle
- [x] **Baseline security headers, owned by the application.** A strict CSP existed, but
      only in the Caddyfile — so it would not have existed at all in home mode. The Go
      server now sets CSP (no `unsafe-inline`, no `unsafe-eval`), `Referrer-Policy:
      no-referrer`, `Permissions-Policy`, nosniff and the `Cross-Origin-*` pair around the
      whole handler; Caddy keeps HSTS, which only a TLS terminator can set meaningfully.
      Every directive was derived from the real build output rather than copied
- [x] **Playwright e2e against the real stack**, landed here rather than in 1.5 because this is
      the first slice where it was possible: until the web bundle shipped inside the binary there
      was no application to drive. Per-PR runs full `en` plus the `fa` smoke subset, nightly runs
      both in full, and the suite starts its own isolated compose stack so it can never touch a
      developer's volumes. Accounts come from the bootstrap path and the admin API — no test-only
      back door into the server. The RTL **snapshot** tests in 1.5 are still outstanding; this is
      the harness they will need
- [x] Consume `Retry-After` in the sign-in form. The header shipped in 1.1b and the frontend kept
      guessing ("try again in a few minutes") from a stale comment claiming the contract carried
      no such header. Found by the first e2e run, which read the real 429 through Caddy
- [x] **Authz harness rework first, before any handler.** Today's four columns are
      instance-scoped and cannot express the question this phase turns on: *member of which
      channel?* Designed in [ADR 002](adr/002-channel-scoped-authz-matrix.md): seven required
      columns, authorship refinements, per-cell fixture bundles, private+dm kind coverage with
      a single public tripwire row, and a completeness gate that refuses instance-scoped
      registration of any `{channelId}` operation
- [x] Channels (public/private) and 1:1 DMs; membership and roles. **The instance is the
      organization** — no org or team layer, no group DMs (see
      [ADR 001](adr/001-instance-as-org-flat-channels.md))
- [x] Storage interface + Postgres driver for channels, membership and messages
- [x] Message history, cursor-paged in both directions (`around` for permalinks lands with
      1.2b, when there is a permalink to resolve)
- [x] Idempotent send: `client_msg_id` unique per (channel, author), so a message queued
      offline and resent after a reconnect lands exactly once. The unique index already exists
- [x] Read positions, **moved here from 1.2b**: the contract makes `unread_count` and
      `mention_count` required on every `Channel`, and `GET /api/v1/channels` ships in this
      slice, so the alternative was emitting zeros the sidebar would draw as truth. Own-device
      read sync only — **no cross-user read receipts** (nothing in the design shows another
      person's read state; privacy default until designed)
- [x] WebSocket gateway per `docs/api/ws-protocol.md`: handshake with Origin validation (CSWSH
      defense), auth by cookie or short-lived one-time ticket — **never** a long-lived token in
      the query string — message delivery, presence, typing, heartbeat, reconnect/resume
- [x] Open socket terminated ≤10s after its session is revoked. Specified in the protocol since
      1.1b and untestable until the gateway exists; it is a 1.2a exit condition, not a later one
- [x] `dm_peer` on every DM `Channel`. Storage carries a DM's pair as two ids and nothing
      resolves them to a person, so a direct message currently reaches the sidebar unlabeled —
      the design draws a name and an avatar there. Optional in the schema, so it is
      contract-legal and still visibly broken. Belongs in the channel queries as a join, not as
      a lookup per sidebar row
- [x] `mention_count` is required on every `Channel` and was **always zero**: nothing writes
      `message_mentions`. The counting query is correct and has nothing to count, so the
      sidebar's filled "@" badge can never appear. Needs the mention parser on the send path —
      the contract already defines the literal form it parses
- [x] `member_added` is specified, implemented in the gateway and was **never emitted**: the REST
      handler that adds a member cannot name the person, so it announces nothing. Blocked on the
      same missing piece as `dm_peer` until `UserByID` reached the `Store` interface; now only
      the two announce calls are missing
- [x] WebSocket connect-flood budget — in the gateway, not the request-scoped table, because
      §8 keys it per session family **and** per IP and both windows outlive any single request.
      Refused with HTTP 429 and a `Retry-After` on the handshake. Both halves are checked before
      either is recorded, so a refusal never counts an attempt that was not admitted — the same
      mistake `internal/passwordreset` already avoids — and the wait is the longer of the two.
      **`4429` turned out to be unreachable by design** and is now documented as reserved: a
      connect budget decides before a socket exists, and the per-frame budgets keep the socket
      open on purpose, because a subscribe-storm is a client bug rather than grounds to drop a
      connection somebody is reading in. §8 says so now instead of naming a code nothing sends
- [x] Markdown through the strict sanitizer, server side as well as client side — **restated,
      because the original wording asked for the wrong thing.** The server stores markdown *as
      authored*, which is what the contract says a message is, and it renders none: the web
      client renders through `react-markdown` with an allowlist and raw HTML never parsed, and
      search snippets are a `{text, match}` parts array the client draws. Rewriting markdown on
      the way in would corrupt what somebody wrote, irreversibly, to defend a rendering step
      this server does not perform.
      What the server does owe is that text it accepts can be stored and handed back unchanged,
      and that promise was being broken: a message containing a NUL passed every check and
      failed inside PostgreSQL with SQLSTATE 22021, which the handler could only answer as a
      500. Every field a client writes prose into — message content on send *and* edit, a
      channel topic on create *and* update, the search needle, the directory filter — now
      refuses unstorable text with a 400. Persian's zero-width non-joiner, bidi isolates,
      tabs, newlines and emoji are all accepted, pinned by their own test, because a validator
      that refuses what people actually type is worse than none
- [x] Strict baseline CSP on all app responses: `default-src 'self'`, no
      `unsafe-inline`/`unsafe-eval`, `object-src 'none'`
- [x] Generic rate-limit middleware with per-endpoint budgets — a table keyed on `r.Pattern`
      like the route-policy table, and **fails closed** the same way: an endpoint nobody
      classified is refused, and a completeness gate makes that a red build rather than a
      production 500. A second gate catches the fail-*open* variant a completeness test cannot
      see, where a route names a budget that has no spec and so misses the limiter map entirely.
      It runs after authentication (a per-account key needs an account) and before the handler
      (a 429 written after the argon2id verification has already paid for what it refused).
      The login, two-step and password-reset budgets stay where they are, deliberately: each
      spends conditionally, or against a key that lives in the request body, or in a shape that
      exists to keep the 429 from becoming an enumeration oracle — moving them into a tidier
      place would have quietly changed what they protect
- [x] The **WS connect/reconnect** half of that budget lives in the gateway — see the item above
- Tests: **IDOR matrix over REST and WS** (user A must never read or write channel B — automated
  via the harness, both protocols); **WS security suite**: (a) connect without session rejected,
  (b) expired/revoked session rejected, (c) open socket terminated ≤10s after remote revocation,
  (d) subscribe/send to non-member channel rejected, (e) presence/typing/message events of a
  private channel never reach a listening non-member socket, (f) reconnect/resume delivers
  missed messages exactly once, (g) disallowed Origin rejected; XSS corpus against the
  sanitizer; CSP header regression test; message-send and WS-reconnect flood tests;
  race-detector clean under concurrent send

- [x] Two screens state something false, both found by the first end-to-end run and both fixed
      with copy in the elements that already existed — the artboards for both states are filed in
      `docs/design/STATUS.md` as `awaiting-design`:
      **(a)** opening `/c/{id}` for a channel you are not in renders "You are not in any
      conversation yet. Create a channel to start one." beside a sidebar listing your
      conversations. It leaks nothing — a channel that exists and one that does not answer
      identically — but it is a lie, and it needs its own string plus a way back.
      **(b)** an empty **direct message** renders the *channel* empty state: "This is the
      beginning of #" with the slug a DM does not have, "Invite people" and "Set a topic" — both
      of which the server refuses with 400 on a DM — and "Only you can see this channel until
      someone is invited", which is false of a conversation with another person in it

#### 1.2b Everything the chat shell draws but 1.2a does not fill

- [x] Message edit and soft delete (the design keeps a placeholder in place where a message was)
- [x] Message search (`kind=messages`), pulled forward from 1.3. The configuration decision
      migration 0003 deferred is made in **migration 0006**, with the reasoning in its header:
      **trigram substring matching (`pg_trgm`), not tsvector**, because every FTS option required
      picking a language and PostgreSQL ships no Persian configuration — `english` would have
      given half the users whole-word-only matching. Substring is also the only choice consistent
      with the contract's snippet shape, which marks the run the query matched: a stemmed match
      has no characters to point at. Scope is a join inside the query, so a non-member's message
      cannot reach the results, the count or a snippet
- [ ] **Persian search does not stem** — `رفتم` does not find `می‌رود`, and the same applies to
      English (`deploying` does not find `deploy`). Character matching, not meaning. Pinned by a
      test so the limitation is documented rather than discovered. Fixing it needs a Persian
      stemmer PostgreSQL does not ship; that is a future migration replacing the 0006 index, not
      a tweak to it
- [ ] A search snippet is the **whole message** — the contract's parts array has to reconstruct
      the original text, so a windowed snippet would break it. At `limit=50` × 4000 characters
      that is ~200 KB worst case per page. Not a defect; a payload budget to revisit if pages
      get heavy
- [x] `around` cursor for permalinks — the anchor is **in** its page, unlike `before`/`after`,
      and at either end the page is simply shorter rather than widened on the far side: topping
      up would answer "this is the start of the channel" with a screenful of later messages
- [x] The contract's `last_member` refusal on `removeChannelMember`. It did need the deliberate
      design pass this line asked for, and the answer was the **lock strength**: `FOR NO KEY
      UPDATE` on the channels row, not `FOR UPDATE`. Two removals of one channel then serialize
      on it — without which both read a member count of two and both succeed, emptying the
      channel, which is write skew that READ COMMITTED does not prevent — while an
      `AddChannelMember` holding an uncommitted membership row and wanting `KEY SHARE` on the
      same channel never queues behind it, because `KEY SHARE` does not conflict with `FOR NO
      KEY UPDATE`. The isolation level is asked for explicitly rather than inherited, since at
      REPEATABLE READ the count would read the snapshot from before the wait. The `# Lock order`
      section now describes what is true instead of the hazard it replaced
- Tests: search authz (results never leak channels the user cannot see); edit/delete authorship
  and admin rules in the matrix; unread counts under concurrent read/send

### 1.3 Files, previews, file search

The contract is finalized (openapi.yaml v0.4.0, migration 0007, and
[ADR 003](adr/003-file-serving-and-egress.md), which fixes the security model: one
authorization rule — a file is readable exactly by its channel's members; signed expiring URLs
as the credential on a cookie-less origin; bytes sniffed and labels decorative; images stripped
at ingest; blobs on the filesystem keyed by server ids only; a dial-time egress guard). Message
search shipped in 1.2b; this phase adds files and previews.

- [x] **Attachment storage + upload pipeline** (`POST /api/v1/channels/{channelId}/files`,
      today a deliberate 501 behind the matrix gate): size cap from the instance document,
      sniff-vs-label enforcement, EXIF strip, dimension caps before decode, thumbnails ≤512px,
      the 24-hour orphan sweep, and the send transaction claiming `attachment_ids` atomically —
      empty content allowed exactly when files ride along
- [x] **The files origin**: signed URL minting and verification, inline for proven images only,
      attachment+nosniff+sandbox CSP for everything else and for the whole bare-IP/home mode;
      Caddy provisioning for `files.<domain>`; the URL-signing key generated at
      install/first-run
- [x] **Link previews** behind the egress guard of ADR 003 (dial-time IP validation, redirect
      re-checks, timeouts, size caps, ports 80/443 only); preview images re-hosted as bounded
      derivatives; enrichment async, announced by `message_updated` without `edited_at`
- [x] **File search** (`kind=files`): filename trigrams over the 0006 fold (index already in
      0007), membership-scoped by the same join, the `attachment` field on `SearchResult`
- [x] **The webapp's half**: the paperclip enabled at last, upload progress and failure per
      file, image/file cards fed by real attachments, the preview card fed by enrichment
- Tests: upload negatives — over-cap → 413; EXE bytes declared image/png → 415; uploaded
  SVG/HTML/XML **never executes script** (Playwright, both serving modes); traversal names
  neutralized structurally (server ids, proven by test); image bomb bounded before decode;
  EXIF GPS absent from the stored original *and* the thumbnail; upload fuzz (malformed
  images). SSRF suite: 169.254.x, 10.x, 127.x, fe80::, redirect-to-private, DNS rebinding
  (resolve-then-swap), IPv6 literals, port 22. Signed-URL suite: tampered signature, expired,
  variant swap, id swap → 404. File-search authz: a filename in a channel the caller cannot
  see never appears in results, count, or snippet

### 1.4 Admin dashboard & org policies

The dashboard is a first-class product surface (decided Aug 2026): it ships **inside the same
install** — same webapp, same binary, same compose stack, zero extra setup — and, because
public registration is off by default, it is **the** way users come into existence.

- [x] Admin dashboard on its own path (`/admin`), reachable only by admins
- [ ] Optional IP allow-list on the admin path — deferred: it protects an already
      session-and-role-gated surface, and on a self-hosted instance behind one Caddy the
      operator's own firewall is the better place for it. Revisit if hosted multi-tenant lands
- [x] User lifecycle from the dashboard: create users, generate invite links, deactivate/
      offboard (kills all sessions + sockets), unlock/reset access
- [x] Role & permission management from the dashboard (org admin/member now; channel-level
      roles from 1.2 surface here)
- [x] Org customization from the dashboard: org name, logo, default locale (`en`/`fa`),
      org-wide policies (2FA enforcement, session lifetime, password policy, registration mode)
- [x] Tamper-evident audit log (hash-chained/HMAC; logins, admin actions, exports) — schema
      designed now, SIEM export later
- [ ] **The invite token rides the URL path**, while the reset token deliberately rides a
      fragment so it never reaches a server's access log. The implementation keeps the *link*
      fragment-based (`{origin}/invite#token=…`), so a browser never sends it in a document
      request — but `GET`/`POST /api/v1/invites/{token}` still puts it in a path, where a
      proxy logs it. An invite is a lower-value credential than a reset token (it creates an
      account rather than taking one over) and it is single-use, which is why this is recorded
      rather than treated as a defect. Moving it to a header or a body would be a contract
      change; decide before the first public release
- Tests: every admin mutation appears in the audit log; directly mutating an audit row in the
  DB → chain verification fails and reports the break; with allow-listing on, admin request
  from a non-allowed IP rejected even with a valid admin session; non-admin cannot reach any
  admin route or dashboard API (matrix); invite link is single-use and expires; deactivated
  user's live WS socket dies ≤10s

### 1.5 Bilingual UI + PWA baseline

- [x] All Phase 1 screens fully translated (`fa`), RTL-correct, no hard-coded strings
- [x] Language switcher; per-user locale preference
- [x] PWA baseline: web manifest, installability, mobile-responsive pass (interim mobile story
      per PLAN.md §11 until native apps) — no service worker, deliberately: cookie auth plus a
      realtime socket makes a cached shell a correctness hazard, not an offline story
- Tests: e2e per the CLAUDE.md tiering (full `en` + `fa` smoke per PR; full both nightly);
  CI fails on locale key divergence; **RTL snapshot tests**: Playwright in `fa` asserts
  `<html dir="rtl" lang="fa">` and compares committed screenshots of the 5 core screens —
  login, channel list, message view, user settings, admin panel — failing on unapproved diffs

### 1.6 Enterprise identity (may overlap Phase 2–4, must ship before public launch)

- [x] **Org policies actually bind** (pre-work, no SSO code — see `docs/adr/004-enterprise-identity.md`).
      `require_totp` and `session_lifetime_hours` are stored and editable since 1.4 and read by
      nothing: an admin turns on enforced two-step, the screen agrees, and the instance does not
      change. Enforcement lands per session at mint, so flipping the policy strands no live
      session. Sign-ins become audited at the same time — they are not today.
- [x] SSO: OIDC first (SAML post-v1 unless a Managed pre-sale demands it) — free, per PLAN.md §6.3
- [x] SCIM provisioning — deprovisioning kills all sessions/devices instantly
- [x] SSO just-in-time provisioning, its own org setting, default off — last on purpose: it is
      the widest door, and it lands after every gate it must respect is tested
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
   no self-serve signup route accepts a caller, admin routes refuse anonymous callers, CSP
   present without inline/eval.

   Two corrections to what this item used to say, both from finding out what was actually
   built. It asked for "signup 403": there is no self-serve signup endpoint in the contract at
   all — `registration_mode` has never had a door behind it — so the script asserts the stronger
   thing, that the endpoint does not exist, and fails loudly if somebody adds one without
   deciding what a closed instance answers. It also asked for "401/404 for non-admins", which
   needs a signed-in non-admin; that case is the authz matrix's job and it is covered there for
   every admin route, so duplicating it here would be a worse copy of a better test.

---

## Phase 2 — Calls & meetings (~1–2 months)

Goal: 1:1 and group calls that survive real-world NATs.

- [x] LiveKit integration: server-side room/token service — LiveKit API keys never leave the server
- [x] 1:1 calls from DMs; group calls in channels; conference rooms with links
- [x] Screen share; mute/camera controls; active-speaker UI — with the leak warning the
      artboards draw, because the state people forget is that they are still sharing
- [x] TURN (LiveKit embedded — see ADR 005 for why, and it is credentials rather than features) in the compose stack, auto-configured by install.sh
- Tests: token service authz via the matrix harness (no token for rooms you're not in); token
  negatives — expired rejected, tampered signature rejected, token for room X rejected at room Y;
  **key-leak scan in CI** ✅; room lifecycle integration tests; **automated TURN test** ✅.

  Two notes on what those two gates actually assert, because both could be written to pass
  without meaning anything. The key-leak scan asserts the **secret** absent in eight encodings,
  and deliberately does *not* assert the key absent from the wire — the key is every token's
  issuer, so failing on it would fail on the protocol working correctly. It carries two controls:
  the run must observe at least one issued token, or a clean scan is vacuous, and the scanner
  must flag a synthetic secret in all eight encodings. The relay test asserts the nominated
  pair's local candidate is genuinely `relay` — forced in the peer-connection constructor, so it
  is measured rather than restated — and that media bytes grow. Its control probe asserts the
  relay port is **refused** rather than merely silent, because the published media port is
  reachable and silent, so "did not answer" is a bar an open port clears.

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

- [x] Library pick finalized: spike (`docs/spikes/mls-library.md`) →
      [ADR 006](adr/006-mls-library-and-boundaries.md) — **OpenMLS**, exact-pinned, on the
      `openmls_rust_crypto` provider, compiled to WASM for the browser client. The line used to
      say "OpenMLS vs libsignal"; the spike's first finding is that libsignal implements no MLS,
      so the real field was OpenMLS / mls-rs / ts-mls and the audit requirement decided it.
      The ADR also fixes two boundaries: the Go server stays MLS-blind (delivery + key-package
      directory, epoch sequencing on an unverified envelope claim), and conference guests are
      outside E2EE — the encryption boundary is the room kind, fixed at birth — *2026-08-29*
- [x] Integration spike **before the first contract freeze**
      ([`docs/spikes/mls-wasm-integration.md`](spikes/mls-wasm-integration.md)): 0.9.0 builds on
      `wasm32` (the 0.8.1 fallback is dead), two-client round-trip green **including a
      restore-from-serialized-state restart**, best bundle cost **489 KB gzipped** (`opt-level=z`
      + LTO, no `wasm-opt` — the post-pass shrinks raw bytes but *grows* gzipped), a Welcome is
      self-contained via `RatchetTreeExtension` so no tree-transfer endpoint is needed —
      *2026-08-29*
- [x] **Device-local keystore at rest** — *shipped with slice 3.1*. (Falls out of the spike, owned by the group-state
      slice): OpenMLS provider storage holds raw secrets as plaintext JSON, so persisting it to
      IndexedDB unwrapped persists key material in the clear. Needs a wrapping-key design plus
      honest labeling of what it does and does not resist — a browser cannot fully protect keys
      from its own profile, and pretending otherwise is the §6.9 violation. The wrapper
      implements the `openmls_traits` storage trait itself (the spike's write-through-public-field
      restore path is nobody's stability promise), bridging the sync trait to async IndexedDB
      with flush-at-commit-points
- [x] MLS for messages; group state management; member add/remove flows — slice 3.1,
      *2026-08-29*: contract v0.7.0 + migration 0017 (opaque-blob transport, ADR 006), the
      `webapp/src-mls` wasm core with its own storage-trait implementation and AES-GCM-wrapped
      IndexedDB keystore, group bootstrap/claim/commit/welcome flows, add-on-invite and
      remove-on-removal, and the e2ee write-path boundary enforced in both directions. Proven
      by a two-browser e2e whose canary crosses encrypted while a `pg_dump` from inside the
      db container carries a plaintext control and no canary. History across reloads is the
      next item's local store; the first implementation's seven contract findings were
      adjudicated back into the contract in the same slice
- [x] **Own-message history after a reload** — *2026-08-30*, bounded at 500 messages and 30 days, in the keystore under the same wrapping key. (Found by the first client implementation): MLS
      gives a sender no way to decrypt its own application messages, so without a local
      plaintext store your own words render as undecryptable after a reload — honest, but
      visible. Its own slice, because the store needs the same at-rest design as the keystore:
      what you said is as sensitive as what you received
- [x] **Key-package pool replenishes on a low-water mark** — *2026-08-30*, at a quarter of a batch, with the local count treated as a lower bound on consumption. No contract read was needed after all: the PUT already returns `unclaimed_count`. (Was: a read for `unclaimed_count`
      (today only the PUT returns it) and a low-water replenishment policy — with the
      multi-device slice, which owns the device lifecycle this feeds
- [ ] **One MLS device per browser profile, shared across tabs.** Each tab builds its own
      device in wasm memory and they write to one stored slot, so the last tab to save wins and
      the other's ratchet advances are lost on the next reload. A Web Lock now stops the worse
      half — two tabs both minting a wrapping key and silently stranding each other's state —
      but a lock cannot make two devices into one. That needs a SharedWorker owning the device
      with the tabs as clients, which is a slice, not a patch
- [x] **Slice 3.2 — eviction by leaf signature key** ([ADR 007](adr/007-device-identity-and-verification.md)) — *2026-08-29*.
      A leaf's credential identity is a string the enrolling client chose, and removal filters on
      it, so a leaf credentialed under a *staying* member's id is never stale and never removed:
      it survives every eviction and reads every epoch after. The fix is an allow-list sweep, not
      a per-user lookup — evict every leaf whose signature key the directory does not map to a
      current member — because a planted leaf's key is exactly one the directory never listed for
      the removed user. Three touchpoints: a channel-scoped read of members' device signature
      keys (new endpoint → authz matrix + `/security-review`), a wasm export of leaf keys plus
      remove-by-key, and `reconcileMembers` switched to the key allow-list. Its regression test is
      the attack: a leaf planted under a staying member's id, evicted by the first reconcile.
      Adversarial review found the property holds and fails closed — a directory page that
      errors aborts the whole reconcile rather than sweeping against a short allow-list
- [x] **`failed` self-retries the way `waiting` and `incomplete` do** — *2026-08-30*, with the backoff bundle. From slice 3.2's
      adversarial review: exhausting the commit-retry budget leaves a channel `failed`, and only
      a commit nudge, a member event, a reconnect or a reopen re-drives it. Deliberately not
      fixed in 3.2: the fix is one more entry in the same five-second timer, and that timer is
      already carrying two unbounded polls the slice-3.1 review flagged for backoff. Doing both
      together is one coherent change; adding a third poll first is two
- [ ] Multi-device: per-device keys, device verification, key sync. Verification is where the
      residue of ADR 007 goes: the sweep trusts the directory's key↔person mapping, and only two
      humans comparing key material out of band can close that — no server signature can, because
      under PLAN §6.1's adversary 3 the signer is the adversary
- [x] **Slice 3.3 — key verification** ([ADR 008](adr/008-key-verification.md)) — *2026-08-30*: client-local
      records in the wrapped keystore (never server-stored — a server that can *set* a verified
      flag marks its own planted key safe), a safety number over a person's whole device-key set
      with **your own half computed locally and never from the directory** (both halves from the
      directory would show a planted key identically on both screens and the ceremony would bless
      it), refusal on the send path because ADR 007 established there is no veto at commit-apply,
      and TOFU pinning on by default. Client-only: no endpoint, no migration, no contract change.
      **Its gate is the roadmap's own key-swap test**, which this design exists to make
      implementable as written
- [x] **Encrypted backups + user-held recovery keys** — *2026-08-30*, [ADR 010](adr/010-encrypted-backups.md).
      Not org-level recovery, which the ADR refused and named for what it is: an organisation
      that can recover is an organisation that can read, so that policy *is* Compliance mode,
      chosen at setup. What a backup restores is the knowledge that cannot be re-derived — your
      verification decisions — never past message plaintext, which forward secrecy has already
      destroyed
- [ ] **The recovery UX drills**, which are the gate rather than the slice: back up, destroy the
      profile, recover with the key; then the same loss with no key, landing on the documented
      non-lying failure path. Two browser contexts, and phase gate item 2 is not met until they run
- [ ] **Slice 3.4 — media E2EE** ([ADR 009](adr/009-media-e2ee.md)) via LiveKit insertable
      streams. The key is MLS exporter output at the current epoch, so nothing distributes it and
      every member derives the same bytes independently; it rotates when the epoch does, into
      keyring slot `epoch % 16`, which needs no signalling at all. A fixed per-call key was
      refused because it would let a member removed mid-call keep listening — exactly the
      property ADR 007 spent a slice restoring for messages. `chan-` rooms only: a conference
      guest has no leaf to derive with, so conferences stay plainly labelled as not
      end-to-end encrypted rather than quietly weaker. LiveKit's stack is used whole except its
      key provider, which is wrapped because the stock one's `setKey` takes no key index and so
      cannot rotate (verified against the pinned 2.22.1 types); its own ratchet stays unused,
      since MLS is the only ratchet
- [x] Per-org mode choice — *2026-08-30*, [ADR 011](adr/011-org-encryption-mode.md), security-reviewed. **Strict E2EE** vs **Compliance mode**, clearly labeled,
      documented bluntly (search/export/retention impossible in Strict, by design)
- [ ] Compliance-mode server-side half actually built: encryption at rest, retention policy,
      compliance export (promised free in PLAN.md §7 — a mode toggle without them is dishonest)
- [x] Research spike: mobile push architecture with metadata minimisation
      ([`docs/spikes/mobile-push.md`](spikes/mobile-push.md)) — *2026-08-30*. It corrected this
      bullet's own framing: on the web path the push provider **cannot** read the payload (RFC
      8291 encrypts it end to end), so the adversary a push design must defend against here is
      our own untrusted server, not Apple or Google. Recommendation is standard Web Push sent by
      the instance itself under a self-generated VAPID key — no Apple Developer membership, no
      Firebase, no vendor relationship, which is the only shape that survives "installation is
      the product", and UnifiedPush comes free because UnifiedPush 3.x *is* Web Push. Two
      supposed blockers dissolved: the manifest already meets iOS's precondition, and §1.5's
      no-service-worker reasoning is about a cached shell, which a push-only worker is not
- [ ] **The one device test the push recommendation rests on**: can a service worker instantiate
      the ~489 KB MLS core, open the IndexedDB keystore through its non-extractable key, take the
      lock against a live tab, decrypt and display — inside a push handler's budget? Answerable
      only on real hardware. If it cannot, the recommendation collapses and the fallback is a
      generic "New message" with no content, which is honest but much less useful
- [ ] **Mentions cannot be computed server-side under Strict E2EE** (found by the same spike):
      `parseMentions` reads message content, which the server no longer has. Matrix hits the
      identical wall. It decides what a push can say and what the mention badge can count, so it
      belongs to whichever slice ships push
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
   (b) media: **a no-key subscriber at the server's own position cannot decode a `chan-` call**
   — mint a token server-side, join the live room as a bare subscriber holding no MLS state,
   and assert the tracks produce decrypt failures and no renderable frames, with the same probe
   against a `conf-` room decoding fine as the control.

   *This item used to say "packet capture at the SFU — RTP payloads fail to decode without the
   E2EE key", and that wording was vacuous* (found while designing
   [ADR 009](adr/009-media-e2ee.md)): SRTP already encrypts every hop, so a captured payload is
   unreadable whether or not media E2EE exists, and the drill would have passed identically
   against a build with no E2EE in it at all. The replacement puts the adversary where it
   actually sits — after SRTP terminates, holding the token-minting power the operator always
   has — and the `conf-` control is what keeps a clean result from being vacuous a second time,
   the same role the plaintext control plays in the message drill and the synthetic secret plays
   in the key-leak scan. The intent is unchanged and now measurable.
2. **Key-loss drill**: honest user loses device → recovers with recovery key; user without a
   recovery key hits the documented, non-lying failure path.
3. Mode choice is irreversible-safe: switching modes can't silently decrypt or expose history.

**Where the gate actually stands** — kept here rather than inferred from the checkboxes above,
because a phase with most of its slices done can still be nowhere near its gate, and that is the
distinction worth being able to see at a glance:

| Gate item | Status |
|---|---|
| 1(a) message canary → ciphertext only | **met**, automated rather than drilled — `webapp/e2e/specs/e2ee-messaging.e2e.ts` asserts the row shape and scans a real `pg_dump` from inside the database container, with a plaintext control so a clean scan cannot be vacuous |
| 1(b) a no-key subscriber cannot decode a `chan-` call | **buildable, not run.** Slice 3.4 landed, so the code the drill needs exists and `webapp/e2e/specs/e2ee-call-rotation.e2e.ts` covers the rotation. The drill in `docs/drills/e2ee-drill.md` has never been executed, and it is the item that proves the claim |
| 2 key-loss / recovery drill | **buildable, not run.** This row said "needs encrypted backups and a recovery key, neither built" — both were built ([ADR 010](adr/010-encrypted-backups.md)), so the row was stale in the direction of understating progress. What remains is running the drill: a real device loss, a real recovery, and the documented non-lying failure for a user who never kept a key |
| 3 mode choice irreversible-safe | **met by construction.** This row said "no Strict/Compliance mode exists yet"; [ADR 011](adr/011-org-encryption-mode.md) shipped it. Nothing converts in either direction — the per-conversation flag is fixed at creation — so the property holds because there is no conversion path, not because a check forbids one |

[`docs/drills/e2ee-drill.md`](drills/e2ee-drill.md) now exists and covers both halves: the
message half as the automated test that meets 1(a), the media half as the manual procedure that
will meet 1(b) once slice 3.4 lands. It also records why the packet-capture form was rejected, so
the shorter version is not reinvented by somebody who never saw this note.

---

## Phase 4 — Packaging polish (~1 month, overlaps 2–3)

Goal: median stranger, fresh VPS → working instance, **under 5 minutes, measured**.

- [x] **`install.sh` hardened** — *2026-08-30*. Ubuntu, Debian, Fedora, RHEL-clones, and
      AlmaLinux, which needed its own route because `get.docker.com` refuses it outright
      (verified: `ERROR: Unsupported distribution 'almalinux'`, and its docker-ce repo 404s).
      Idempotent: a second run leaves `deploy/.env` byte-identical. It also installs cosign,
      pinned to the version CI signs with, because nothing did — without it the update timer
      would have failed on every fire on every stock install. 118 checks.
      **Container-level only**: detection, idempotency and error paths are covered; actually
      installing Docker, starting systemd units and SELinux at runtime are not, which is why
      the VM matrix below is still open rather than satisfied by this
- [x] **A security finding closed in passing**: `.env.example` shipped `REPLACED_AT_INSTALL`
      for five secrets and the README told readers to copy it, while every `ensure_*` check
      tested for the *key* and never the *value* — so a placeholder LiveKit secret that mints
      admin tokens for every room could survive an install. Unexploitable only by luck (the
      32-byte key floor rejects a 19-byte placeholder, so the server refused to boot).
      Placeholders are now stripped and regenerated
- [ ] Installer served from `get.hamlaneh.com` (redirect/proxy to repo raw) — the documented
      one-liner uses this URL so tutorials never break
- [x] **The SQLite driver** — *2026-08-30*, `server/internal/sqlitestore`, 102 methods, the
      same set as the PostgreSQL store. The seam is the **consumer-side** interfaces that
      already existed, so nothing was invented for it and the compiler proves completeness:
      `var _ testdb.Store = (*sqlitestore.Store)(nil)`. Three representation choices carry
      what PostgreSQL got from its types — fixed-width UTC TEXT timestamps, canonical TEXT
      uuids whose hex order matches uuid bytes, citext as a registered collation. The whole
      module passes on both drivers, and `ci.yml` runs a full `-race` leg per driver because
      the SQLite concurrency *shape* (one connection, a retrying busy handler) is its own.
      Six PostgreSQL-mechanism tests are skipped under an allow-list whose gate was proved
      to fail in **both** directions — a stale entry fails it, a missing one fails it too
- [x] **Home mode's boot path** — *2026-08-30*. `hamlaneh-server home`: SQLite at the per-OS
      data directory, loopback bind, calls refused, compression on, and `--version`, which the
      updater needs to know what is installed. It mints its own signing and audit keys on first
      run, since home mode has no `install.sh` to generate them; that key path was reviewed
      adversarially before merge. The inverse is pinned too — a test proves server mode never
      creates a SQLite file, so an organisation's data cannot silently fork onto one
- [x] **Home mode's first run** — *2026-08-30*. The first admin is generated and printed once,
      chosen over a setup screen because a screen is a privileged unauthenticated path that must
      be proven shut afterwards and proven unreachable from a LAN bind, while a console line has
      no network surface to close. `localhost` and `127.0.0.1` are now both accepted, by an
      option that takes no argument and so cannot be handed a name: it derives siblings only
      when the configured origin is already loopback, same scheme and port. Proven in a real
      browser, and proven not to widen — a page on a *different* loopback port is still refused,
      and server mode's origin table is asserted byte-identical with the option forced on
- [x] **Gate clause 4's second half** — *2026-08-31*,
      `webapp/e2e/specs/home-mode-restart.e2e.ts`. It could never be a manual drill: a fresh
      instance is Strict, so a message exists only if the browser's MLS client put it there.
      Proven to fail as well as pass — pointed at a fresh data directory it goes red on the
      substantive assertion, not only on its console control.
      **What it proves is the server's half.** The database keeps the ciphertext, the session row
      and the channel, and never holds the words or any key that opens them; the MLS keystore and
      this device's own copy of what it sent live in the browser profile the restart does not
      touch. So a different device, or the same browser with its profile cleared, cannot read
      that history — MLS working, not persistence failing. Device-side durability is the backup
      path and a separate claim
- [ ] **Docker Desktop usually holds `:8080` on a household Windows machine**, so the very first
      run fails to bind for a reason that has nothing to do with Hamlaneh. The error now names
      `HAMLANEH_HOME_ADDR` and a free port. Whether the default should move off 8080 entirely is
      still open — it is the port most likely to be taken on exactly the machines home mode is for
- [x] **Tauri desktop app builds** — *2026-08-31*, three OSes in CI. The shell does not embed
      the web application and cannot: the client builds its base URL from
      `window.location.origin`, the session is an HttpOnly cookie and CSRF is a double-submit
      cookie, so a copy served from `tauri://localhost` would sit on an origin with no server
      and could hold none of them. It navigates to the instance instead. That makes its address
      field a trust boundary — the value reaches `location.assign` in the shell's own privileged
      origin — so it refuses `javascript:`, `data:`, `file:`, `tauri:`, `vbscript:` and `ws:`,
      with tests. Verified by driving the real Linux binary under Xvfb: the instance logged the
      request and the window displayed content that exists only on the remote origin.
      Two gaps written into the workflow rather than left implicit: the macOS leg is arm64 only,
      so an Intel Mac is uncovered, and **every bundle is unsigned**, so Gatekeeper and
      SmartScreen will both object
- [ ] **Clause 4's desktop smoke e2e**, which does not split the way I assumed when scoping it.
      Strict E2EE is *not* the blocker — the shell loads the instance's own webapp, MLS client
      included. The blockers are that **Tauri's WebDriver support is Linux and Windows only**, so
      a third of the matrix could never run this test whatever we write, and that the e2e stack's
      internal-CA certificate is refused by the system webview, with no in-app override by design
      (trusting a CA is the machine owner's decision). It needs the CA in each runner's trust
      store, or a home-mode HTTP instance for the desktop leg
- [ ] Bare-IP mode polished. *(The Caddy half is fixed: the files origin now has its own
      variable, because gluing `files.` onto a bare IP produced a name neither the internal CA
      nor any public CA could ever certify, and Caddy retried that impossible order for a month
      on an otherwise-working install.)*
- [x] **Response compression in the Go server, for home mode only** — *2026-08-30*,
      `server/internal/httpserver/compress.go`, off unless asked for and on by default in home
      mode, which is the mode with no proxy in front of it. Caddy's `encode zstd gzip`
      covers the compose path, but home mode has no proxy in front of it and would otherwise
      serve the ~560 KB web bundle uncompressed over whatever link the household has
- [x] **Signed releases (Sigstore/cosign) + SPDX SBOM + the anti-rollback refusal** —
      *2026-08-30*, `.github/workflows/release.yml`, `deploy/verify-release.sh` and its test
      suite, runbook in [`docs/releasing.md`](releasing.md). One signature over `SHA256SUMS`
      rather than per artifact, and the release is drafted so §6.6's simultaneous patch and
      advisory is a control rather than an intention. Every control in the verifier was
      mutation-tested. **Keyless signing is the untested half**: Fulcio and Rekor cannot be
      exercised offline, so the first real tag is where a wrong identity pattern would show
- [x] **The auto-updater** — *2026-08-30*, `deploy/hamlaneh-update.sh`. It contains no
      signature logic and no version ordering: `verify-release.sh`'s exit code is the authority,
      so there is only ever one copy of that check to rot. Both deployment shapes, atomic swap
      by `rename(2)` between siblings on one filesystem, rollback on a failed health check.
      43 checks, 14 controls mutated and all 14 went red.
      **On by default only where systemd is** — on a systemd-less host the timer cannot be armed
      and the installer says so rather than reporting success; backups already have a cron
      fallback and the updater does not
- [x] **Automated encrypted backups on by default; documented restore** — *2026-08-30*,
      `deploy/hamlaneh-backup.sh`, [`docs/backups.md`](backups.md). Verification happens before
      anything is stopped or written, so a wrong key or a tampered archive leaves the instance
      untouched rather than half-restored — asserted by comparing checksums before and after,
      not by inspection
- [x] **Publish [`docs/hardening.md`](hardening.md)** — *2026-08-30*. Defaults carry the load;
      the guide covers the optional extras (IP allow-lists, reverse-proxy variants, backup key
      custody) and now also home mode beyond the machine it runs on
- [ ] Start pre-selling Managed to interested orgs (validates pricing, near-zero build cost)
- Tests: install matrix in **real VMs, not containers** (installer touches Docker + systemd —
  containers test nothing; use nested-virt CI runners or scheduled cloud VMs): Ubuntu LTS,
  Debian, Fedora, RHEL-clone, each from a clean image, asserting healthz + login page +
  verify-defaults.sh; update→rollback drill; backup→restore drill

### Test gate ✅

1. ≥3 strangers, fresh VPS each, median time-to-working-instance **< 5 minutes** — timed, logged.
   **At risk, structurally, and not by a margin tuning will close:** `install.sh` runs
   `compose up -d --build`, which is a Go compile plus a Vite build on the stranger's VPS. On a
   modest machine that alone exceeds five minutes, and on a 1 GB one the web build is
   OOM-killed. The release pipeline already builds and signs images that nobody pulls. Making
   compose reference the published image, with `--build` kept for CI and development, is what
   this clause needs — and it also puts the signed-image supply chain on the path operators
   actually take, instead of leaving it theoretical.
2. Auto-update applies a signed release; a tampered release is rejected; an **older
   validly-signed release is rejected** unless explicitly forced (anti-rollback negative test).
3. Restore drill on a fresh machine: pre-backup canary message present, file checksums match,
   existing users log in. Negatives: backup archive unreadable without key; restore with wrong
   key fails with a clear error, not a corrupt instance.
   **The encryption check is not "a scan finds no plaintext canary"** — that wording was
   vacuous and is corrected here rather than quietly met. Gzip hides a literal string exactly
   as well as a cipher does, so absence alone certifies an unencrypted archive as clean; it was
   demonstrated by replacing encryption with `cat` and watching the naive check pass. The real
   check is four: the canary absent from the ciphertext, the archive not readable as gzip, not
   readable as tar, **and** the canary recovered from the archive once the key is supplied. The
   positive control is what makes the three absences mean anything.
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
- [x] `security.txt`, disclosure policy, security contact, stated patch targets — *2026-08-30*,
      drafted in `docs/security.txt` and `SECURITY.md`. **Not yet true in the world**: the
      contact address and canonical domain are placeholders, the file is served nowhere, and
      GitHub private vulnerability reporting is off by default — SECURITY.md links to that form
      today, so the link is a dead contact until it is switched on
- [ ] **Fronting the stack with a CDN or upstream proxy has no correct configuration** (found
      while writing the hardening guide). Caddy ships without `trusted_proxies` so it strips
      client-supplied `X-Forwarded-For`, and `clientIP` reads XFF only from a trusted peer — so
      behind a CDN every client collapses onto the CDN's address and per-IP limits on sign-in
      stop distinguishing anyone, while trusting the proxy naively would make the leftmost hop
      spoofable. The fix is code, not documentation: rightmost-untrusted parsing plus a
      trusted-proxy list behind one env var. Until then the guide tells operators not to try
- [ ] **IP allow-listing is promised in PLAN §6.5 and reachable only by editing a baked-in
      Caddyfile**, which `up -d --build` rebuilds and any upgrade overwrites. One env var
      (`HAMLANEH_ADMIN_ALLOW_CIDRS`) consumed by the Caddyfile turns a documented workaround
      into a supported default — and note the trap the guide had to correct: `/admin` is a
      client-side route served the same index.html as everything else, so blocking it protects
      nothing. The surfaces with power are `/api/v1/admin/*` and `/scim/v2/*`
- [ ] **No operator backup tooling exists at all** — the only backup code in the tree is the
      per-user MLS key backup (ADR 010), which is a different thing entirely. This is the Phase
      4 automated-backups box; the hardening guide deliberately did not paper over it with a
      cron recipe, because that is how a missing feature turns into folklore
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
