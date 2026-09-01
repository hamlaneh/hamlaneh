# ADR 012 — Home mode: a second driver behind one contract, no calls and the binary says so, one suite that names what it skips

**Status:** accepted · 2026-08-30 · **Scope:** Phase 4, home mode (single binary + SQLite)

> **Accepted with one correction, 2026-08-30.** The ADR's first implementation step says to
> extract a literal `storage.Store` interface. Checked against the code: `storage.Store` is
> indeed a concrete pgx struct (~102 exported methods), but the seam already exists on the
> **consumer** side — `httpserver.Store` is an 81-method interface whose own comment already
> says "(two drivers)", and `bootstrap.Store` is a second, smaller one. That is the idiomatic
> Go shape and the one a second driver should satisfy. Extracting a producer-side twin would
> add a 110-method interface with one caller and two implementations to keep in step, which is
> the speculative abstraction CLAUDE.md's principle 5 forbids. **The SQLite driver implements
> the existing consumer interfaces**; where they are missing a method the driver needs, the
> consumer interface grows, and the compiler says so.

## Context

PLAN §4 promises three deployment modes, and home mode is the second: "a single downloadable
binary / desktop app that runs the whole stack on one machine with SQLite." §2's first principle
is why it exists — installation is the product, and one file with no Docker, no domain and no
YAML is the floor of that promise. CLAUDE.md's stack table carries the same line ("one storage
interface, two drivers") and its SQLite-timing paragraph carries the commitment this ADR designs
to meet: the driver is Phase 4 work, and from Phase 4 on "the full storage-interface test suite
runs against both drivers in CI." ROADMAP Phase 4 holds the bullets (home mode with both drivers
in CI, home-mode-only response compression, the three-OS Tauri builds) and gate item 4 holds the
proof: the binary starts on Windows, macOS and Linux; first run creates the SQLite database; a
sent message survives a process restart.

One question was explicitly deferred here. ADR 005's open list says: "Home-mode calls: LiveKit is
a second process a single binary cannot trivially absorb. Phase 4 decides; nothing here worsens
either branch." This ADR closes it.

What the storage layer actually is, measured rather than assumed, because the whole shape of the
work turns on it. There is no `Store` interface as a Go type today: `storage.Store` is a concrete
struct over a pgx pool — 110 exported methods across 22 files — and the "interface" CLAUDE.md
speaks of is, in reality, that exported method set plus the narrow consumer-side interfaces a few
packages define for themselves (`scim.Store`, `passwordreset.Store`, `linkpreview.Store`,
`bootstrap.Store`). The SQL behind those methods is deliberately, documentedly PostgreSQL — a
census of the package finds ~185 dialect-specific uses across 36 files, and they are not
incidental syntax but load-bearing design:

- **Row-lock strengths.** `FOR NO KEY UPDATE` on the channels row is the entire last-member
  design (`channels.go`, and the "# Lock order" essay in `storage.go` on why `FOR UPDATE` is the
  strength that deadlocks against `KEY SHARE`). `FOR UPDATE SKIP LOCKED` is the key-package
  claim (`mls.go`): two claimers race and neither waits. `pg_advisory_xact_lock` serializes the
  audit chain (`audit.go`), the last-admin rule (`users.go`) and a session path (`sessions.go`).
- **Trigram search.** Migration 0006 (and 0007 for filenames) builds GIN `pg_trgm` indexes over
  a `translate()` fold chosen for a bilingual product PostgreSQL ships no Persian stemmer for;
  `search.go` carries the matching Go fold (`foldSearchText`), pinned to the index expression by
  a test.
- **Arrays and set-returning functions.** `ANY($n::uuid[])` in the mention and attachment paths
  (`messages.go`), `unnest` for welcome fan-out and key-package upload (`mls.go`) and recovery
  codes (`totp.go`).
- **Postgres-only types and machinery.** `citext` (with pgx type registration in `storage.go`),
  `timestamptz` with a `'-infinity'` coalesce, `bytea`, `GENERATED ALWAYS AS IDENTITY` (audit
  seq), `LEFT JOIN LATERAL` for the single-pass unread/mention counts (a measured ~40x buffer
  win), data-modifying CTEs so a send is one round trip, `ON CONFLICT` against a partial unique
  index (the DM pair), error mapping by `pgerrcode` + constraint name, `information_schema`
  assertions in tests.

SQLite has no equivalent for the first category and different equivalents for the rest. That
asymmetry — plus what it would cost server mode to give these up — is what Decision 1 turns on.

## Decision 1 — a second driver behind one contract, not a portable dialect

Confirmed: home mode is a **second implementation of the storage contract**, not a rewrite of the
existing SQL into a lowest common denominator both databases parse.

The refutation of the portable-dialect path is concrete. To run on both engines, one shared SQL
text would have to drop: the lock strengths (SQLite has no row locks at all — no `FOR` clause of
any kind, no `SKIP LOCKED`, no advisory locks), `LATERAL`, data-modifying CTEs, arrays and
`unnest`, `citext`, `pg_trgm`/GIN, `timestamptz`/`'-infinity'`, and constraint-name error
mapping. Every one of those was chosen *for server mode* — the deployment real organizations run
— and several are the recorded resolution of real races (the last-member write skew, the
remove-vs-add queueing hazard, the double-claimed key package). A portable rewrite degrades the
mode that carries actual load to subsidize the mode that carries a household. That trade is
refused.

The inverse is also true, and it is what makes the second driver cheap rather than heroic:
**SQLite does not need that machinery to keep the same promises.** SQLite has one writer at a
time — every write transaction serializes (the driver opens them `BEGIN IMMEDIATE`, WAL mode on,
busy timeout set). Under a single writer:

- The last-member race cannot happen: two removals cannot overlap, so the count-then-delete is
  correct with no lock clause. Cost in home mode: none. The property holds; only the
  Postgres-specific concurrency *shape* (a removal never queuing behind an adder) is
  meaningless, because on SQLite everything queues, for microseconds, at household scale.
- The key-package claim cannot double-spend: the delete-returning statement is atomic under the
  writer lock. `SKIP LOCKED`'s never-wait property is lost; the outcome (one claim, one honest
  empty answer, no deadlock) is kept. Cost: none observable.
- The advisory locks vanish: the audit chain and last-admin serializations are what a single
  writer does by existing. Cost: none.
- The epoch compare-and-swap (`advanceEpochQuery`) is a plain conditional UPDATE and ports
  as-is in meaning.

The rest translates rather than vanishes, each with its home-mode cost stated: `LATERAL` becomes
correlated subqueries (per-row cost, fine for a sidebar of dozens); data-modifying CTEs become
two statements in one transaction (atomicity is the writer lock); arrays/`unnest` become
`json_each` or per-item statements in the transaction; `citext` becomes a Go collation the driver
registers, so case-insensitivity is defined by our code identically on every OS, with a parity
test pinning both drivers to the contract's username alphabet; `timestamptz` becomes an ordinary
sortable timestamp encoding and the `'-infinity'` coalesce a sentinel; the audit `seq` becomes
`INTEGER PRIMARY KEY AUTOINCREMENT` (monotonic, never reused); error mapping reads SQLite's
extended result codes instead of `pgerrcode`. Search is Decision 2's third item.

Three sub-choices, fixed here so the work has one shape:

- **The contract becomes a literal Go interface.** Today it is the method set of a concrete
  struct; Phase 4 extracts `storage.Store` as an interface over the existing 110 methods, with
  the domain types and sentinel errors as the shared, dialect-neutral vocabulary — which they
  already are: no `pgconn.PgError` crosses the package boundary today, handlers match on
  `storage.Err*` sentinels. This is mechanical, behavior-preserving, and lands **before** any
  SQLite code, with the suite green on Postgres, so the refactor and the new driver are never
  debugged together.
- **The driver is `modernc.org/sqlite`** (the established CGO-free translation), exact-pinned and
  vetted per the dependency rule. The deciding property is `CGO_ENABLED=0`: gate item 4 is a
  Windows/macOS/Linux matrix of static binaries, and a cgo SQLite (mattn) turns "cross-compile
  three targets" into "maintain three C toolchains," forfeiting the single-static-binary property
  PLAN §4 bought Go for.
- **A parallel migration tree, and no silent fallback.** The Postgres migrations are untouched —
  never edit an applied migration. The SQLite schema starts as its own baseline migration
  equivalent to the current schema (there are no existing SQLite databases to replay history
  for), in a dialect-named directory beside the existing one, and future slices add siblings in
  lockstep. Mode selection is explicit — a home-mode flag/data-directory setting versus the
  libpq environment — and **misconfiguration fails, never falls back**: a server-mode instance
  whose Postgres is unreachable must stop, not quietly open an empty SQLite file and fork the
  organization's data.

## Decision 2 — what home mode does not have, said by the instance document, not by broken buttons

**Calls: home mode ships without them, and this closes ADR 005's deferred item.** LiveKit is a
second process; the honest options were to absorb it, supervise it, or not ship it, and the first
two are refused on evidence already in the record. Absorbing it as a library: ADR 005 measured
that importing just LiveKit's `protocol` module grew the server's build closure from ~19 to 73
modules; the whole SFU — which is not built to be embedded — plus its media stack inside our
binary is that cost multiplied, permanently, against "assemble, don't reinvent" (we would be
assembling *inside* our process what upstream ships as a service). Supervising a bundled second
binary breaks the one-file promise outright and buys a process-management story (crash loops,
port collisions, upgrade lockstep) that a household installer cannot be asked to debug. And the
feature itself is at its least valuable exactly here: the SFU/TURN design exists to traverse
hostile NATs between strangers' networks, which is the opposite of one machine's household.

The presentation costs nothing new, because Phase 2 already built it: `GET /api/v1/instance`
carries a `calls` flag whose contract text already anticipates "an install that has not enabled
calls," the call and conference endpoints already answer `calls_unavailable`, and the UI already
**omits** call controls when the flag is false — no call button exists to fail, no greyed-out
door, no conference-creation screen. Home mode sets the flag false. One adjustment falls out:
ADR 005 made a *half*-configured LiveKit environment stop startup; home mode makes
*absent-entirely* a valid configuration meaning "calls off." Half-set still stops startup, in
both modes.

**Search: kept, same semantics, implemented as a scan.** The 0006 decision — substring matching,
no stemming, the Persian fold — is a product decision, not a Postgres one, and home mode keeps
it: the SQLite driver reuses the Go fold that already exists and is already pinned
(`foldSearchText`), matching with `LIKE`/`instr` over folded content. No `pg_trgm` equivalent is
built. The ceiling is named: this is a linear scan per search, which is fine for a household's
history and would not be fine for an organization's — if a home instance ever measures it slow,
FTS5's trigram tokenizer is the recorded upgrade path, behind the same interface, changing no
semantics. Snippets work identically: substring matching is what makes the contract's
`{text, match}` parts computable at all, and the run is found with the same fold on the Go side.

**Files: unchanged.** ADR 003 put blobs on the filesystem keyed by server ids, and 1.3 already
built the same-origin serving path with the attachment+nosniff+sandbox CSP "for the whole
bare-IP/home mode." Home mode is one data directory — the SQLite file, the blob store, the
generated keys — which is also the honest backup unit. Nothing moves into the database; a chat
instance's bytes do not belong inside its own row store.

**TLS: home mode is HTTP on loopback, and loopback is the default bind.** Caddy is not in this
mode and nothing replaces it. That is sound, not a shrug: `localhost` is a secure context, so the
whole cookie stack (`Secure`, `HttpOnly`, `SameSite=Strict`) and the app-owned security headers —
which 1.2a deliberately moved into the binary precisely so home mode would have them — hold
unchanged. HSTS is correctly absent: it is the one header only a TLS terminator can set
meaningfully. Binding beyond loopback is an explicit flag, and it fails closed by construction:
`Secure` cookies do not travel over plain HTTP to a non-localhost origin, so a LAN-exposed
home instance does not silently degrade — it visibly does not log in until the operator puts TLS
in front of it, and the flag's documentation and `docs/hardening.md` say exactly that (a
reverse-proxy in front is the supported shape; a self-signed-certificate ceremony that trains
people to click through browser warnings is refused as worse than honest HTTP). The roadmap's
home-mode-only response compression bullet exists for the same no-proxy reason and stands.

## Decision 3 — one behavioral suite on both drivers; mechanism tests are driver tests, and every skip is named

Confirmed, with a precise reading rather than a vague one. CLAUDE.md commits the "full
storage-interface test suite" to both drivers. The storage tests divide, in fact, into two kinds:

- **Behavioral tests of the contract** — what a method returns, which sentinel a conflict maps
  to, what an outcome must be under concurrency. This is nearly everything, *including* most of
  the concurrency tests, because they assert outcomes, not mechanisms:
  `TestOpenDirectMessageConcurrentIntegration` (one DM), `TestCreateMessageConcurrentIntegration`
  (one row per idempotency key), `TestConcurrentSendsClaimOneAttachmentOnceIntegration`,
  `TestClaimMlsKeyPackagesConcurrentIntegration` (one claim, one empty answer),
  `TestSubmitMlsCommitConcurrentIntegration` (one CAS winner),
  `TestRemoveChannelMemberConcurrentIntegration` (no emptied channel),
  `TestLastAdminRuleIsRaceSafe`, `TestRedeemInviteRaceProducesOneAccount`. These outcomes are
  exactly what SQLite's single writer guarantees, so they run — and must pass — on both drivers.
- **Mechanism tests of the Postgres driver** — assertions about *how* Postgres serializes, which
  are meaningless or false-by-design under a single writer:
  `TestRemoveChannelMemberDoesNotBlockOnAddIntegration` (asserts a removal does **not** queue
  behind an in-flight add; on SQLite everything queues, briefly, and that is correct there),
  `TestRegisterCitextIntegration` (pgx type plumbing), and the `information_schema` shape
  assertions (`integration_test.go`, `orgsettings_test.go`, `orgencryptionmode_test.go`), which
  are ported to `PRAGMA table_info` where the assertion is about the schema and stay
  driver-scoped where it is about Postgres itself.

The commitment is therefore achievable **as stated** for everything that is a test of the
interface; the mechanism tests were never tests of the interface, and pretending they were —
either by skipping them silently or by contorting them until they pass vacuously — is the
dishonest shape this decision exists to prevent. The CI form:

- The suite runs as a **driver matrix** (the storage and authz-matrix integration suites with the
  driver selected by the harness; `-race` on both legs; the SQLite leg needs no container at
  all, which also finally lets the storage suite run natively on a Windows dev machine).
- Driver-scoping is expressed through **one helper** (`requiresPostgres(t, reason)`), and the
  helper is **counted**: a checked-in allow-list names every test permitted to skip on SQLite
  and CI fails when the actual skip set differs from the list in either direction. A new
  Postgres-only test is thereby a reviewed, named decision, never a silent erosion of the
  matrix. A suite that silently skipped half its cases on one driver would be worse than one
  that names what it does not cover; this makes the naming machine-checked.
- The `pg_dump` canary (the e2ee drill's automated half in `webapp/e2e`) is a test of the
  compose stack, not of the storage interface; it stays on the Postgres stack, and gate item 4's
  home-mode assertions (three-OS start, first-run creates the database, a message survives
  restart) are its home-mode counterpart, not a port of it.
- Coverage floors and all other gates apply unchanged; the SQLite driver's packages sit under
  the same lint, vet, race and review rules as the rest of the server.

## Deliberately not decided

- **Home→server migration** (moving a household's SQLite data into a Postgres instance). Real
  someday; Phase 4 does not need it; designing an exporter now is speculation. Named so it is a
  decision when someone asks, not an accident.
- **Whether the Tauri desktop app bundles and spawns the home binary** or only connects to
  instances. The roadmap holds Tauri builds as their own bullet; this ADR only guarantees the
  binary the app would spawn.
- **Home-mode backup automation.** The data directory is the backup unit (stated above); whether
  the Phase 4 "automated encrypted backups on by default" slice covers home mode with the same
  machinery or a simpler file-level story belongs to that slice.
- **FTS5-trigram adoption** — the named upgrade path if the search scan ever measures slow.
- **Any LAN/multi-machine home topology beyond "put TLS in front."** Presets (Tailscale, a
  bundled proxy config) are hardening-guide material, not product surface, until demand says
  otherwise.

## What must happen next, in order

1. **Contract change — no.** The `calls` flag, `calls_unavailable`, `password_reset_available`
   and the search/file surfaces already say everything home mode needs said. No OpenAPI edit, no
   new endpoint, no authz-matrix delta from this ADR.
2. **Migration — none for Postgres; a new parallel SQLite tree.** No existing migration is
   touched. The SQLite baseline plus lockstep siblings land with the driver (Decision 1).
3. **Interface extraction (Opus, mechanical, first):** `storage.Store` becomes an interface;
   the pgx implementation and the test helpers re-point; suite green on Postgres with zero
   behavior change before any SQLite code exists.
4. **SQLite driver, TDD against the existing suite** (Opus): the suite is the spec and the red;
   driver-internal choices (timestamp encoding, collation registration, error-code mapping) stay
   inside the driver. NEW DEPENDENCY: `modernc.org/sqlite`, exact-pinned, `govulncheck`ed,
   justified in the implementing PR per CLAUDE.md.
5. **CI driver matrix + the named-skip gate** (Decision 3), in the same PR as the driver — the
   commitment is "in CI," not "on a laptop."
6. **Home-mode boot path** in `cmd/`: explicit mode selection with no silent fallback, loopback
   default bind, the LAN flag's fail-closed documentation, `calls` off, response compression
   (the existing roadmap bullet), first-run bootstrap reusing the existing empty-users-table
   admin path against SQLite.
7. **Gate item 4 verification**: three-OS start, first-run creates the database, message
   survives restart, Tauri smoke — plus the Fable adversarial review (read-only) on three
   falsifiable questions: confirm or refute — (i) some concurrent storage-suite outcome holds on
   Postgres and not on SQLite (a race the single-writer argument missed); (ii) some path lets a
   misconfigured server-mode instance open SQLite and fork data; (iii) some home-mode response
   is missing a security header or cookie property the compose stack has, beyond the two named
   and intended absences (HSTS, TLS itself).

Per the Fable protocol, this ADR is the design touchpoint; steps 3–6 are Opus; step 7's review
is the second and last Fable touchpoint unless an implementer reports being stuck.

## Same-PR updates this ADR requires (on acceptance)

Per CLAUDE.md "changing a decision": a PLAN §12 row (home mode: second SQLite driver behind an
extracted interface, no calls, loopback HTTP, named-skip driver matrix — ADR 012). CLAUDE.md: the
SQLite-timing paragraph flips from "do not write SQLite code before Phase 4" to naming the
active work and this ADR; the stack table's E2EE row precedent applies — the database row gains
the ADR pointer, and `modernc.org/sqlite` enters via the implementing PR's dependency
justification rather than this document. ROADMAP Phase 4: the home-mode bullet gains the pointer
here. ADR 005's open question gains the one-line "resolved by ADR 012 — home mode ships without
calls" annotation, the courtesy 006 and 011 established. OVERVIEW is untouched by the ADR
itself; the slice that makes home mode real adds it there in the same commit, as always.
