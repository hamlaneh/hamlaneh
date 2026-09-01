# Spike — OpenMLS 0.9.0 on wasm32, measured

> **Status: measured facts.** The hands-on half ordered by
> [ADR 006](../adr/006-mls-library-and-boundaries.md), run 2026-08-29 against the exact pins the
> ADR names: `openmls =0.9.0`, `openmls_rust_crypto =0.6.0`, built in a `rust:1.91` container
> (this development host has no Rust toolchain, deliberately). The spike code is scratch work
> and was not committed; every command needed to reproduce it is recorded here. Verdict up
> front: **0.9.0 builds on `wasm32-unknown-unknown`; the ADR's 0.8.1 fallback was not needed
> and is now dead.**

## 1. It builds — with one integration fact worth the whole spike

`cargo build --release --target wasm32-unknown-unknown` succeeds, but only after fixing a
failure that any wrapper crate will hit and whose error message points at the wrong knob:
**there are two `getrandom` versions in the tree, and OpenMLS's `js` feature covers only one.**
`getrandom 0.4.3` (via `openmls` itself) is handled by the `js` feature; `getrandom 0.2.17`
(via `openmls_rust_crypto` → `rand_core 0.6`) is not — OpenMLS's own fix for it (`js-test`)
is test-scoped. The wrapper must carry:

```toml
[target.'cfg(target_arch = "wasm32")'.dependencies]
getrandom = { version = "0.2", features = ["js"] }
```

`openmls`'s `js` feature is itself mandatory — a `compile_error!` fires on wasm32 without it.
No `RUSTFLAGS` cfg was needed. Ciphersuite exercised:
`MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519`.

## 2. Size: +489 KB gzipped, best case — a bundle-doubling, lazy-loadable

| Configuration | raw | gzip -9 |
|---|---:|---:|
| `--release` default (wasm + JS glue) | ~2.70 MB | **~760 KB** |
| `opt-level="z"` + `lto` + `codegen-units=1` + `panic="abort"` + `strip` | 1.56 MB | **489 KB** |
| ...that, plus `wasm-opt -Oz` (binaryen 108) | 1.29 MB | 518 KB |

Two findings that will save the packaging slice a wrong turn:

- **`wasm-opt -Oz` makes the file smaller raw but *larger* gzipped** (489 → 518 KB). If the
  budget is gzipped bytes — and it is, that is what crosses the wire — the cheapest measured
  configuration is the `z`-profile build with **no** post-pass. (Caveat: binaryen 108, Debian
  bookworm's package, which also needed `--all-features` to accept sign-extension opcodes; a
  current binaryen may change this.)
- The existing webapp bundle is ~560 KB gzipped, so the MLS core roughly **doubles the shipped
  application** even at best, and nearly triples it at the naive default. Nothing needs this
  code before a user opens an encrypted conversation, so it ships as a lazily loaded chunk —
  but the number is a budget line now, not a surprise later.

## 3. The round-trip is real, including the browser-reload case

11/11 assertions in a Node harness driving **three separately instantiated** wasm modules
(distinct linear memories, proven by an assertion): Alice creates a group; Bob joins from a key
package via Welcome; application messages round-trip both ways (Persian + emoji plaintext
byte-identical); then Alice's full provider state is exported, a **fresh instance that never
saw the group** is rebuilt from the blob, and it both decrypts a message Bob encrypted *after*
the export and successfully sends — so the sender ratchet and signature key survive the
restart. That is the browser-reload-from-IndexedDB case, simulated honestly.

Wire sizes for the contract design: **key package 275 B**, **Welcome 787 B**, application
ciphertext **175 B for a 30-byte plaintext** (~145 B framing overhead, `AlwaysCiphertext`
policy). The ratchet tree rode in the `RatchetTreeExtension`, so **a Welcome is
self-contained** — the contract needs no separate tree-transfer endpoint for joins.

## 4. Storage: what the group-state slice inherits

Provider storage is a keyed map (`HashMap<Vec<u8>, Vec<u8>>`), not one blob — 12 entries,
~8.5 KB total for a two-member group, dominated by `Tree` and `MessageSecrets`, both of which
grow with membership. Keys are an ASCII label + `serde_json` of a key struct; values are JSON
text (a 32-byte secret costs ~100–130 bytes as a number array). Four consequences, stated as
requirements on later slices rather than left as observations:

- **Raw MLS secrets sit in those values in the clear** — signature private key, epoch secrets,
  message secrets. Persisting the map to IndexedDB as-is persists unwrapped key material. This
  is the concrete form of Wire's "keystore at rest isn't validated" caveat, now measured on our
  own stack. The group-state slice owes a keystore-at-rest design **and honest labeling of what
  it does and does not resist** — a browser cannot fully protect keys from its own profile.
- **The wrapper must implement `openmls_traits`' storage trait itself.** The spike's restore
  path worked by writing through `MemoryStorage`'s public `values` field, which is nobody's
  stability promise. The trait is the stable surface; the public field was a spike shortcut.
- **The storage trait is synchronous; IndexedDB is async.** The adapter shape this forces
  (sync in-memory map, flushed at commit points) is a design item for the wrapper slice, named
  here so it is decided rather than improvised.
- **Restore needs two out-of-band handles**: the signature public key
  (`SignatureKeyPair::read` cannot enumerate) and the `GroupId` (`MlsGroup::load` needs it).
  Whatever the client persists must carry both alongside the map.

## Pins and audit

`Cargo.lock` resolved `hpke-rs 0.7.0` / `hpke-rs-crypto 0.7.0` / `hpke-rs-rust-crypto 0.7.0`
— above the RUSTSEC-2026-0071 patched floor (≥ 0.6.0), confirming the ADR's claim on the real
lockfile rather than on the manifest ranges. `cargo audit`: 198 dependencies, **0
vulnerabilities**; one `unmaintained` warning (`proc-macro-error2`, RUSTSEC-2026-0173),
proc-macro-time only — it does not reach the wasm binary.

## Reproduction

Toolchain: `rust:1.91-bookworm` + `wasm32-unknown-unknown` target + `wasm-bindgen-cli 0.2.127`
+ binaryen (`wasm-opt`) in a container; Node ≥ 20 on any host for the harness. The crate is
~150 lines: a `cdylib` exposing `Client::new / key_package / create_group / add_member / join /
encrypt / decrypt / export_state / from_state` over the quick-start API of openmls 0.9.0, plus
the `getrandom` stanza above. Build with `cargo build --release --target
wasm32-unknown-unknown`, bindgen with `--target bundler` (browser) or `--target nodejs`
(harness), measure with `gzip -9`. The spike's scratch copy lives in this session's scratchpad
and is disposable by design.
