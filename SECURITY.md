# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security problems.**

Report vulnerabilities privately via
[GitHub private vulnerability reporting](https://github.com/hamlaneh/hamlaneh/security/advisories/new)
(Security → Advisories → "Report a vulnerability" on this repository).

What to expect:

- Acknowledgement, triage and a fix on the timelines in the next section.
- Coordinated disclosure: we prepare fixes privately and release them together with an
  advisory. We will credit you unless you prefer otherwise.
- No bug bounty exists at this stage — this is a pre-revenue open source project — but
  reports are genuinely appreciated and publicly credited.

Our standing commitments: security patches ship to everyone, simultaneously, free —
never paywalled, never delayed for non-payers. We never claim any software is immune
to attack, and we never hand-roll cryptography.

## Response and patch targets

These are the numbers we hold ourselves to. They are deliberately modest, because
Hamlaneh is maintained by one person and a target that is quietly missed is worse than
a slower one that is met.

| Stage | Target | Clock starts |
|---|---|---|
| **Acknowledgement** — a human has read your report | **3 business days** | when you send it |
| **Triage** — severity assigned, a remediation plan or a reasoned dismissal, sent to you | **14 calendar days** | acknowledgement |
| **Fix released** — Critical | **7 calendar days** | triage |
| **Fix released** — High | **30 calendar days** | triage |
| **Fix released** — Medium | **90 calendar days** | triage |
| **Fix released** — Low | next regular release; no date committed | — |

Severity is our own assessment, using CVSS v3.1 bands (Critical 9.0–10.0, High 7.0–8.9,
Medium 4.0–6.9, Low 0.1–3.9) as shared vocabulary rather than a score we will argue
about. If you disagree with the rating, say so — we record the disagreement in the
advisory rather than settling it silently.

Four things this promise deliberately includes:

- **If we are going to miss a target, you hear it before it lapses**, with a new date and
  the reason. Silence is the failure mode we are guarding against, not slowness.
- **The fix clock starts at triage, not at your report.** We are not going to pretend a
  bug that takes ten days to understand had a seven-day fix window.
- **Public disclosure happens when the fix ships, or 90 days after triage, whichever comes
  first** — shorter or longer by agreement with you, and immediately if the issue is
  already being exploited in the wild.
- **Patches ship to every user at once, free.** Restated here because it is the commitment
  most easily eroded by a deadline.

Two things it does not include: a promise about issues in our dependencies, where we can
only ship an update once upstream does (we will say so and, where practical, mitigate in
the meantime); and any target at all for reports sent through a channel we do not
monitor, which is why the contacts above are the only ones that count.

While Hamlaneh is pre-release these targets bind us, not a support contract — there are
no released versions to patch yet, and no user relying on one. They become the published
commitment at the first release.

## Supported versions

**There are no released versions yet.** Hamlaneh is pre-release, under active
development, and has not been audited. Nothing here should be relied on to protect
anything valuable yet. Once releases exist, this section will list which versions
receive security patches.

## Threat model

Security is meaningless until the adversary is named. Hamlaneh is designed to defend
against:

1. **External attackers on the internet** — the default case for any
   internet-reachable service.
2. **Malicious or careless insiders** at a customer organization.
3. **A compromised or untrusted server** — including hosting providers. This is what
   end-to-end encryption (planned: MLS, RFC 9420, via an audited library) is for: a
   server breach should yield ciphertext, not conversations.
4. **A compromised employee endpoint** — blast radius limited via session revocation,
   per-user device lists, and per-device keys.
5. **Supply-chain attacks on us** — our dependencies, CI, and release pipeline
   (lockfiles, minimal dependencies, vulnerability scanning in CI, signed releases).

### Explicit non-goals

We state publicly what we do **not** defend against, because pretending otherwise
destroys trust:

- **A nation-state adversary with malware on the user's device.** Nobody survives a
  compromised endpoint; no communication tool can protect content from the device it is
  read on.
- **Full metadata invisibility.** E2EE protects message *content*. We minimize the
  metadata the server stores and sees, but we cannot make traffic patterns — who talks
  to whom, when, how much — disappear.

### Where breaches actually happen

Products like this are almost never broken through cryptography; they are broken
through application bugs. Our engineering priorities reflect that: centralized
authorization checks with automated matrix tests (IDOR), strict content sanitization
and CSP (XSS), egress-proxied link previews (SSRF), sandboxed file processing, and rate
limiting on authentication endpoints. See `docs/PLAN.md` §6 for the full security plan.

## Current status, honestly

- No external penetration test has happened yet.
- No cryptography audit has happened yet (E2EE is not yet implemented).
- Both are planned and gate any use of security language in marketing — audits come
  before claims, not after.
