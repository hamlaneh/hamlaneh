# Hamlaneh — Master Plan

> **Hamlaneh** (هم + لانه — "shared nest"): self-hosted team communication. Chat, calls, meetings, and conferencing that a company installs on its own server in one line — and that stays theirs.

| | |
|---|---|
| **Status** | Pre-development — planning locked, foundation phase next |
| **License** | AGPL-3.0 (entire product, no proprietary features) |
| **Business model** | 100% open source; revenue from official hosting and managed services |
| **Document version** | 1.0 — August 2026 |

---

## 1. Vision

Companies and communities should be able to own their communication the way they own their laptops. Hamlaneh is a complete team communication platform — text channels, DMs, file sharing, 1:1 and group voice/video calls, screen sharing, and conferences — that anyone can run on their own infrastructure: a VPS with a domain, a bare IP address, or a computer at home.

The defining promise is the **install experience**. The self-hosted communication space (Mattermost, Rocket.Chat, Element/Matrix, Zulip) is mature but notoriously painful to deploy and operate. Hamlaneh's wedge is the 3x-ui standard: one command on a server, a short guided setup, and a working, TLS-secured instance five minutes later. Everything else in this plan serves that promise or the second one: **secure by default, honestly described**.

**Target users, in order:**
1. Homelab users and sysadmins who find us on GitHub and run us for fun — they become evangelists.
2. Small/medium businesses that want private team chat + meetings without per-seat SaaS pricing.
3. Privacy-sensitive organizations (legal, healthcare, agencies, NGOs) that *must* self-host.
4. Larger enterprises — reached bottom-up through employees who used Hamlaneh elsewhere.

---

## 2. Product principles

1. **Installation is the product.** If setup takes more than five minutes or requires editing YAML, we failed. Every feature must survive the question: "does this still work after `curl | bash`?"
2. **Secure by default.** Customers will not read the hardening guide. Defaults carry the entire security load.
3. **Everything open source, forever.** No feature gating, no "enterprise edition," no SSO tax. SSO, SCIM, audit logs, and compliance features ship free to everyone.
4. **Honesty over hype.** We never say "unhackable" — that product doesn't exist. We say: small attack surface, memory-safe language, E2EE, secure defaults, fast patching, independent audits (once true). Security is a process we commit to, not a property we ship.
5. **Assemble, don't reinvent.** Real-time video and cryptography are the two hardest problems in software. We build product and glue; we use proven, audited components (LiveKit, MLS libraries, Caddy) for the hard cores.

---

## 3. Market position

| Competitor | Strength | Weakness we exploit |
|---|---|---|
| Mattermost | Mature, enterprise traction | SSO/compliance paywalled ("SSO tax"); heavier deploy |
| Rocket.Chat | Feature-rich | Complex operation; licensing shifted over time |
| Element / Matrix | True federation, strong E2EE | Famously painful to deploy and administer |
| Zulip | Great threading model, fully OSS | Niche UX; video is an external bolt-on |
| Slack / Teams / Zoom | Polish, network effects | Cloud-only; no self-hosting; per-seat cost; no E2EE (Slack) |

**Differentiators:** (1) one-line install and one-file home mode, (2) chat *and* calls/conferencing unified out of the box, (3) every security feature free, (4) honest, audited security posture.

---

## 4. Architecture & technology stack

| Layer | Choice | Why |
|---|---|---|
| Backend | **Go** | Single static binary (enables the one-line/one-file install), strong concurrency, memory-safe. Same reason Mattermost chose it. |
| Database | **PostgreSQL** (server mode), **SQLite** (single-machine/home mode) | Postgres for real deployments; SQLite makes "one binary + one data file" possible. |
| Real-time messaging | **WebSockets** | Chat, presence, typing, notifications. |
| Calls / video / conferencing | **LiveKit** (open-source SFU, Go) + TURN (**coturn** or LiveKit's embedded TURN) | 1:1 → large conferences, screen share, E2EE-capable media. TURN makes calls survive corporate NAT/firewalls. |
| E2EE — messages | **MLS (RFC 9420)** via **OpenMLS** (the audited implementation — ADR 006; libsignal was never a candidate, it implements no MLS) | Group E2EE with forward secrecy and post-compromise security. **Never hand-rolled.** |
| E2EE — media | LiveKit insertable streams | SFU routes packets it cannot read. |
| Web frontend | **React + TypeScript** | Ecosystem, hiring, component reuse into desktop. |
| Desktop apps | **Tauri** wrapping the web UI | Small native .exe/.dmg/.AppImage; far lighter than Electron. |
| TLS / reverse proxy | **Caddy** | Automatic HTTPS the moment a domain points at the server; sane defaults. |
| Packaging | **Docker Compose** + `install.sh` (server); single binary (home) | The 3x-ui experience: detect OS, install Docker, ask for domain/IP, boot. |
| Updates | **Signed releases** (Sigstore/cosign) + auto-update channel | See §6.6 — unpatched instances are this category's biggest failure mode. |

**Deployment modes:**
- **Server mode:** `curl -fsSL get.hamlaneh.com | bash` → Docker Compose stack behind Caddy, on a domain or bare IP.
- **Home mode:** a single downloadable binary / desktop app that runs the whole stack on one machine with SQLite.
- **Cloud mode (ours):** the same server-mode automation, driven by our provisioning system (§8).

---

## 5. Organization & repositories

**GitHub org:** `github.com/hamlaneh` — an organization, not a personal account (ownership survives adding maintainers/cofounders; signals product, not side project).

**One monorepo for the product** — `hamlaneh/hamlaneh`:

```
hamlaneh/
├── server/      # Go backend
├── webapp/      # React + TypeScript
├── desktop/     # Tauri wrapper
├── deploy/      # docker-compose, Caddy config, install.sh
└── docs/        # including this plan
```

Rationale: nearly every feature touches API + client; a monorepo means one PR, one review, one CI run, no drift. (Mattermost started split and later paid the cost of merging into a monorepo; Zulip was a monorepo from day one.) Contributors clone one thing and `docker compose up`.

**Private repos (business machinery, not product):**
- `hamlaneh/cloud` — the hosting control plane: signup, billing, provisioning, internal admin. Keeping this private is standard (Ghost, Discourse) and does not dent the "100% open source product" claim.

**Satellite repos** (mobile apps, SDKs, Helm chart) are created only when they exist. An org full of empty placeholder repos reads as abandonware.

**Serving the installer:** the script lives in the monorepo but is served via `get.hamlaneh.com`, so tutorials never break if we reorganize.

**Go public at the right moment:** start the repo private; flip public when the one-line install genuinely works and the README has a screenshot. On GitHub, going public *is* the launch — one first impression.

---

## 6. Security plan

### 6.1 Threat model

Security is meaningless until we name the adversary. We defend against:

1. **External attackers** on the internet (the default case).
2. **Malicious or careless insiders** at a customer organization.
3. **A compromised or untrusted server** — including hosting providers. This is what E2EE is for: a server breach yields ciphertext.
4. **A compromised employee endpoint** — limited blast radius via session revocation, device lists, per-device keys.
5. **Supply-chain attacks on us** — our dependencies, CI, and release pipeline.

**Explicit non-goals (stated publicly):** a nation-state with malware on the user's device (nobody survives the endpoint); full metadata invisibility (E2EE protects *content*; we minimize metadata but cannot make traffic patterns disappear). Publishing this threat model, as Signal and Mattermost do, is itself a trust signal.

### 6.2 Application security — where breaches actually happen

Products like this are almost never broken through cryptography; they're broken through boring application bugs. Chat-specific hot spots:

- **Authorization (IDOR)** — the #1 real-world bug class. All permission checks live in centralized middleware (no handler can forget them), backed by automated tests of the full authz matrix: user A must never read channel B by guessing an ID.
- **Message rendering (XSS)** — markdown through a strict sanitizer + strict Content-Security-Policy with no inline scripts. One XSS in a chat app is a wormable account takeover.
- **Link previews (SSRF)** — URL fetching goes through an isolated egress proxy that blocks private IP ranges, with timeouts and size caps.
- **File uploads** — sandboxed processing workers, EXIF stripping, content-type enforcement, files served from a separate cookie-less domain.
- **Rate limiting everywhere**, especially login, signup, and password reset.

### 6.3 Identity & sessions

- Passwords: **argon2id**. 2FA: **TOTP** + **WebAuthn/passkeys** (phishing-resistant).
- **SSO (OIDC/SAML) + SCIM provisioning — free.** SCIM is quietly a security feature: when HR offboards someone, access dies instantly. Most corporate breaches involve forgotten accounts.
- Per-user device list with remote session revocation; new-device login notifications.
- Short-lived access tokens + rotating refresh tokens.
- Org-level policies: enforced 2FA, session lifetimes, password rules.

### 6.4 End-to-end encryption

- **Messages:** MLS groups (forward secrecy + post-compromise security) via an audited library.
- **Calls:** insertable-streams E2EE through LiveKit — the SFU routes what it cannot read.
- The parts teams get wrong are *around* the protocol, so they're first-class work items: **key verification** (safety numbers/QR now; a key-transparency log later so a malicious server can't silently swap keys), **multi-device key management**, **encrypted backups** with user-held recovery keys, and recovery UX that doesn't lock honest users out forever.
- **The E2EE ↔ compliance tension, resolved honestly:** strict E2EE makes server-side search, compliance export, and retention impossible *by design*. Each organization chooses a mode at setup — **Strict E2EE** or **Compliance mode** (TLS in transit + encryption at rest + server-side features) — clearly labeled. Pretending both can coexist simultaneously destroys credibility with anyone technical.

### 6.5 Secure by default

Customers won't harden anything, so defaults carry the load: public registration **off**; org-wide 2FA enforcement available; admin panel on a separate path with optional IP allow-listing; HSTS on; database never exposed to the network; containers non-root with read-only filesystems, minimal/distroless images, dropped capabilities; encrypted automated backups **on**. A hardening guide is published — and assumed unread.

### 6.6 Supply chain & updates

- **Unpatched self-hosted instances are this category's biggest failure mode** (attackers still exploit years-old GitLab/Exchange CVEs). Therefore: **signed releases (Sigstore/cosign) and one-click or automatic security updates from day one.**
- Open source cuts both ways on patching: attackers diff public commits and weaponize fixes within days. Therefore: **coordinated disclosure workflow** — GitHub security advisories, embargoed patches, simultaneous release + advisory, fast auto-update rollout.
- On our side: lockfiles, minimal dependencies, Dependabot + `govulncheck` in CI, an **SBOM shipped with every release** (enterprises ask), protected branches, no secrets in CI logs.

### 6.7 Detection & response

- Tamper-evident **audit logs** (logins, admin actions, exports) with SIEM export — security teams ask on the first sales call.
- `security.txt`, a public vulnerability disclosure policy, a real security contact, and a stated patch SLA.

### 6.8 Validation

- In CI: gosec/semgrep, Go native fuzzing on every parser.
- Before the word "secure" appears in marketing: an **external penetration test** and a **specialized cryptography audit** of the E2EE implementation (Trail of Bits, Cure53, NCC class). Published reports become sales assets.

### 6.9 Rules we never break

- Security patches ship to **everyone, simultaneously, free**. Never paywalled, never delayed for non-payers.
- No claim of "unhackable," ever.
- No hand-rolled cryptography, ever.

---

## 7. Open source model

**License: AGPL-3.0** for the entire product.
- What it does: anyone can self-host free; anyone offering Hamlaneh *as a service* must publish their modifications — so no one can build a better proprietary Hamlaneh on our backs, and every fork's improvements can flow home.
- What it honestly does **not** do: it can't stop someone from hosting *unmodified* Hamlaneh cheaply. Plausible and Discourse live with this. Our real moats are the **trademark** (clones can sell hosting, but they can't *be* Hamlaneh), upstream advantage (patches land with us first), and running the best instance ourselves.

**Contributions: DCO, not CLA.** A CLA's main purpose is preserving relicensing/dual-licensing options — which we've chosen against. The Developer Certificate of Origin (Linux-kernel style signed-off-by) is near-zero friction and signals there's no escape hatch. Trade-off accepted: contributed code can't easily be relicensed later. *Final confirmation before the first external PR* (see §12).

**What's open vs. private:**
- Open: the entire product — including SSO, SCIM, audit logs, compliance export. "Every security feature, free, forever" is both ethics and marketing (see the "SSO tax" criticism aimed at competitors).
- Private: only the cloud control plane (billing/provisioning) — business machinery, not product.

**Trademark:** register "Hamlaneh" once revenue exists. Code is forkable by design; the brand is not.

**Transparency:** a permanent "How Hamlaneh makes money" section in the README from day one — open product, paid hosting, no license changes coming, security patches free forever. Community backlash hits projects that change the deal midstream, not ones that state it upfront.

---

## 8. Business model

**Everything is open source; revenue comes from operating it.** Proven by Discourse (the closest comparable: communication software, fully OSS, hosting-funded), Ghost, Zulip, Plausible, Element.

| Stream | What it is | Notes |
|---|---|---|
| **Hamlaneh Cloud** | We host: sign up → automatic instance at `acme.hamlaneh.app` → per-user/month | Primary revenue. Free trial; small free tier TBD. |
| **Managed** | We operate Hamlaneh **on the customer's server/VPS/on-prem** | Premium-priced, high-touch; loved by exactly our privacy-conscious target orgs. |
| **Support / SLA** | Contracts for self-hosters: response times, upgrade help | Real revenue but scales with headcount; keep secondary. |
| *(Later)* Marketplace / sponsorships | Integrations, GitHub Sponsors | Supplementary only. |

**Cloud architecture: one isolated instance per customer** (Discourse-style), not a shared multi-tenant app.
- Reuses the self-hosting architecture — **our one-line installer literally is the provisioning engine**; every hour invested in the install experience improves cloud margins.
- Per-customer isolation is a genuine security upgrade (tenant-separation bugs can't exist without shared tenancy) and fits the whole story.
- Revisit multi-tenancy only if unit economics ever demand it.

**Payments:** a **merchant of record** (Paddle / Lemon Squeezy) at the start — they're the legal seller and handle global GST/VAT/sales tax, which is a miserable problem for a tiny company selling worldwide. Graduate to Stripe when revenue justifies the accounting overhead.

**Hard rules:** security patches never paywalled; no bait-and-switch license changes; the free self-hosted product is complete, not crippled.

---

## 9. Roadmap

Realism first: assembling proven components, a solid v1 (chat + calls + great install) is roughly **6–12 months for a small experienced team — longer solo**. The sequence below is ordered so there is always something shippable and self-validating. Before Phase 1, spend a weekend actually deploying Mattermost, Rocket.Chat, and Element+Jitsi — to study the competition and to catalogue exactly what makes them annoying.

### Phase 0 — Foundation (~2–4 weeks)
Namespace grabs, org + private monorepo, repo hygiene (§14), CI pipeline, and a **walking skeleton**: `docker compose up` → login page served by Caddy over TLS. Milestone: a stranger can boot the skeleton from the README alone.

### Phase 1 — Chat core (~2–3 months)
Accounts (argon2id, TOTP, WebAuthn), orgs/teams/channels/DMs, file sharing, search, admin panel, and the §6.5 secure defaults **built in from the start** (retrofit is how security fails). App-sec hot spots (§6.2) addressed as features are built, not after. Milestone: **you use Hamlaneh daily for something real.**

### Phase 2 — Calls & meetings (~1–2 months)
LiveKit integration: 1:1 and group video, screen share, conference rooms; TURN for corporate NATs. Milestone: a full meeting between two networks that would break naive WebRTC.

### Phase 3 — E2EE (~2–3 months)
MLS for messages, key verification UX, multi-device, encrypted backups + recovery keys, insertable-streams media E2EE, per-org Strict/Compliance mode choice. Milestone: a compromised-server drill yields only ciphertext.

### Phase 4 — Packaging polish (~1 month, overlaps 2–3)
`install.sh` hardened across distros, bare-IP mode, single-binary home mode, Tauri desktop builds, **signed auto-updates**, automated encrypted backups. Milestone: median stranger, VPS to working instance, **under 5 minutes**.

### Phase 5 — Hardening & audit (ongoing; gate before "secure" marketing)
Continuous: rate limiting review, CSP tightening, fuzzing, dependency scanning. Gate: external pentest + cryptography audit, findings fixed, reports published. **Public launch** (repo flips public, Show HN, etc.) happens whenever Phase 4's milestone is met — audits gate the *security marketing*, not the launch.

### Phase 6 — Hamlaneh Cloud
Provisioning automation on top of the installer, `*.hamlaneh.app`, merchant-of-record billing, status page, then the Managed tier. Start pre-selling Managed to interested orgs as early as Phase 4 — it validates pricing with near-zero build cost.

---

## 10. Risks & concerns

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | **Scope underestimation** — video + E2EE are the two hardest problems in software | Project stalls at 70% | Assemble (LiveKit, MLS libs), never build cores; strict phase gates; §11 non-goals enforced |
| 2 | **Solo capacity / burnout / bus factor** | Slow decay | Ship small and often; recruit maintainers early; docs good enough that others can run with it; consider a cofounder |
| 3 | **A security incident in a security-branded product** | Existential reputation damage | SDL from day one; audits *before* claims; disclosure playbook ready; honest comms if it happens |
| 4 | **Unpatched customer instances get owned** (the category's classic failure) | "Hamlaneh hacked" headlines for 2-year-old bugs | Signed auto-updates on by default; advisories; opt-in version telemetry to see the update curve |
| 5 | **Public patch diffing** — attackers weaponize fixes against laggards | Fast exploitation window | Embargoed coordinated releases; fast auto-update channel (see §6.6) |
| 6 | **E2EE vs. compliance expectations** — enterprises want both | Lost deals or dishonest promises | Per-org mode choice, documented bluntly (§6.4) |
| 7 | **Cheap unofficial hosts undercut Cloud** (AGPL can't stop vanilla rehosting) | Margin pressure | Trademark; official status; upstream speed; best-run instance. Discourse proves this suffices |
| 8 | **Incumbent competition** (Mattermost et al. copy the wedge) | Differentiation erodes | Install experience is culture, not a feature — hard to bolt on; keep "no SSO tax" loud |
| 9 | **Revenue arrives slower than motivation lasts** | Abandonment | Near-zero fixed costs; pre-sell Managed in Phase 4; MoR kills admin overhead; set a personal runway checkpoint |
| 10 | **Supply-chain attack on us** | Poisoned releases | §6.6 controls; signed releases; minimal deps; protected CI |
| 11 | **Mobile expectation gap** — users expect native apps + push; push + E2EE leaks metadata and is genuinely hard | Adoption friction | PWA as interim; native apps as first post-v1 satellite repos; design push with metadata minimization from the start |
| 12 | **E2EE key loss locks out honest users** | Support nightmare, angry customers | Recovery keys, org-level recovery policy options, relentless UX testing on recovery flows |
| 13 | **Legal/tax complexity of selling globally** | Fines, wasted months | Merchant of record; form the company before the first paying customer; trademark registration; brief consult with an OSS-savvy lawyer |
| 14 | **Abusive self-hosted instances** (spam, illegal content) tarnish the name | Brand damage | AUP applies to *our* Cloud; public stance that self-hosted instances are their operators' responsibility (standard OSS position) |
| 15 | **"Unhackable" expectations we didn't set** | Trust collapse on first CVE | Honest marketing (§2.4); published threat model; visible fast-patch track record |

---

## 11. Non-goals for v1

Explicitly out of scope until after v1 ships — each is a scope trap:
- Federation between instances (Matrix's complexity tax; maybe never)
- Native mobile apps (first post-v1 priority; PWA in the meantime)
- Plugin/app marketplace
- AI features
- High-availability clustering
- Compliance certifications (SOC 2 pursued for Cloud when enterprise deals require it)

---

## 12. Decisions log

| Decision | Status | Notes |
|---|---|---|
| Name: **Hamlaneh** | ✅ Decided | Namespace verified clear (only collision: an unrelated music track). Grab all handles immediately |
| **Bilingual UI: English (default) + Persian, full RTL** | ✅ Decided | Aug 2026. i18n from the first screen; all repo docs stay English. See ROADMAP.md |
| Frontend tooling: **Vite + Tailwind** | ✅ Decided | Aug 2026. Build tool + styling for the React webapp |
| License: **AGPL-3.0** | ✅ Decided | Whole product |
| **100% open source + hosting revenue** (no open-core) | ✅ Decided | Discourse model |
| **Monorepo** | ✅ Decided | Private `cloud` repo separate |
| Stack: Go / Postgres+SQLite / LiveKit / MLS / React+TS / Tauri / Caddy | ✅ Decided | §4 |
| Cloud: instance-per-customer | ✅ Decided | Installer = provisioner |
| Payments: merchant of record first | ✅ Decided | Paddle / Lemon Squeezy |
| **MLS library: OpenMLS**, exact-pinned, server MLS-blind, guests outside E2EE | ✅ Decided | Aug 2026, ADR 006. "OpenMLS vs libsignal" was a phantom choice — libsignal implements no MLS |
| **Device identity: eviction by leaf signature key against the directory; out-of-band comparison is the only real binding** | ✅ Decided | Aug 2026, ADR 007. No server-signed credentials — under §6.1's adversary 3 the signer is the adversary |
| **Key verification: client-local records, set-based safety numbers, refusal on the send path, TOFU pinning on** | ✅ Decided | Aug 2026, ADR 008. Your own half of the number never comes from the directory, or the ceremony would bless the attack |
| **DCO** instead of CLA | 🟡 Leaning | Confirm before first external PR — this is the last easy moment to choose |
| Cloud jurisdiction / data residency options | ⬜ Open | Decide before Phase 6 |
| Pricing numbers | ⬜ Open | Validate via Managed pre-sales |
| Opt-in telemetry design (version ping) | ⬜ Open | Privacy-first or none |
| Company formation timing/structure | ⬜ Open | Before first paying customer |
| Mobile push architecture (metadata) | ⬜ Open | Research during Phase 3 |

---

## 13. What success looks like

- **Install:** median time from fresh VPS to working instance **< 5 minutes** — measured, not vibes.
- **Adoption:** you daily-drive it (Phase 1) → 10 outside orgs daily-driving it → 1,000 GitHub stars as a rough awareness proxy.
- **Security:** pentest + crypto audit published; zero paywalled patches ever; advisory-to-patch time we're proud to publish.
- **Business:** first Managed pre-sale; first self-serve Cloud customer; hosting revenue covers infrastructure, then covers a salary.

---

## 14. Day-one checklist

- [ ] Register GitHub org `hamlaneh`; domains `hamlaneh.com` + `hamlaneh.app`; Docker Hub namespace; social handles — all in one sitting, today
- [ ] Create private monorepo `hamlaneh/hamlaneh` with the §5 layout; commit this document as `docs/PLAN.md`
- [ ] Hygiene files: `LICENSE` (AGPL-3.0), `README.md` (pitch + screenshot placeholder + "How Hamlaneh makes money"), `SECURITY.md` (disclosure policy), `CONTRIBUTING.md`, `CODEOWNERS`, issue/PR templates
- [ ] Branch protection + required CI: tests, gosec/semgrep, `govulncheck`, Dependabot
- [ ] DCO check on PRs (pending final §12 confirmation)
- [ ] `security.txt` on the website when it exists
- [ ] Walking skeleton: `docker compose up` → Caddy TLS → login page
- [ ] Weekend recon: deploy Mattermost, Rocket.Chat, Element+Jitsi; write down every friction point — that list is the install-experience spec
- [ ] Set the Phase 1 milestone: "I use Hamlaneh every day"

---

*This is a living document. When reality disagrees with the plan, update the plan — but changes to §6.9 (rules we never break) and §7 (the open source deal) require extraordinary justification, because those are promises to other people.*
