# SCIM 2.0 provisioning

Hamlaneh's provisioning surface, mounted at `/scim/v2`. It is the door an identity provider's
sync engine uses to create, update and deactivate accounts, and it is the half of enterprise
identity that matters most for security: when an organisation offboards somebody, access has to
die, not merely be marked as ended.

**This file is machine-read.** §6 is a table with one row per SCIM operation, and it plays the
part `openapi.yaml` plays for the REST contract, exactly as `ws-protocol.md` does for the
WebSocket gateway. The OpenAPI parser cannot see this file, so §6 is the only place a
completeness gate can learn these routes exist.

## 1. Why this is not in `openapi.yaml`

SCIM has its own wire format, fixed by RFC 7643 and RFC 7644: its own media type
(`application/scim+json`), its own error envelope (`urn:ietf:params:scim:api:messages:2.0:Error`
with a `scimType`), and its own resource schemas. None of that fits the contract's `Error` shape
or its codegen, and bending either to accommodate the other would damage both.

Two surfaces already live outside the spec for their own reasons. The files origin
(`server/internal/httpserver/files_origin.go`, ADR 003) is cookie-less and authorises by signed
URL. The WebSocket gateway carries a principal and mutates state, so it gets a second
machine-checked contract rather than an exemption.

SCIM follows the **gateway** precedent, not the files one: it has a principal and it mutates
accounts. It gets a contract document, an authz matrix, and a completeness test that diffs §6
against the mux — a route that exists in code and not in the table fails CI, and so does the
reverse.

## 2. What is implemented, and what is refused

Implemented: **Users only.**

| Operation | Behaviour |
|---|---|
| `GET /ServiceProviderConfig` | Static. Declares `patch: true`, `bulk: false`, `filter: {supported: true, maxResults: 200}`, `changePassword: false`, `sort: false`, `etag: false`. |
| `GET /ResourceTypes`, `GET /Schemas` | Static. `User` only — the **absence** of a `Group` resource type is how Groups are refused. |
| `GET /Users` | `startIndex`/`count` pagination. Filter supports **only** `userName eq "…"` and `externalId eq "…"`; anything else is `400 invalidFilter`. |
| `POST /Users` | Creates. `409 uniqueness` on conflict. |
| `GET /Users/{id}` | `id` is the account's own UUID. |
| `PUT /Users/{id}` | Full replace of the mapped attributes (Okta's update style). |
| `PATCH /Users/{id}` | RFC 7644 PatchOp over the mapped attributes; unknown paths are `400 invalidPath` (Entra's update style). |
| `DELETE /Users/{id}` | **Deactivates.** See §5. |

Refused deliberately, and documented here so nobody looks for them:

- **Groups.** There is no group model in the product — channels are flat and membership is
  direct (ADR 001). Mapping groups onto nothing and answering 200 would be a lie a provider
  would then build on. The `ResourceTypes` document not listing `Group` is the honest refusal.
- **Bulk, sorting, ETag.** Not needed at instance scale; declared false rather than half-built.
- **Password synchronisation.** `changePassword: false`, and a `password` attribute in a request
  body is ignored. A directory pushing passwords into this system is a credential path nobody
  asked for.
- **`is_admin`.** Not writable from SCIM at all. Role changes stay a deliberate act in the
  dashboard, so a compromised sync token cannot mint itself an administrator.

## 3. Authentication

`Authorization: Bearer <token>` and nothing else.

The client is a sync engine: no browser, no cookie jar, no CSRF header, and a fifteen-minute
rotating access token would be unusable by it. A valid session cookie — including an
administrator's — is worthless here, and a provisioning token is worthless under `/api`. Two
doors, two credentials, no ambient authority crossing between them.

Tokens are minted from the admin dashboard (`POST /api/v1/admin/scim/tokens`), shown once, and
stored only as a digest, exactly as invite links and reset tokens are. Several may be live at
once on purpose: rotating a credential an external system holds needs an overlap.

A missing, unknown or revoked token is `401`. So is a session cookie presented without one.

## 4. Attribute mapping

Anything not in this table is ignored on write and never emitted.

| SCIM | Hamlaneh |
|---|---|
| `id` | `users.id` |
| `userName` | `users.scim_user_name`, verbatim; derives `users.username` at creation |
| `externalId` | `users.scim_external_id` |
| `active` | `users.is_active` |
| `displayName`, `name.formatted` | `users.display_name` |
| `emails[primary]` (else the first entry) | `users.email` |
| `meta.created`, `meta.lastModified` | `created_at`, `updated_at` |

**`userName` is usually an email address**, which cannot satisfy the local username rules
(lowercase, 3–32, `[a-z0-9][a-z0-9_.-]*`). Those rules stay; the provider's value is stored
verbatim for round-trip fidelity and filtering, and the local username is *derived* from it —
lowercased local part, invalid characters replaced, truncated, suffixed on collision. Relaxing
the account rules to accept directory names would push the change through every screen that
displays one.

**`PUT` clears an omitted attribute — except `active`.** Full-replace semantics say an attribute
absent from the body is cleared, and every mapped attribute here follows that. `active` does not,
deliberately: a provider that trims its payload, or a hand-written integration that omits the
field, would otherwise offboard an entire directory in one request. The asymmetry is a
deliberate refusal to make the destructive reading the default one. Deactivation has to be
asked for — `active: false`, or `DELETE`.

**Adopting an existing account.** A filter on `userName` matches `scim_user_name` **or**
`email`. That second matcher is what lets a provider take over an account somebody already made
locally: the create returns `409`, the provider's lookup finds it by email, and the following
`PUT`/`PATCH` sets `externalId`, marking it directory-managed. This is safe where email
auto-linking at *sign-in* would not be, because the administrator minting the token is the
authority granting it.

## 5. Deactivation, and what "instantly" means

`active: false` and `DELETE` both deactivate. They call the same path the admin dashboard calls
— advisory lock, last-administrator check, and revocation of every session family in one
transaction. The WebSocket gateway's existing sweep then closes every socket of those families,
because it keys on revoked families rather than on who revoked them.

No new kill path exists, and none is needed. The requirement is sixty seconds; the machinery is
already tested at ten.

**Hard deletion is impossible by schema and would be wrong anyway.** `messages.author_id` and
`attachments.uploader_id` are `ON DELETE RESTRICT`, and erasing somebody's history to satisfy a
directory would destroy other people's conversations. `DELETE` deactivating is documented here
rather than discovered.

Repeated `DELETE` answers `204`: the resource still exists, deactivated, so the operation is
idempotent.

**Deactivating the last active administrator fails with `409`.** An instance nobody can
administer is unrecoverable, so the refusal is correct — but a provider will retry it, and an
operator needs to know why.

**When SCIM and the dashboard disagree**, the last writer wins, and every write from either side
is audit-logged with its source, so the tug-of-war is visible rather than silent. An
administrator can deactivate a directory-managed account and the provider's next sync may
reactivate it. That is a deliberate v1 choice: the alternative — locking directory-managed
accounts against dashboard edits — is more machinery than the first version needs, and can be
added without a migration.

## 6. Machine-readable operation table

CI parses the table between the markers. Every row must have a matching entry in the SCIM authz
registry (`server/internal/authztest`); the completeness gate fails on a row without an entry
and on an entry without a row. Columns are fixed: `op`, `method`, `path`, `authz`.

The gate has **three** sides, not two. Document against registry catches an unregistered route;
registry against the mux catches the case the first check cannot see — a document and a registry
that agree with each other while the server actually serves something else. The mux and the
route list are built from the same table, and a test asserts they match.

- `authz` — the rule the registry must assert:
  - `bearer` — a live provisioning token, and nothing else. Anonymous, an ordinary session
    cookie, an administrator's session cookie, and a revoked token all get `401`.

Every operation is `bearer`. The column exists so that adding one that is not has to be a
visible decision rather than an omission.

<!-- scim-operations:begin -->
| op | method | path | authz |
|---|---|---|---|
| serviceProviderConfig | GET | /scim/v2/ServiceProviderConfig | bearer |
| resourceTypes | GET | /scim/v2/ResourceTypes | bearer |
| schemas | GET | /scim/v2/Schemas | bearer |
| listUsers | GET | /scim/v2/Users | bearer |
| createUser | POST | /scim/v2/Users | bearer |
| getUser | GET | /scim/v2/Users/{id} | bearer |
| replaceUser | PUT | /scim/v2/Users/{id} | bearer |
| patchUser | PATCH | /scim/v2/Users/{id} | bearer |
| deleteUser | DELETE | /scim/v2/Users/{id} | bearer |
<!-- scim-operations:end -->

## 7. Rate limiting

One limiter at the top of the mux, keyed on the token when the bearer resolves and on the client
address otherwise, so guessing at tokens spends the address budget. The tokens are 256-bit, so
this is hygiene rather than the defence.

It cannot live in `ratelimits.go`: that registry is keyed on contract route patterns, and these
are not contract routes. A single choke point at the top of the mux means there is no per-route
budget to forget — which is the property the fail-closed registry exists to protect, preserved
by construction instead of by a table.

The budget has to survive a provider's initial full sync, which arrives as a burst.

## 8. Audit

Every account mutation is recorded with the authority that made it: `scim.user.created`,
`scim.user.updated`, `scim.user.deactivated`, `scim.user.reactivated`. Single sign-on records its
own creations separately as `sso.user.created`, so a log reader can tell an account a directory
pushed from one a provider assertion produced on somebody's first sign-in. The actor is the system
rather than a person, with the token's id in the detail, so the log names *which* credential
acted. Token lifecycle is recorded from the dashboard side as `scim.token.created` and
`scim.token.revoked`.
