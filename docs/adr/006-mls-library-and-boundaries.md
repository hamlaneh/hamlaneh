# ADR 006 — The MLS library, and where MLS runs

**Status:** accepted · 2026-08-29
**Evidence:** [`docs/spikes/mls-library.md`](../spikes/mls-library.md) — every version, audit
and advisory claim below is verified there, with sources and a checked-on date. This document
answers that spike's three questions and does not restate its research.

## Context

Phase 3 opens with the roadmap line "Library pick finalized (OpenMLS vs libsignal)". The spike's
first finding is that this comparison never existed: libsignal implements the Signal Protocol,
not MLS — there is no MLS crate in its workspace. The stack tables in CLAUDE.md and PLAN.md §4
repeated the same phantom choice. Since MLS-vs-Signal-Protocol was decided by PLAN.md §6.4 long
ago and nobody has asked to reopen it, the real decision is which RFC 9420 implementation, at
which version, running where. The field is OpenMLS, mls-rs and ts-mls.

One constraint decides more than any comparison row, and it appears in no planning document:
**the product's only client is a browser.** `desktop/` does not exist until Phase 4, and when it
does, Tauri wraps the same web UI and inherits its answer. Whatever library is picked must reach
a browser, or Phase 3 does not ship.

## Decision 1 — OpenMLS 0.9.0, exact-pinned, on the `openmls_rust_crypto` provider

**OpenMLS is the only candidate that clears the bar the project already set.** CLAUDE.md's stack
table says "via audited library"; mls-rs states in its own README that it has RFC-conformance
validation but no third-party security audit, and ts-mls states the same. OpenMLS has one —
SRLabs, eight findings, one High, remediated in 0.8.1/0.7.3. This is a disqualification on the
candidates' own words, not a ranking: if either acquires a real audit, the comparison can be
rerun, and until then there is nothing to weigh. Q1 confirmed — the choice is forced, and what
was actually open is the version and the crypto provider.

**Version: `openmls = "=0.9.0"`,** exact-pinned per the dependency rule, MSRV 1.91. The
alternative was 0.8.1, the release the audit remediation shipped in, and the deciding argument
is an asymmetry in when breakage is cheap. Today no MLS state exists anywhere — no group, no
key, no stored epoch on any device. 0.9.0's breaking changes therefore cost nothing now,
whereas adopting 0.8.1 and moving later means migrating persisted cryptographic state inside
users' browsers, the single worst kind of migration this project could sign up for. The
audited lineage carries forward into 0.9.0; it is four days old, which would matter for a
ship-now decision and does not for a phase that runs months before anything reaches users. The
fallback is stated now so exercising it is not a redesign: if the integration spike below
cannot make 0.9.0 work on `wasm32`, drop to 0.8.1 — a one-line change this week versus a
state migration next year.

**Provider: `openmls_rust_crypto`.** Both providers depend on `hpke-rs ^0.7`, which is above
the floor that patches the critical nonce-reuse advisory (RUSTSEC-2026-0071, fixed ≥ 0.6.0) —
the floor is already cleared by the current lineage, and the CI gate below exists to keep that
true rather than to establish it. What separates them: `openmls_rust_crypto` is the reference
provider on the RustCrypto stack, pure Rust and WASM-proven — Wire's browser deployment is
this lineage. `openmls_libcrux_crypto` carries Cryspen's formally verified primitives, which
is genuinely attractive, but its sub-crates sit at version 0.0.8–0.0.9, and a crypto stack
that has not yet committed to its own API is not where this project starts. Revisit when
libcrux stabilizes; the provider sits behind `openmls_traits`, so the swap is contained by
construction.

**Consequence accepted knowingly: the webapp build chain gains Rust.** A thin wrapper crate —
glue only, zero cryptographic logic, per "assemble, don't reinvent" — is compiled to WASM by
`wasm-bindgen`/`wasm-pack` inside `deploy/Dockerfile` and CI. The server stays a single Go
binary; Rust enters the client build only, and the install experience is untouched because the
WASM ships inside the same embedded bundle. Two costs are real and named: contributor toolchain
(Rust joins Go and Node) and bundle weight, which is unmeasured — the spike below measures it
before any contract freezes. CI gains `cargo audit` over the wrapper's lockfile; the `hpke-rs`
episode — a critical bug one layer below an audited library — is the entire argument for it.

## Decision 2 — the Go server never learns MLS

Q2 confirmed: the server is a delivery service and a key-package directory — it stores and
forwards opaque blobs, and no Go MLS implementation or binding exists in this design. The
Rust→WASM wrapper is the product's only MLS integration surface.

The one place this claim gets tested is commit sequencing. MLS requires that exactly one commit
win each epoch, and the obvious implementation has the server parse the message framing to read
the epoch — at which point the server has an MLS parser, and the claim above is dead. Instead,
the client states the group id and epoch **in the envelope, outside the ciphertext**, and the
server uses that claim for exactly one thing: first-wins conflict rejection per (group, epoch).
This is safe to leave unverified because the claim is not load-bearing for security — a client
that lies about its epoch merely gets its own commit rejected by its own group members, who
verify cryptographically; it can disrupt nothing beyond a group it is already inside, which a
member can do anyway by other means. The server sequences what it cannot read.

Authorization splits into two authorities on purpose. The existing channel-membership check
remains the **transport** authority — who may fetch and post ciphertext, same 404, same matrix
harness, every new endpoint registered. The MLS group is the **confidentiality** authority —
who can actually read. The server does not try to enforce their equality, because it cannot:
verifying that a group's leaves match a channel's members requires reading group state, which
is the capability this decision denies it. Clients reconcile the two — membership events drive
add/remove proposals — and the roadmap's key-swap and downgrade tests exist precisely because
the server is untrusted in that loop.

## Decision 3 — conference guests are outside E2EE, and the boundary is the room kind

Q3 confirmed: strict mode excludes guests rather than adopting a key-in-fragment scheme, and
the scheme is rejected for the reason the question predicted. The server mints the conference
link; whatever key travels in its fragment is a secret the server chose or could have chosen,
so "the server cannot read this call" would be a promise underwritten by the party it is made
about. A guest has no credential, so this cannot be repaired by binding — there is nothing to
bind to. And a fragment-key scheme is not MLS; it is a parallel hand-rolled protocol, which
§6.9 bars regardless of its merits.

So the encryption boundary is the **room kind, fixed at birth**: DM and channel calls are
between authenticated members with MLS identities and get media E2EE; conference rooms are
guest-capable and are not E2EE, in either mode, labeled plainly on the join surface —
"honesty over hype" applied to a screen. The boundary being the kind rather than runtime
state is what makes the roadmap's downgrade test passable by construction: a client knows from
its own join path whether it is entering a `conf-` room, so there is no signal a malicious
server could flip to talk a client out of encrypting a members-only call.

Deliberately not built until someone asks: an org policy disabling conference links entirely
under strict mode, for orgs that find a labeled plaintext room unacceptable. Named so the next
person designs a setting rather than a workaround. How the MLS exporter keys LiveKit insertable
streams is its own design item inside Phase 3 — this ADR fixes who is inside the boundary, not
the keying mechanics.

## What must happen next, in order

1. **Integration spike** (Opus, scratch work, no repo changes): build the wrapper skeleton
   against 0.9.0 on `wasm32`, run a two-client group round-trip, measure the gzipped bundle
   delta. Its output is numbers and a works/doesn't verdict — it feeds step 2 and can trigger
   the 0.8.1 fallback. This host has no Rust toolchain; the spike runs in a Rust container.
2. **Contract** for the first slice — key-package directory endpoints and the ciphertext
   envelope (including the epoch field of Decision 2) — designed against the spike's measured
   reality, then frozen before any implementation agent spawns.
3. Implementation slices per the roadmap, Opus throughout; Fable returns for the adversarial
   review, read-only.

## Same-PR updates this ADR requires

Per CLAUDE.md "Changing a decision": the stack tables in CLAUDE.md and PLAN.md §4 (both named
"OpenMLS or libsignal", which the spike refutes), PLAN.md §12 decisions log, the ROADMAP Phase 3
checkbox, and OVERVIEW's architecture table — all in this commit.
