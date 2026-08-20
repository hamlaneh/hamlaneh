# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security problems.**

Report vulnerabilities privately via
[GitHub private vulnerability reporting](https://github.com/hamlaneh/hamlaneh/security/advisories/new)
(Security → Advisories → "Report a vulnerability" on this repository).

What to expect:

- **Acknowledgement within 3 business days.**
- An assessment and a remediation plan (or a reasoned dismissal) within **14 days**.
- Coordinated disclosure: we prepare fixes privately and release them together with an
  advisory. We will credit you unless you prefer otherwise.
- No bug bounty exists at this stage — this is a pre-revenue open source project — but
  reports are genuinely appreciated and publicly credited.

Our standing commitments: security patches ship to everyone, simultaneously, free —
never paywalled, never delayed for non-payers. We never claim any software is immune
to attack, and we never hand-roll cryptography.

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
