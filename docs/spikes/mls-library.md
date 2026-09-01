# Spike — the MLS library pick

> **Status: evidence only. Nothing is decided here.** This file feeds ADR 006, which makes the
> call. Written by the orchestrator so the design pass reads a prepared record rather than
> repeating the search.
>
> **Checked on 2026-08-29.** Every version, date and audit claim below was verified that day
> against the source named in it. Crypto libraries move; re-check before relying on any line.

ROADMAP Phase 3 opens with: *"Library pick finalized (OpenMLS vs libsignal): short spike
comparing maturity, audit status, multi-device story → ADR."* This is that spike.

## 1. The roadmap's framing is wrong, and that is the first finding

**libsignal does not implement MLS.** Its Rust workspace contains `protocol` (the Signal
Protocol — X3DH plus the Double Ratchet), `zkgroup` (private group membership via
zero-knowledge credentials), `keytrans`, `message-backup`, `svrb` and a dozen others. There is
no MLS crate, and Signal's group messaging is sender keys over `zkgroup`, not RFC 9420.

So "OpenMLS vs libsignal" is not a choice between two MLS libraries. It is a choice between
**MLS and the Signal Protocol** — a protocol decision, not a dependency decision. PLAN.md §6.4
and the stack table in CLAUDE.md both already decided that question: MLS, RFC 9420. Reopening it
is an ADR of its own and nothing in Phase 3 asks for one.

Treat the roadmap line as naming one candidate and one non-candidate. The real field is below.

One thing libsignal has that is worth stealing later rather than now: `keytrans`, a key
transparency implementation. PLAN.md §6.4 wants a key-transparency log "later so a malicious
server can't silently swap keys" and files it as post-v1. That stays post-v1; noted so the next
person does not rediscover it as if it were new.

## 2. The constraint nobody has written down: where MLS actually runs

E2EE means the crypto runs on the client. Ours are:

| Client | Exists today | Language |
|---|---|---|
| Web app | **yes, and it is the only one** | React 19 + TypeScript, Vite, browser |
| Desktop (Tauri v2) | **no** — `desktop/` is not in the tree; Phase 4 | Rust shell around the same web UI |
| Native mobile | no — PLAN.md §11 non-goal for v1 | — |

**Today there is exactly one client and it is a browser.** That is the hardest constraint on
this decision, it appears in no plan, roadmap or ADR, and it eliminates candidates on its own. A
library that cannot reach a browser cannot ship Phase 3 at all — not "less conveniently", at all.

The Tauri desktop app, when it arrives, wraps the same web UI, so it inherits whatever the
browser gets. It does not create a second integration.

The server side is an open question rather than a constraint, and question **Q2** below asks it.

## 3. The field

| | **OpenMLS** | **mls-rs** (AWS Labs) | **ts-mls** | libsignal |
|---|---|---|---|---|
| Implements RFC 9420 | yes | yes | yes | **no** |
| Latest release | **0.9.0**, 2026-08-25 | 0.56.0, 2026-08-19 | — | — |
| License | MIT | Apache-2.0 OR MIT | — | — |
| crates.io downloads | ~565k | ~280k | — | — |
| Third-party security audit | **yes** — SRLabs | **no** — RFC conformance only | **no**, stated by its own README | n/a |
| Browser | Rust → WASM; `js` feature, `openmls-wasm`, `wasm-bindgen-test` in CI | WASM builds supported | native TS | n/a |
| Production browser precedent | **Wire `core-crypto`** | AWS Wickr (not browser-confirmed) | none found | n/a |
| RustSec advisories against the crate | none | none | n/a | n/a |
| Language | Rust | Rust | TypeScript | Rust |

### The audit row is the one that decides

CLAUDE.md's stack table says "MLS (RFC 9420) via **audited library**", and PLAN.md §6.9 makes
"no hand-rolled cryptography, ever" a rule needing extraordinary justification to bend. Read
literally, that eliminates two of the three MLS candidates:

- **OpenMLS** was audited by **SRLabs**, sponsored by the Sovereign Tech Agency. Eight findings,
  one rated High; seven fixed and shipped in **0.8.1** and **0.7.3**, the last (low) still in
  flight when the maintainers published in May 2026. Co-maintained by Phoenix R&D and Cryspen,
  and it uses Cryspen's formally verified ML-KEM and x25519 from libcrux.
- **mls-rs** states it has been *"validated for conformance to the RFC 9420 specification but
  has not yet received a full security audit by a 3rd party."* Conformance is not an audit.
- **ts-mls** says in its own README that it *"has not undergone a formal security audit."* Pure
  TypeScript with no WASM boundary makes it the most pleasant option to integrate, and it is the
  one that fails the project's own bar most clearly. Recorded so nobody proposes it twice.

### The dependency finding, which matters more than any row above

`hpke-rs` — Cryspen's RFC 9180 implementation — carries **three RustSec advisories, all
published 2026-03-24**:

| Advisory | Severity | What |
|---|---|---|
| RUSTSEC-2026-0071 | **Critical** | Nonce reuse: the HPKE context sequence number was a `u32` incremented with wrapping addition, silently wrapping to 0 after 2^32 operations in release mode. Patched in `>= 0.6.0` (`checked_add`, widened to `u64`). One-shot API users unaffected. |
| RUSTSEC-2026-0070 | High | Panic when opening or sealing on an export-only context |
| RUSTSEC-2026-0069 | — | Incorrect length encoding on KDF export |

That crate sat under **both** OpenMLS and Signal's `signal-crypto`. It is the concrete answer to
"is an audited library enough?" — no: the audit covered OpenMLS, and the critical bug was one
layer down.

What blunts it here is that `openmls` 0.9.0 does **not** depend on `hpke-rs` directly. Its
crypto arrives through a swappable provider — `openmls_rust_crypto` or `openmls_libcrux_crypto`
behind the `openmls_traits` interface. Which provider we pick, and pinning it above the patched
floor, is a decision the ADR owes rather than something inherited.

### The browser precedent is real, and so are its edges

Wire ships MLS to browsers today: **`wireapp/core-crypto`** wraps OpenMLS and produces a
WebAssembly binary through `wasm-bindgen`/`wasm-pack` for web and Electron, with the keystore in
IndexedDB encrypted with AES-256-GCM. That is a shipping E2EE product in our exact shape, and the
strongest single piece of evidence that this path works.

Two edges Wire's own architecture document names, both landing on Phase 3 work items:

- OpenMLS on WASM needs **secure randomness and the current time**, neither of which WASM
  provides — it runs only in a host supplying the usual JavaScript APIs. A browser does.
- Wire's own note: the keystore's **encryption at rest on WASM "isn't validated nor audited."**
  Phase 3's "encrypted backups + user-held recovery keys" line is the same problem, and this says
  the precedent does not solve it for us.

## 4. What this spike does not establish

Named, rather than left for someone to assume the opposite:

- **No code was written and nothing was measured.** WASM bundle size, cold-start cost and
  handshake latency on a mid-range phone browser are all unmeasured. The webapp bundle is ~560 KB
  today (ROADMAP Phase 4); a WASM MLS core is plausibly the same order again, and nobody has
  checked.
- **No storage design.** Where a browser client keeps group state and private keys across
  reloads, and what a private window does to that, is untouched here.
- **0.9.0 is four days old** at the time of writing and its changelog describes substantial,
  breaking changes. The audited-and-remediated versions are 0.8.1 and 0.7.3. Whether to adopt
  0.9.0 or pin lower is a real question, and Q1 asks it.
- **Media E2EE was not researched at all.** LiveKit insertable streams is a separate mechanism
  with a separate key path; `livekit-client` 2.22.1 is already in `webapp/package.json`. It needs
  its own pass.
- Post-quantum is not evaluated. OpenMLS ships ML-KEM through libcrux and ts-mls advertises
  X-Wing; neither is a requirement anybody has stated.

## 5. The three questions ADR 006 must answer

Written as confirm-or-refute, per CLAUDE.md's pre-flight test.

**Q1 — Is the library choice actually forced, and at which version?**
Confirm or refute: the audit requirement eliminates mls-rs and ts-mls, libsignal implements no
MLS, and OpenMLS is therefore the only candidate clearing the bar CLAUDE.md already set — making
this a version-and-provider decision rather than a library one. If so: 0.9.0 (four days old,
breaking) or 0.8.1 (the version the SRLabs remediation shipped in), and which crypto provider,
pinned above the patched `hpke-rs` floor?

**Q2 — Does the Go server need MLS at all?**
Confirm or refute: the server can be a pure delivery service — opaque ciphertext plus a
key-package directory — needing no MLS implementation, so no Go binding is required and the
Rust→WASM boundary is the only integration surface in the product. If refuted, name the exact
thing the server must parse and why membership authorization cannot supply it, because that
answer decides whether Phase 3 needs a Go MLS story at all.

**Q3 — ADR 005's open tension: conference guests.**
Confirm or refute: strict mode must **exclude** guests from encrypted conferences, because a
key-in-fragment scheme cannot bind a key to an authenticated identity — the server mints the
link, so it can mint itself a key — which is precisely the property strict mode exists to deny.
If refuted, the alternative has to say what a guest's key is bound to that the server cannot
forge.

## Sources

- [openmls/openmls](https://github.com/openmls/openmls) · [crates.io/crates/openmls](https://crates.io/crates/openmls) · [WebAssembly — OpenMLS Book](https://book.openmls.tech/user_manual/wasm.html)
- [OpenMLS independent security audit (Phoenix R&D)](https://blog.phnx.im/openmls-independent-security-audit/)
- [awslabs/mls-rs](https://github.com/awslabs/mls-rs) · [crates.io/crates/mls-rs](https://crates.io/crates/mls-rs)
- [LukaJCB/ts-mls](https://github.com/LukaJCB/ts-mls) · [ts-mls on npm](https://www.npmjs.com/package/ts-mls)
- [signalapp/libsignal](https://github.com/signalapp/libsignal) — crate layout, checked directly
- [wireapp/core-crypto ARCHITECTURE.md](https://github.com/wireapp/core-crypto/blob/main/docs/ARCHITECTURE.md)
- RustSec: [RUSTSEC-2026-0071](https://rustsec.org/advisories/RUSTSEC-2026-0071.html) · [RUSTSEC-2026-0070](https://rustsec.org/advisories/RUSTSEC-2026-0070.html) · [RUSTSEC-2026-0069](https://rustsec.org/advisories/RUSTSEC-2026-0069.html)
- [RFC 9420](https://www.rfc-editor.org/rfc/rfc9420.html) · [RFC 9750 — MLS Architecture](https://www.rfc-editor.org/info/rfc9750/)
