# ADR 004 — Enterprise identity: OIDC single sign-on and SCIM provisioning

**Status:** accepted, 2026-08-26 · **Owner:** orchestrator · **Scope:** Phase 1.6

## Context

Phase 1.1 and 1.4 already built most of what enterprise identity stands on: hashed session
tokens with families and rotation, a deactivation path that revokes every family inside one
transaction under a last-admin lock, a WS sweep that closes a revoked family's sockets within a
tested 10 seconds, fail-closed route-policy and rate-limit tables, the hash-chained audit log,
and the authz matrix with its completeness gate.

Reading that code to design against it turned up three gaps that this phase inherits rather than
creates:

1. **`org_settings.require_totp` is stored, editable, and enforced nowhere.** No code outside the
   admin handlers and the settings storage reads it. The generated API text says it is "enforced
   at the next sign-in"; that enforcement was never built. An admin who turns it on gets a
   screen that agrees with them and an instance that does not.
2. **`org_settings.session_lifetime_hours` is likewise unenforced** — sessions always use the
   compile-time `session.RefreshTTL`.
3. **`registration_mode: open` has no endpoint behind it.** There is no self-signup route in the
   contract, so the setting currently gates nothing.

The roadmap's own test — "SSO-created users still subject to org 2FA policy" — cannot pass while
(1) holds for anybody. Making the policies bind is therefore the first slice, before any SSO
code.

A fourth structural fact: `users.password_hash` is `NOT NULL`, and SSO- and SCIM-created accounts
have no password.

## Decisions

**OIDC through `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`.** The no-dependency path
founders on one part: ID-token signature verification is JOSE, and JOSE is where real
implementations die — algorithm confusion, `alg: none`, `kid` injection, RSA/HMAC key confusion.
Calling `rsa.Verify` is not hand-rolled crypto, but re-implementing JWS header parsing and
algorithm negotiation around it is exactly the "around the protocol" layer PLAN §6.4 warns about.
Both modules are pinned and vetted at implementation; `go-jose/v4` arrives transitively and is
covered by the existing `govulncheck` gate.

**One provider per instance, configured by environment, validated at startup, discovered lazily.**
One org is one instance is one IdP. A half-configured set stops the server the way a half-
configured mailer does, so misconfiguration surfaces at deploy. Discovery itself is lazy and
retried: an IdP that is down must not stop the chat server from booting. The identity table still
records `issuer`, so multi-provider later is additive rather than a migration.

**`(issuer, subject)` is the only login key, and email never auto-links a local password account.**
Looking users up by email on each login is the classic takeover bug — emails are mutable at the
IdP, `sub` is contractually stable. Auto-linking is permitted in exactly one case: the account is
already SCIM-managed (`scim_external_id` set) and the emails match, so both sides of the match
come from the same authority an admin already granted. A local password account with a colliding
email is refused (`sso_account_exists`) and links from Settings while signed in. Refusing costs
one password sign-in per legacy user; accepting `email_verified` from the IdP would fuse the
weakest email assertion on either side into a session.

**Just-in-time provisioning is its own org setting, default off.** It is not inferred from
`registration_mode` — that setting governs a self-serve password door that does not exist, and
"an admin configured an IdP" is not the same consent as "everyone in the directory may have an
account". With it off, an unknown identity creates nothing, which is how "SSO cannot bypass
registration-off" passes: the branch does not run.

**Org 2FA binds per session, at mint, for password and SSO alike.** A `sessions` column set when
the policy is on and the account has no activated TOTP; middleware then admits only the enrolment
endpoints. Per-session rather than computed live in middleware, because computing it live *is*
mid-session enforcement and would strand every open session including the admin who flipped it —
the opposite of the documented semantics. SSO sign-ins get the same TOTP challenge as password
sign-ins: a policy screen that silently exempts an entire sign-in method is a lie the admin
cannot see, and reading IdP `amr`/`acr` claims to detect IdP-side MFA is IdP-specific and
unverifiable. Orgs whose IdP already enforces MFA leave the setting off, which is their decision
and visible on their screen.

**`password_hash` becomes nullable**, read through `COALESCE(..., '')` so the Go type and every
existing reader stay as they are. Login gets one explicit guard: without it, every password
attempt against an SSO-only account logs a malformed-hash error.

**SCIM is hand-implemented, Users only, and documents what it refuses.** It is JSON over HTTP with
no cryptography; the Go SCIM libraries bring their own routing and schema machinery and we would
still write all the storage glue. Groups are refused by the `ResourceTypes` document rather than
by a fake 200 — there is no group model in the product (ADR 001). `is_admin` is not writable from
SCIM at all, so a compromised sync token cannot mint an admin. `DELETE` deactivates: hard delete
is impossible by schema (`messages.author_id` is `ON DELETE RESTRICT`) and erasing history would
be worse than the surprise.

**SCIM authenticates by bearer token, not the session cookie.** The client is a sync engine: no
browser, no cookie jar, no CSRF header, and a 15-minute rotating access token is unusable by it.
Tokens are minted from the dashboard and stored hashed, following the invite pattern. The SCIM
mux accepts only `Authorization: Bearer`; a valid admin cookie is worthless there and the token
is worthless on `/api/`. Two doors, two credentials, no ambient authority crossing between them.

**Instant deprovision reuses the existing path.** `active: false` calls the same
`UpdateUserAdmin` the dashboard calls — advisory lock, last-admin check, family revocation in one
transaction — and the WS sweep then closes the sockets because it keys on revoked families, not
on who revoked them. No new kill path exists or is needed; the roadmap's 60-second requirement is
met by machinery already tested at 10.

**SCIM lives outside `openapi.yaml`, inside the completeness discipline.** Its wire format and
error envelope do not fit the contract's, so it follows the `ws-protocol.md` precedent: a second
document with a machine-readable operation table, a matrix, and a test that diffs the table
against the mux.

## Consequences

- Four migrations, one per slice, in landing order: the session enrolment flag; `oidc_identities`;
  nullable `password_hash` plus the SCIM columns and token table; the JIT setting.
- Two new direct dependencies, both vetted at implementation per the CLAUDE.md rule.
- `session.RefreshTTL` becomes a ceiling rather than the value once org lifetime binds.
- Sign-ins become audited (`user.signed_in`, with the method) — they are not today, which would
  otherwise ship SSO logins audited while password logins are not.
- The webapp needs the two-step challenge reachable by URL; today it is an in-flow state after a
  202, and an SSO callback has to be able to land on it.
- The SSO button and the SCIM token screen have no delivered mockups, so they are built as
  unstyled plumbing marked `awaiting-design`, per the UI pipeline rule.

## Not in this phase

SAML (post-v1 per the roadmap; nothing here blocks it). OIDC front- or back-channel logout — the
security need is "when the org offboards you, access dies", which SCIM meets; local logout stays
local, and that limitation is documented rather than hidden. IdP refresh tokens. SCIM Groups.
Multi-provider, a provider picker, IdP secrets in the database, and forced-SSO mode — the last is
a real ask someday and needs its break-glass story designed first.

## Slices

1. **Org policies actually bind** — no SSO code. The enrolment gate, session lifetime, and
   sign-in auditing. Closes the three inherited gaps above.
2. **OIDC for linked accounts, plus settings linking.** JIT does not exist yet, so unknown
   identities are refused and the registration-off property is true and tested from the start.
3. **SCIM**, including the deprovision-kills-sockets test and fuzz targets for the filter and
   PatchOp parsers.
4. **JIT provisioning** — the smallest slice, deliberately last: it is the widest door, and it
   lands after every gate it must respect exists and is tested.

OIDC and SCIM do not land together. Each is useful without the other, and each carries its own
security review.

## Open questions

- Uniform 2FA means double-MFA for orgs whose IdP already enforces it. Holding the line until a
  real customer asks; the exemption toggle is additive.
- SCIM reactivating an admin-deactivated user is last-writer-wins with audit visibility. If
  operators trip on it, an IdP-managed edit lock is the known next step.
- Clock-skew leeway for ID-token expiry: shipping strict, with the one-line offset in reserve.
- Whether `user.signed_in` should cover failed attempts. Success-only for now; failed-attempt
  logging is a volume and enumeration trade not yet thought through.
